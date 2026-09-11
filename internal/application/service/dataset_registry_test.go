package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/parquet-go/parquet-go"
)

// ---- test dataset writer helpers ---------------------------------------

func writeParquet[T any](t *testing.T, path string, rows []T) {
	t.Helper()
	if err := parquet.WriteFile[T](path, rows); err != nil {
		t.Fatalf("write parquet %s: %v", path, err)
	}
}

func writeSampleDataset(t *testing.T, dir string, qidBase int64, passageText string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	base := strconv.FormatInt(qidBase, 10)
	queries := []TextInfo{{ID: qidBase, Text: "question " + base}}
	corpus := []TextInfo{
		{ID: qidBase + 1, Text: passageText + " passage A"},
		{ID: qidBase + 2, Text: passageText + " passage B"},
	}
	answers := []TextInfo{{ID: qidBase + 3, Text: "answer " + base}}
	qrels := []RelsInfo{{QID: qidBase, PID: qidBase + 1}, {QID: qidBase, PID: qidBase + 2}}
	qas := []QaInfo{{QID: qidBase, AID: qidBase + 3}}
	writeParquet(t, filepath.Join(dir, "queries.parquet"), queries)
	writeParquet(t, filepath.Join(dir, "corpus.parquet"), corpus)
	writeParquet(t, filepath.Join(dir, "answers.parquet"), answers)
	writeParquet(t, filepath.Join(dir, "qrels.parquet"), qrels)
	writeParquet(t, filepath.Join(dir, "qas.parquet"), qas)
}

func manifestFromDir(t *testing.T, datasetID, dir string) datasetManifest {
	t.Helper()
	m := datasetManifest{DatasetID: datasetID, SchemaVersion: datasetSchemaVersion, BaseDir: dir}
	add := func(name string, rowCount int64) {
		p := filepath.Join(dir, name+".parquet")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		sum := sha256.Sum256(b)
		m.Files = append(m.Files, datasetFileManifest{Name: name, Path: name + ".parquet", SHA256: hex.EncodeToString(sum[:]), RowCount: rowCount})
	}
	q, _ := loadParquet[TextInfo](filepath.Join(dir, "queries.parquet"))
	c, _ := loadParquet[TextInfo](filepath.Join(dir, "corpus.parquet"))
	a, _ := loadParquet[TextInfo](filepath.Join(dir, "answers.parquet"))
	qr, _ := loadParquet[RelsInfo](filepath.Join(dir, "qrels.parquet"))
	qa, _ := loadParquet[QaInfo](filepath.Join(dir, "qas.parquet"))
	add("queries", int64(len(q)))
	add("corpus", int64(len(c)))
	add("answers", int64(len(a)))
	add("qrels", int64(len(qr)))
	add("qas", int64(len(qa)))
	return m
}

func newRegistryWith(t *testing.T, manifests ...datasetManifest) *datasetRegistry {
	t.Helper()
	r := &datasetRegistry{byID: make(map[string]datasetManifest)}
	for _, m := range manifests {
		r.byID[m.DatasetID] = m
	}
	return r
}

// ---- positive + determinism --------------------------------------------

func TestDatasetDefaultLoads(t *testing.T) {
	svc := NewDatasetService()
	pairs, err := svc.GetDatasetByID(context.Background(), "default")
	if err != nil {
		t.Fatalf("load default: %v", err)
	}
	if len(pairs) == 0 {
		t.Fatalf("expected non-empty default dataset")
	}
	// Empty datasetID must normalize to default without error (I01 non-fallback).
	pairs2, err := svc.GetDatasetByID(context.Background(), "")
	if err != nil {
		t.Fatalf("load empty id: %v", err)
	}
	if len(pairs2) != len(pairs) {
		t.Fatalf("empty id should normalize to default")
	}
}

func TestDatasetDeterministicOrderAndHash(t *testing.T) {
	svc := NewDatasetService()
	a, err := svc.GetDatasetByID(context.Background(), "default")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	b, err := svc.GetDatasetByID(context.Background(), "default")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if datasetContentHash(a) != datasetContentHash(b) {
		t.Fatalf("content hash not deterministic across loads")
	}
	// Sorted canonical order must be stable.
	for i := 1; i < len(a); i++ {
		if a[i].QID < a[i-1].QID {
			t.Fatalf("pairs not sorted by QID at index %d", i)
		}
	}
}

func TestDatasetTwoIDsLoadDifferentContentAndHash(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeSampleDataset(t, dirA, 1, "alpha")
	writeSampleDataset(t, dirB, 101, "beta")
	reg := newRegistryWith(t,
		manifestFromDir(t, "dataset-a", dirA),
		manifestFromDir(t, "dataset-b", dirB),
	)
	pa, err := reg.load(context.Background(), "dataset-a")
	if err != nil {
		t.Fatalf("load a: %v", err)
	}
	pb, err := reg.load(context.Background(), "dataset-b")
	if err != nil {
		t.Fatalf("load b: %v", err)
	}
	if datasetContentHash(pa) == datasetContentHash(pb) {
		t.Fatalf("two different datasets must hash differently")
	}
	if pa[0].Question == pb[0].Question {
		t.Fatalf("two different datasets must have different content")
	}
}

// ---- typed negative controls (no panic) --------------------------------

func TestDatasetUnknownIDIsTypedErrorNoFallback(t *testing.T) {
	svc := NewDatasetService()
	_, err := svc.GetDatasetByID(context.Background(), "does-not-exist")
	if !errors.Is(err, ErrDatasetNotFound) {
		t.Fatalf("got %v, want ErrDatasetNotFound", err)
	}
}

func TestDatasetMissingFileIsTypedError(t *testing.T) {
	dir := t.TempDir()
	writeSampleDataset(t, dir, 1, "alpha")
	m := manifestFromDir(t, "d", dir)
	os.Remove(filepath.Join(dir, "corpus.parquet"))
	reg := newRegistryWith(t, m)
	_, err := reg.load(context.Background(), "d")
	if !errors.Is(err, ErrDatasetFileMissing) {
		t.Fatalf("got %v, want ErrDatasetFileMissing", err)
	}
}

func TestDatasetHashMismatchIsTypedError(t *testing.T) {
	dir := t.TempDir()
	writeSampleDataset(t, dir, 1, "alpha")
	m := manifestFromDir(t, "d", dir)
	// Corrupt corpus.parquet bytes.
	p := filepath.Join(dir, "corpus.parquet")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f.Write([]byte("corrupt"))
	f.Close()
	reg := newRegistryWith(t, m)
	if _, err := reg.load(context.Background(), "d"); !errors.Is(err, ErrDatasetHashMismatch) {
		t.Fatalf("got %v, want ErrDatasetHashMismatch", err)
	}
}

func TestDatasetDuplicatePassageIDIsTypedError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeParquet(t, filepath.Join(dir, "queries.parquet"), []TextInfo{{ID: 1, Text: "q"}})
	// duplicate passage id 2
	writeParquet(t, filepath.Join(dir, "corpus.parquet"), []TextInfo{{ID: 2, Text: "a"}, {ID: 2, Text: "b"}})
	writeParquet(t, filepath.Join(dir, "answers.parquet"), []TextInfo{{ID: 3, Text: "ans"}})
	writeParquet(t, filepath.Join(dir, "qrels.parquet"), []RelsInfo{{QID: 1, PID: 2}})
	writeParquet(t, filepath.Join(dir, "qas.parquet"), []QaInfo{{QID: 1, AID: 3}})
	m := manifestFromDir(t, "d", dir)
	reg := newRegistryWith(t, m)
	if _, err := reg.load(context.Background(), "d"); !errors.Is(err, ErrDatasetDuplicateID) {
		t.Fatalf("got %v, want ErrDatasetDuplicateID", err)
	}
}

func TestDatasetDanglingQrelIsSchemaError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeParquet(t, filepath.Join(dir, "queries.parquet"), []TextInfo{{ID: 1, Text: "q"}})
	writeParquet(t, filepath.Join(dir, "corpus.parquet"), []TextInfo{{ID: 2, Text: "a"}})
	writeParquet(t, filepath.Join(dir, "answers.parquet"), []TextInfo{{ID: 3, Text: "ans"}})
	// qrel references pid 999 not in corpus
	writeParquet(t, filepath.Join(dir, "qrels.parquet"), []RelsInfo{{QID: 1, PID: 999}})
	writeParquet(t, filepath.Join(dir, "qas.parquet"), []QaInfo{{QID: 1, AID: 3}})
	m := manifestFromDir(t, "d", dir)
	reg := newRegistryWith(t, m)
	if _, err := reg.load(context.Background(), "d"); !errors.Is(err, ErrDatasetSchemaInvalid) {
		t.Fatalf("got %v, want ErrDatasetSchemaInvalid", err)
	}
}

func TestDatasetDuplicateQrelIsTypedError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeParquet(t, filepath.Join(dir, "queries.parquet"), []TextInfo{{ID: 1, Text: "q"}})
	writeParquet(t, filepath.Join(dir, "corpus.parquet"), []TextInfo{{ID: 2, Text: "a"}})
	writeParquet(t, filepath.Join(dir, "answers.parquet"), []TextInfo{{ID: 3, Text: "ans"}})
	// duplicate (qid,pid) relation
	writeParquet(t, filepath.Join(dir, "qrels.parquet"), []RelsInfo{{QID: 1, PID: 2}, {QID: 1, PID: 2}})
	writeParquet(t, filepath.Join(dir, "qas.parquet"), []QaInfo{{QID: 1, AID: 3}})
	m := manifestFromDir(t, "d", dir)
	reg := newRegistryWith(t, m)
	if _, err := reg.load(context.Background(), "d"); !errors.Is(err, ErrDatasetDuplicateID) {
		t.Fatalf("got %v, want ErrDatasetDuplicateID", err)
	}
}

func TestDatasetUnsupportedSchemaVersionIsTypedError(t *testing.T) {
	dir := t.TempDir()
	writeSampleDataset(t, dir, 1, "alpha")
	m := manifestFromDir(t, "d", dir)
	m.SchemaVersion = "dataset/999"
	reg := newRegistryWith(t, m)
	if _, err := reg.load(context.Background(), "d"); !errors.Is(err, ErrDatasetSchemaInvalid) {
		t.Fatalf("got %v, want ErrDatasetSchemaInvalid", err)
	}
}

func TestDatasetTruncatedParquetNoPanic(t *testing.T) {
	dir := t.TempDir()
	writeSampleDataset(t, dir, 1, "alpha")
	// Truncate corpus.parquet to a few bytes — previously DefaultDataset would
	// panic; now the loader must return a typed error (hash mismatch or parse).
	p := filepath.Join(dir, "corpus.parquet")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(p, b[:8], 0o644); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	m := manifestFromDir(t, "d", dir)
	reg := newRegistryWith(t, m)
	if _, err := reg.load(context.Background(), "d"); err == nil {
		t.Fatalf("expected an error for truncated parquet, got nil")
	}
}

func TestDatasetDuplicateQueryIDIsTypedError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeParquet(t, filepath.Join(dir, "queries.parquet"), []TextInfo{{ID: 1, Text: "q1"}, {ID: 1, Text: "q2"}})
	writeParquet(t, filepath.Join(dir, "corpus.parquet"), []TextInfo{{ID: 2, Text: "a"}})
	writeParquet(t, filepath.Join(dir, "answers.parquet"), []TextInfo{{ID: 3, Text: "ans"}})
	writeParquet(t, filepath.Join(dir, "qrels.parquet"), []RelsInfo{{QID: 1, PID: 2}})
	writeParquet(t, filepath.Join(dir, "qas.parquet"), []QaInfo{{QID: 1, AID: 3}})
	m := manifestFromDir(t, "d", dir)
	reg := newRegistryWith(t, m)
	if _, err := reg.load(context.Background(), "d"); !errors.Is(err, ErrDatasetDuplicateID) {
		t.Fatalf("got %v, want ErrDatasetDuplicateID", err)
	}
}

func TestDatasetQasUnknownAIDIsSchemaError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeParquet(t, filepath.Join(dir, "queries.parquet"), []TextInfo{{ID: 1, Text: "q"}})
	writeParquet(t, filepath.Join(dir, "corpus.parquet"), []TextInfo{{ID: 2, Text: "a"}})
	writeParquet(t, filepath.Join(dir, "answers.parquet"), []TextInfo{{ID: 3, Text: "ans"}})
	writeParquet(t, filepath.Join(dir, "qrels.parquet"), []RelsInfo{{QID: 1, PID: 2}})
	// qa references aid 999 not in answers
	writeParquet(t, filepath.Join(dir, "qas.parquet"), []QaInfo{{QID: 1, AID: 999}})
	m := manifestFromDir(t, "d", dir)
	reg := newRegistryWith(t, m)
	if _, err := reg.load(context.Background(), "d"); !errors.Is(err, ErrDatasetSchemaInvalid) {
		t.Fatalf("got %v, want ErrDatasetSchemaInvalid", err)
	}
}

func TestDatasetQasUnknownQIDIsSchemaError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeParquet(t, filepath.Join(dir, "queries.parquet"), []TextInfo{{ID: 1, Text: "q"}})
	writeParquet(t, filepath.Join(dir, "corpus.parquet"), []TextInfo{{ID: 2, Text: "a"}})
	writeParquet(t, filepath.Join(dir, "answers.parquet"), []TextInfo{{ID: 3, Text: "ans"}})
	writeParquet(t, filepath.Join(dir, "qrels.parquet"), []RelsInfo{{QID: 1, PID: 2}})
	// qa references qid 999 not in queries (dangling, silently ignored before)
	writeParquet(t, filepath.Join(dir, "qas.parquet"), []QaInfo{{QID: 999, AID: 3}})
	m := manifestFromDir(t, "d", dir)
	reg := newRegistryWith(t, m)
	if _, err := reg.load(context.Background(), "d"); !errors.Is(err, ErrDatasetSchemaInvalid) {
		t.Fatalf("got %v, want ErrDatasetSchemaInvalid", err)
	}
}
