package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// datasetSchemaVersion pins the dataset manifest + parquet schema shape so a
// future schema change cannot silently collide with an old manifest.
const datasetSchemaVersion = "dataset/1"

// Typed dataset errors. Each maps to a distinct, stable reason so the caller can
// fail closed without falling back to the default dataset (I01/I02, F01/F02).
var (
	ErrDatasetNotFound      = errors.New("dataset not found")
	ErrDatasetFileMissing   = errors.New("dataset file missing")
	ErrDatasetHashMismatch  = errors.New("dataset file hash mismatch")
	ErrDatasetSchemaInvalid = errors.New("dataset schema invalid")
	ErrDatasetDuplicateID   = errors.New("dataset duplicate id")
)

// datasetFileManifest is the content identity of one parquet file.
type datasetFileManifest struct {
	Name     string `json:"name"`      // logical role: queries/corpus/answers/qrels/qas
	Path     string `json:"path"`      // relative to the dataset base dir
	SHA256   string `json:"sha256"`    // frozen content hash
	RowCount int64  `json:"row_count"` // frozen row count (audit + belt-and-suspenders)
}

// datasetManifest is the immutable, content-addressed identity of a dataset.
type datasetManifest struct {
	DatasetID     string                `json:"dataset_id"`
	SchemaVersion string                `json:"schema_version"`
	BaseDir       string                `json:"base_dir"`
	Files         []datasetFileManifest `json:"files"`
}

// datasetRegistry resolves dataset_id -> immutable manifest (plan §4.1 方案 B:
// config registry + immutable manifest, no DB catalog). Adding a dataset is a
// config change (register a manifest), never a runtime fallback.
type datasetRegistry struct {
	byID map[string]datasetManifest
}

func newDatasetRegistry(baseDir string) *datasetRegistry {
	r := &datasetRegistry{byID: make(map[string]datasetManifest)}
	r.byID["default"] = defaultDatasetManifest(baseDir)
	return r
}

// defaultDatasetManifest is the frozen manifest for the shipped sample dataset.
// The SHA-256 values and row counts were frozen at Task016 Step 2 from the
// repository files and MUST NOT be regenerated at runtime (a regenerated hash
// would silently accept a corrupted file — I02, F02).
func defaultDatasetManifest(baseDir string) datasetManifest {
	return datasetManifest{
		DatasetID:     "default",
		SchemaVersion: datasetSchemaVersion,
		BaseDir:       baseDir,
		Files: []datasetFileManifest{
			{Name: "queries", Path: "queries.parquet", SHA256: "4e727c93692ed7676fcad97c91ac284a527f1e7e5a8bf41216c5e23facbadc56", RowCount: 1},
			{Name: "corpus", Path: "corpus.parquet", SHA256: "7cddf67a5ed45d4191aa1408e86353293d31aa71986226d97ee228a7607ec15e", RowCount: 4},
			{Name: "answers", Path: "answers.parquet", SHA256: "ff052131209af612274b19534d7a9a4ec8a40c48aeef72b2f8ce046e7517255e", RowCount: 1},
			{Name: "qrels", Path: "qrels.parquet", SHA256: "72ec24350f5298b4e1ba854ddf1d9d88e421265e61ef7a711c7debaa88c324f3", RowCount: 4},
			{Name: "qas", Path: "qas.parquet", SHA256: "fd4b49ed769f758e14b807e4f6c4b6581b163e68b13af8b1d3d4cf1bdac88f78", RowCount: 1},
		},
	}
}

// defaultDatasetBaseDir resolves the dataset base dir independent of cwd (tests
// run from package dirs; the app runs from the repo/app root). An explicit env
// override wins.
func defaultDatasetBaseDir() string {
	if v := os.Getenv("WEKNORA_DATASET_DIR"); v != "" {
		return v
	}
	dir, err := os.Getwd()
	if err != nil {
		return "./dataset/samples"
	}
	for {
		candidate := filepath.Join(dir, "dataset", "samples")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "./dataset/samples"
}

// load resolves the manifest, verifies every file's content identity, parses
// the files and returns deterministic QA pairs sorted by QID. It never panics
// and never falls back to the default dataset for an unknown ID (I01).
func (r *datasetRegistry) load(ctx context.Context, datasetID string) ([]*types.QAPair, error) {
	if datasetID == "" {
		datasetID = "default"
	}
	m, ok := r.byID[datasetID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrDatasetNotFound, datasetID)
	}
	if m.SchemaVersion != datasetSchemaVersion {
		return nil, fmt.Errorf("%w: unsupported schema version %q", ErrDatasetSchemaInvalid, m.SchemaVersion)
	}

	// 1. Verify every file's existence + frozen content hash (no panic on
	//    missing/corrupt files — I02, F02).
	for _, f := range m.Files {
		if err := verifyDatasetFile(filepath.Join(m.BaseDir, f.Path), f); err != nil {
			return nil, err
		}
	}

	// 2. Parse each file.
	queries, err := loadParquet[TextInfo](filepath.Join(m.BaseDir, "queries.parquet"))
	if err != nil {
		return nil, fmt.Errorf("%w: queries: %v", ErrDatasetFileMissing, err)
	}
	corpus, err := loadParquet[TextInfo](filepath.Join(m.BaseDir, "corpus.parquet"))
	if err != nil {
		return nil, fmt.Errorf("%w: corpus: %v", ErrDatasetFileMissing, err)
	}
	answers, err := loadParquet[TextInfo](filepath.Join(m.BaseDir, "answers.parquet"))
	if err != nil {
		return nil, fmt.Errorf("%w: answers: %v", ErrDatasetFileMissing, err)
	}
	qrels, err := loadParquet[RelsInfo](filepath.Join(m.BaseDir, "qrels.parquet"))
	if err != nil {
		return nil, fmt.Errorf("%w: qrels: %v", ErrDatasetFileMissing, err)
	}
	qas, err := loadParquet[QaInfo](filepath.Join(m.BaseDir, "qas.parquet"))
	if err != nil {
		return nil, fmt.Errorf("%w: qas: %v", ErrDatasetFileMissing, err)
	}

	// 3. Row-count parity (hash already covers content; this catches a manifest
	//    typo and provides an auditable row count).
	if err := checkRowCount("queries", int64(len(queries)), m); err != nil {
		return nil, err
	}
	if err := checkRowCount("corpus", int64(len(corpus)), m); err != nil {
		return nil, err
	}
	if err := checkRowCount("answers", int64(len(answers)), m); err != nil {
		return nil, err
	}
	if err := checkRowCount("qrels", int64(len(qrels)), m); err != nil {
		return nil, err
	}
	if err := checkRowCount("qas", int64(len(qas)), m); err != nil {
		return nil, err
	}

	// 4. Build deterministic, validated QA pairs (sorted by QID).
	pairs, err := buildQAPairs(queries, corpus, answers, qrels, qas)
	if err != nil {
		return nil, err
	}
	logger.Infof(ctx, "Loaded dataset %q: %d QA pairs", datasetID, len(pairs))
	return pairs, nil
}

func verifyDatasetFile(path string, f datasetFileManifest) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrDatasetFileMissing, path)
		}
		return fmt.Errorf("%w: %s: %v", ErrDatasetFileMissing, path, err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != f.SHA256 {
		return fmt.Errorf("%w: %s", ErrDatasetHashMismatch, path)
	}
	return nil
}

func checkRowCount(name string, got int64, m datasetManifest) error {
	for _, f := range m.Files {
		if f.Name == name && got != f.RowCount {
			return fmt.Errorf("%w: %s row_count=%d want %d", ErrDatasetSchemaInvalid, name, got, f.RowCount)
		}
	}
	return nil
}

// buildQAPairs converts raw parquet rows into deterministic QA pairs, validating
// duplicate IDs and dangling references (typed reasons, no silent guessing).
func buildQAPairs(queries, corpus, answers []TextInfo, qrels []RelsInfo, qas []QaInfo) ([]*types.QAPair, error) {
	qmap := make(map[int64]string, len(queries))
	for _, q := range queries {
		if _, dup := qmap[q.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate query id %d", ErrDatasetDuplicateID, q.ID)
		}
		qmap[q.ID] = q.Text
	}
	cmap := make(map[int64]string, len(corpus))
	for _, c := range corpus {
		if _, dup := cmap[c.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate passage id %d", ErrDatasetDuplicateID, c.ID)
		}
		cmap[c.ID] = c.Text
	}
	amap := make(map[int64]string, len(answers))
	for _, a := range answers {
		if _, dup := amap[a.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate answer id %d", ErrDatasetDuplicateID, a.ID)
		}
		amap[a.ID] = a.Text
	}

	qrelsMap := make(map[int64][]int64)
	seenRel := make(map[[2]int64]struct{})
	for _, rel := range qrels {
		if _, ok := qmap[rel.QID]; !ok {
			return nil, fmt.Errorf("%w: qrel qid=%d not present in queries", ErrDatasetSchemaInvalid, rel.QID)
		}
		if _, ok := cmap[rel.PID]; !ok {
			return nil, fmt.Errorf("%w: qrel qid=%d references unknown pid=%d", ErrDatasetSchemaInvalid, rel.QID, rel.PID)
		}
		key := [2]int64{rel.QID, rel.PID}
		if _, dup := seenRel[key]; dup {
			return nil, fmt.Errorf("%w: duplicate qrel (qid=%d, pid=%d)", ErrDatasetDuplicateID, rel.QID, rel.PID)
		}
		seenRel[key] = struct{}{}
		qrelsMap[rel.QID] = append(qrelsMap[rel.QID], rel.PID)
	}

	qasMap := make(map[int64]int64)
	for _, qa := range qas {
		if _, ok := qmap[qa.QID]; !ok {
			return nil, fmt.Errorf("%w: qa qid=%d not present in queries", ErrDatasetSchemaInvalid, qa.QID)
		}
		if _, dup := qasMap[qa.QID]; dup {
			return nil, fmt.Errorf("%w: duplicate qa qid=%d", ErrDatasetDuplicateID, qa.QID)
		}
		qasMap[qa.QID] = qa.AID
	}

	qids := make([]int64, 0, len(qmap))
	for qid := range qmap {
		qids = append(qids, qid)
	}
	sort.Slice(qids, func(i, j int) bool { return qids[i] < qids[j] })

	pairs := make([]*types.QAPair, 0, len(qids))
	for _, qid := range qids {
		pids := qrelsMap[qid]
		pidInts := make([]int, len(pids))
		passages := make([]string, len(pids))
		for i, pid := range pids {
			pidInts[i] = int(pid)
			passages[i] = cmap[pid]
		}
		aid := int64(0)
		answer := ""
		if a, ok := qasMap[qid]; ok {
			if text, exists := amap[a]; exists {
				aid = a
				answer = text
			} else {
				return nil, fmt.Errorf("%w: qa qid=%d references unknown aid=%d", ErrDatasetSchemaInvalid, qid, a)
			}
		}
		pairs = append(pairs, &types.QAPair{
			QID:      int(qid),
			Question: qmap[qid],
			PIDs:     pidInts,
			Passages: passages,
			AID:      int(aid),
			Answer:   answer,
		})
	}
	return pairs, nil
}
