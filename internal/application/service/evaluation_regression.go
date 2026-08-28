package service

// Task007 (G5) — Deterministic Quality Regression Gate.
//
// This file holds the versioned deterministic retrieval fixture schema, the
// offline deterministic runner, and the canonical result schema. The runner
// reuses the PRODUCTION ranking/selection seam (fuseOrDeduplicate -> RRF
// fusion / score-desc dedup in knowledgebase_search_fusion.go) and the
// PRODUCTION metric implementations (internal/application/service/metric),
// with zero network, zero provider, zero secret and zero database access.
//
// The only divergence from the online evaluation pipeline is that the runner
// consumes a frozen fixture of precomputed deterministic scores instead of
// embedding/rerank model outputs. The ranking seam's tie-breaker
// (chunk_id_lexicographic_asc, a total order on ChunkID strings) lives in the
// PRODUCTION sortByScoreDesc comparator, so the runner trusts production order
// exactly. This is a deterministic extraction, not a second retrieval
// implementation.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"sort"
	"strconv"

	"github.com/Tencent/WeKnora/internal/application/service/metric"
	"github.com/Tencent/WeKnora/internal/types"
)

// ---- Version pins (frozen; a change is a schema upgrade, never silent) ----

const (
	regressionFixtureSchemaVersion  = "retrieval-core/1"
	regressionResultSchemaVersion   = "regression_result/1"
	regressionProtocolSchemaVersion = "regression_protocol/1"
)

// ---- Decision taxonomy (four-state, stable exit mapping) ----

// RegressionDecision is the machine-readable gate decision.
type RegressionDecision string

const (
	DecisionPass          RegressionDecision = "PASS"
	DecisionBlock         RegressionDecision = "BLOCK"
	DecisionNotComparable RegressionDecision = "NOT_COMPARABLE"
	DecisionError         RegressionDecision = "ERROR"
)

// ExitCode maps the four-state decision to the process exit contract:
// PASS=0, BLOCK=2, NOT_COMPARABLE=3, ERROR=4. exit 1 is reserved so the three
// failure classes can never be flattened into a single "test failed" signal.
func (d RegressionDecision) ExitCode() int {
	switch d {
	case DecisionPass:
		return 0
	case DecisionBlock:
		return 2
	case DecisionNotComparable:
		return 3
	default:
		return 4
	}
}

// ---- Fixture schema ----

// ScoredCandidate is one deterministic candidate with a precomputed score.
type ScoredCandidate struct {
	DocID int     `json:"doc_id"`
	Score float64 `json:"score"`
}

// RegressionQuery is a single deterministic query with ground truth and the
// scored candidates that would have been returned by the vector / keyword
// retrievers in production.
type RegressionQuery struct {
	QID               int               `json:"qid"`
	Query             string            `json:"query"`
	RelevantIDs       []int             `json:"relevant_ids"`
	VectorCandidates  []ScoredCandidate `json:"vector_candidates"`
	KeywordCandidates []ScoredCandidate `json:"keyword_candidates"`
}

// RegressionFixture is the versioned deterministic retrieval fixture.
type RegressionFixture struct {
	SchemaVersion    string            `json:"schema_version"`
	FixtureID        string            `json:"fixture_id"`
	TopK             int               `json:"top_k"`
	TieBreaker       string            `json:"tie_breaker"`
	RRFK             int               `json:"rrf_k"`
	RRFVectorWeight  float64           `json:"rrf_vector_weight"`
	RRFKeywordWeight float64           `json:"rrf_keyword_weight"`
	Queries          []RegressionQuery `json:"queries"`
}

// ---- Result schema ----

// RegressionMetrics mirrors types.RetrievalMetrics (the production surface).
//
// Decoding from JSON records which metric ids were actually PRESENT, so a
// missing blocking metric can be distinguished from a real observed 0 (missing
// ≠ zero). Metrics built by the runner in-process (never decoded from JSON)
// report all ids present. The presence map is unexported: marshaling is
// unchanged, so ResultArtifactHash is unaffected by this tracking.
type RegressionMetrics struct {
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	MRR       float64 `json:"mrr"`
	MAP       float64 `json:"map"`
	NDCG3     float64 `json:"ndcg3"`
	NDCG10    float64 `json:"ndcg10"`

	present map[string]bool
}

// UnmarshalJSON decodes the typed fields and records which metric ids were
// present in the JSON object (nil-valued/absent fields stay absent).
func (m *RegressionMetrics) UnmarshalJSON(data []byte) error {
	type regressionMetricsAlias RegressionMetrics
	var a regressionMetricsAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	present := map[string]bool{}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	allowed := map[string]struct{}{
		"precision": {}, "recall": {}, "mrr": {}, "map": {}, "ndcg3": {}, "ndcg10": {},
	}
	for k, value := range raw {
		if _, ok := allowed[k]; !ok {
			return fmt.Errorf("unknown regression metric field %q", k)
		}
		// JSON null is unavailable telemetry, not an observed numeric zero.
		if !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			present[k] = true
		}
	}
	*m = RegressionMetrics(a)
	m.present = present
	return nil
}

// HasMetric reports whether metric id m was present in the decoded JSON.
// In-process (runner-built) metrics report all ids present.
func (m RegressionMetrics) HasMetric(id string) bool {
	if m.present == nil {
		return true
	}
	return m.present[id]
}

func (m RegressionMetrics) valuesValid() bool {
	values := [...]float64{m.Precision, m.Recall, m.MRR, m.MAP, m.NDCG3, m.NDCG10}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return false
		}
	}
	return true
}

// RegressionResult is the canonical, comparable result artifact. It carries
// the identity facts needed by the comparison preflight plus the metrics.
// No timestamps, run IDs or git commits live here: those are provenance.
type RegressionResult struct {
	SchemaVersion              string            `json:"schema_version"`
	FixtureID                  string            `json:"fixture_id"`
	FixtureArtifactHash        string            `json:"fixture_artifact_hash"`
	DatasetContentHash         string            `json:"dataset_content_hash"`
	TopK                       int               `json:"top_k"`
	SampleCount                int               `json:"sample_count"`
	Metrics                    RegressionMetrics `json:"metrics"`
	MetricsValid               bool              `json:"metrics_valid"`
	ProtocolHash               string            `json:"protocol_hash"`
	MeasurementContractVersion string            `json:"measurement_contract_version"`
	EvaluatorArtifactHash      string            `json:"evaluator_artifact_hash"`
	MetricDefinitionVersion    string            `json:"metric_definition_version"`
	ComparisonPolicyHash       string            `json:"comparison_policy_hash"`
	// RankingArtifactHash is the SUT (system-under-test) identity: the hash of
	// the production ranking seam the runner executed against. It is provenance,
	// NOT a MUST_EQUAL preflight field — a candidate is allowed to change the
	// ranking seam (that is the point of the regression gate), but the change
	// must not regress metrics. See rankingContractVersion.
	RankingArtifactHash string `json:"ranking_artifact_hash"`
}

// RegressionProtocol is the frozen comparable protocol identity. git_commit,
// build time, run id and go version are NOT part of this hash.
type RegressionProtocol struct {
	SchemaVersion           string  `json:"schema_version"`
	FixtureSchemaVersion    string  `json:"fixture_schema_version"`
	RankingContractVersion  string  `json:"ranking_contract_version"`
	MetricDefinitionVersion string  `json:"metric_definition_version"`
	TopK                    int     `json:"top_k"`
	RRFK                    int     `json:"rrf_k"`
	RRFVectorWeight         float64 `json:"rrf_vector_weight"`
	RRFKeywordWeight        float64 `json:"rrf_keyword_weight"`
}

// ---- Fixture parsing & canonicalization ----

// parseRegressionFixture parses and validates a closed fixture schema. Any
// unknown/invalid field, duplicate QID/doc ID, or negative score fails.
func parseRegressionFixture(raw []byte) (*RegressionFixture, error) {
	var f RegressionFixture
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("fixture decode: %w", err)
	}
	if f.SchemaVersion != regressionFixtureSchemaVersion {
		return nil, fmt.Errorf("fixture schema_version %q != %q", f.SchemaVersion, regressionFixtureSchemaVersion)
	}
	if f.FixtureID == "" {
		return nil, fmt.Errorf("fixture_id is empty")
	}
	if f.TopK <= 0 {
		return nil, fmt.Errorf("top_k must be positive")
	}
	if f.TieBreaker != "chunk_id_lexicographic_asc" {
		return nil, fmt.Errorf("unsupported tie_breaker %q", f.TieBreaker)
	}
	if f.RRFK <= 0 {
		f.RRFK = 60
	}
	if f.RRFVectorWeight <= 0 || f.RRFKeywordWeight <= 0 {
		return nil, fmt.Errorf("rrf weights must be positive")
	}
	seenQID := map[int]struct{}{}
	for _, q := range f.Queries {
		if _, dup := seenQID[q.QID]; dup {
			return nil, fmt.Errorf("duplicate qid %d", q.QID)
		}
		seenQID[q.QID] = struct{}{}
		// A document may legitimately appear in BOTH vector and keyword
		// candidate lists (hybrid retrieval). Duplicates are only rejected
		// within a single retriever's list.
		seenVec := map[int]struct{}{}
		for _, c := range q.VectorCandidates {
			if c.Score < 0 {
				return nil, fmt.Errorf("negative score on qid %d doc %d", q.QID, c.DocID)
			}
			if _, dup := seenVec[c.DocID]; dup {
				return nil, fmt.Errorf("duplicate vector doc %d on qid %d", c.DocID, q.QID)
			}
			seenVec[c.DocID] = struct{}{}
		}
		seenKw := map[int]struct{}{}
		for _, c := range q.KeywordCandidates {
			if c.Score < 0 {
				return nil, fmt.Errorf("negative score on qid %d doc %d", q.QID, c.DocID)
			}
			if _, dup := seenKw[c.DocID]; dup {
				return nil, fmt.Errorf("duplicate keyword doc %d on qid %d", c.DocID, q.QID)
			}
			seenKw[c.DocID] = struct{}{}
		}
	}
	return &f, nil
}

// canonicalFixture sorts queries and candidates so the artifact hash is
// independent of input ordering (still a closed schema, but order-insensitive
// identity for the frozen fixture).
func canonicalFixture(f *RegressionFixture) *RegressionFixture {
	out := *f
	out.Queries = make([]RegressionQuery, len(f.Queries))
	copy(out.Queries, f.Queries)
	sort.SliceStable(out.Queries, func(i, j int) bool { return out.Queries[i].QID < out.Queries[j].QID })
	for i := range out.Queries {
		q := &out.Queries[i]
		sort.SliceStable(q.RelevantIDs, func(a, b int) bool { return q.RelevantIDs[a] < q.RelevantIDs[b] })
		sortCandidates(q.VectorCandidates)
		sortCandidates(q.KeywordCandidates)
	}
	return &out
}

func sortCandidates(cs []ScoredCandidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Score != cs[j].Score {
			return cs[i].Score > cs[j].Score
		}
		return cs[i].DocID < cs[j].DocID
	})
}

// FixtureArtifactHash is the sha256 of the canonical fixture bytes.
func FixtureArtifactHash(f *RegressionFixture) string {
	return sha256Hex(mustJSON(canonicalFixture(f)))
}

// DatasetContentHash is the sha256 of the dataset content only (the canonical
// queries: qid, query, relevant_ids and scored candidates), excluding the
// execution parameters (top_k, rrf weights, tie_breaker, schema_version). It is
// the dataset identity that MUST_EQUAL across baseline and candidate, distinct
// from the full fixture identity.
func DatasetContentHash(f *RegressionFixture) string {
	canon := canonicalFixture(f)
	return sha256Hex(mustJSON(struct {
		Queries []RegressionQuery `json:"queries"`
	}{Queries: canon.Queries}))
}

// ---- Protocol identity ----

func protocolFor(f *RegressionFixture, metricDefinitionVersion string) RegressionProtocol {
	return RegressionProtocol{
		SchemaVersion:           regressionProtocolSchemaVersion,
		FixtureSchemaVersion:    regressionFixtureSchemaVersion,
		RankingContractVersion:  rankingContractVersion,
		MetricDefinitionVersion: metricDefinitionVersion,
		TopK:                    f.TopK,
		RRFK:                    f.RRFK,
		RRFVectorWeight:         f.RRFVectorWeight,
		RRFKeywordWeight:        f.RRFKeywordWeight,
	}
}

// rankingContractVersion pins the production ranking seam semantics consumed
// by the deterministic runner (score-desc selection + RRF fusion, tie-break
// chunk_id_lexicographic_asc on ChunkID strings). The ranking seam is the SUT,
// not part of the evaluator artifact; its identity is RankingArtifactHash.
// See source_trace.md.
const rankingContractVersion = "retrieval-ranking/1"

// ProtocolHash derives the comparable protocol identity (no git commit).
func ProtocolHash(f *RegressionFixture, metricDefinitionVersion string) string {
	return sha256Hex(mustJSON(protocolFor(f, metricDefinitionVersion)))
}

// ---- Deterministic runner ----

// RunRegressionFixture executes the deterministic runner over a fixture and
// returns the canonical result. It is the offline core of the gate.
//
// rankingArtifactHash is the SUT identity of the production ranking seam the
// runner is executing against (computed from the seam source by the caller /
// workflow). It is provenance, not a MUST_EQUAL field.
func RunRegressionFixture(ctx context.Context, f *RegressionFixture, evaluatorArtifactHash, metricDefinitionVersion, policyHash, rankingArtifactHash string) (*RegressionResult, error) {
	cfg := &types.RetrievalConfig{
		RRFK:             f.RRFK,
		RRFVectorWeight:  f.RRFVectorWeight,
		RRFKeywordWeight: f.RRFKeywordWeight,
	}

	ranked := make(map[int][]int, len(f.Queries))
	relevant := make(map[int][]int, len(f.Queries))
	for _, q := range f.Queries {
		relevant[q.QID] = append([]int(nil), q.RelevantIDs...)
		ranked[q.QID] = rankQuery(ctx, q, f.TopK, cfg)
	}

	metrics := aggregateMetrics(ranked, relevant)

	return &RegressionResult{
		SchemaVersion:              regressionResultSchemaVersion,
		FixtureID:                  f.FixtureID,
		FixtureArtifactHash:        FixtureArtifactHash(f),
		DatasetContentHash:         DatasetContentHash(f),
		TopK:                       f.TopK,
		SampleCount:                len(f.Queries),
		Metrics:                    metrics,
		MetricsValid:               true,
		ProtocolHash:               ProtocolHash(f, metricDefinitionVersion),
		MeasurementContractVersion: measurementContractVersion,
		EvaluatorArtifactHash:      evaluatorArtifactHash,
		MetricDefinitionVersion:    metricDefinitionVersion,
		ComparisonPolicyHash:       policyHash,
		RankingArtifactHash:        rankingArtifactHash,
	}, nil
}

// rankQuery maps a fixture query through the PRODUCTION ranking seam:
// build IndexWithScore slices, call fuseOrDeduplicate (RRF / score-desc dedup),
// apply the explicit deterministic tie-breaker, truncate to topK, and return
// ordered document IDs.
func rankQuery(ctx context.Context, q RegressionQuery, topK int, cfg *types.RetrievalConfig) []int {
	vectorResults := toIndexWithScore(q.VectorCandidates)
	keywordResults := toIndexWithScore(q.KeywordCandidates)

	// Trust the PRODUCTION ranking order exactly. The production comparator
	// (sortByScoreDesc) already applies the deterministic tie-breaker
	// (chunk_id_lexicographic_asc), so fuseOrDeduplicate returns a total order.
	fused := fuseOrDeduplicate(ctx, vectorResults, keywordResults, cfg)

	out := make([]int, 0, topK)
	for i, r := range fused {
		if i >= topK {
			break
		}
		id, err := strconv.Atoi(r.ChunkID)
		if err != nil {
			continue
		}
		out = append(out, id)
	}
	return out
}

func toIndexWithScore(cs []ScoredCandidate) []*types.IndexWithScore {
	// Production retrievers return results sorted by score desc; mirror that
	// so fuseWithRRF rank maps (index+1) are correct.
	sortCandidates(cs)
	out := make([]*types.IndexWithScore, 0, len(cs))
	for _, c := range cs {
		out = append(out, &types.IndexWithScore{
			ChunkID: strconv.Itoa(c.DocID),
			Score:   c.Score,
		})
	}
	return out
}

// aggregateMetrics mirrors production HookMetric.recordFinish + MetricList.Avg:
// per-query metric scores averaged across queries in stable (sorted) order.
func aggregateMetrics(ranked, relevant map[int][]int) RegressionMetrics {
	qids := make([]int, 0, len(relevant))
	for qid := range relevant {
		qids = append(qids, qid)
	}
	sort.Ints(qids)

	var agg RegressionMetrics
	if len(qids) == 0 {
		return agg
	}
	for _, qid := range qids {
		rel := relevant[qid]
		ids := ranked[qid]
		in := &types.MetricInput{RetrievalGT: [][]int{rel}, RetrievalIDs: ids}
		agg.Precision += metric.NewPrecisionMetric().Compute(in)
		agg.Recall += metric.NewRecallMetric().Compute(in)
		agg.MRR += metric.NewMRRMetric().Compute(in)
		agg.MAP += metric.NewMAPMetric().Compute(in)
		agg.NDCG3 += metric.NewNDCGMetric(3).Compute(in)
		agg.NDCG10 += metric.NewNDCGMetric(10).Compute(in)
	}
	n := float64(len(qids))
	agg.Precision /= n
	agg.Recall /= n
	agg.MRR /= n
	agg.MAP /= n
	agg.NDCG3 /= n
	agg.NDCG10 /= n
	return agg
}

// ---- Hashing helpers ----

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// measurementContractVersion pins the retrieval evaluator measurement contract
// (separate from the online evaluation_protocol/1 which remains UNVERSIONED
// for historical runs).
const measurementContractVersion = "retrieval-evaluator/1"

// artifactHashFiles computes the artifact hash over a list of file paths,
// each serialized as "FILE <path>\n<file-bytes>", in sorted path order. This
// is the canonical evaluator/ranking artifact hash method (same as the frozen
// manifest). Returns an error if any file cannot be read, so the runner can
// VERIFY the evaluator identity instead of self-reporting it.
func artifactHashFiles(paths []string) (string, error) {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, p := range sorted {
		content, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("artifact component read %s: %w", p, err)
		}
		fmt.Fprintf(h, "FILE %s\n", p)
		h.Write(content)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// EvaluatorArtifactHash computes the evaluator artifact hash over the given
// component files (the measurement apparatus: runner + metric implementations).
// The production ranking seam (SUT) is deliberately NOT part of this hash.
func EvaluatorArtifactHash(componentFiles []string) (string, error) {
	return artifactHashFiles(componentFiles)
}

// RankingArtifactHash computes the SUT identity hash over the production
// ranking seam content at its frozen logical code location. The caller's
// checkout/temp path is deliberately excluded so local and CI candidate
// checkouts identify byte-identical SUTs identically. This is provenance, not
// a MUST_EQUAL evaluator field.
func RankingArtifactHash(rankingFile string) (string, error) {
	content, err := os.ReadFile(rankingFile)
	if err != nil {
		return "", fmt.Errorf("ranking seam read %s: %w", rankingFile, err)
	}
	return rankingArtifactHashContent(content), nil
}

const rankingSeamLogicalPath = "internal/application/service/knowledgebase_search_fusion.go"

func rankingArtifactHashContent(content []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "FILE %s\n", rankingSeamLogicalPath)
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// SUT (ranking seam) boundary rules. The ranking seam is the ONLY file the
// candidate is allowed to change; everything around it (evaluator, metrics,
// fixture, policy, baseline, runner) is trusted and hash-locked. To keep that
// boundary honest, the seam itself must not be able to reach outside the
// deterministic slice: no network egress, no external provider clients, no
// database, no subprocess execution, no file-system mutation, and no
// init()-time side effects. These are the same forbidden patterns enforced
// by the workflow's SUT boundary audit step.
//
// This is deliberately an import allowlist rather than a symbol blacklist.
// Blacklists are bypassable with import aliases (for example n "net"). Any
// expansion is a Measurement Change Review because it expands executable SUT
// authority inside the trusted runner process.
var allowedRankingSeamImports = map[string]struct{}{
	"context": {},
	"slices":  {},
	"github.com/Tencent/WeKnora/internal/logger": {},
	"github.com/Tencent/WeKnora/internal/types":  {},
}

// ValidateRankingSeamSource statically enforces the SUT boundary over the raw
// source of the candidate ranking seam. It reports the first forbidden pattern
// found. A clean seam is a precondition for hashing it (RankingSeamIdentity).
func ValidateRankingSeamSource(content []byte) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "ranking_seam.go", content, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("ranking seam parse: %w", err)
	}
	if file.Name == nil || file.Name.Name != "service" {
		return fmt.Errorf("ranking seam boundary violation: package must be service")
	}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return fmt.Errorf("ranking seam boundary violation: invalid import %s", spec.Path.Value)
		}
		if _, ok := allowedRankingSeamImports[path]; !ok {
			return fmt.Errorf("ranking seam boundary violation: import %q is not allowlisted", path)
		}
		if spec.Name != nil {
			return fmt.Errorf("ranking seam boundary violation: import alias %q is not allowed", spec.Name.Name)
		}
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			// Package variables can execute arbitrary initializers before main and
			// retain mutable state across fixture cases. The current production seam
			// needs only imports, constants, types and functions.
			if d.Tok == token.VAR {
				return fmt.Errorf("ranking seam boundary violation: package-level variables are not allowed")
			}
		case *ast.FuncDecl:
			if d.Name != nil && d.Name.Name == "init" {
				return fmt.Errorf("ranking seam boundary violation: init function is not allowed")
			}
		}
	}
	return nil
}

// RankingSeamIdentity validates the SUT boundary and then computes the SUT
// identity hash. Validation-before-hash means a boundary-violating candidate
// seam can never be recorded as a comparable ranking identity.
func RankingSeamIdentity(rankingFile string) (string, error) {
	content, err := os.ReadFile(rankingFile)
	if err != nil {
		return "", fmt.Errorf("ranking seam read %s: %w", rankingFile, err)
	}
	if err := ValidateRankingSeamSource(content); err != nil {
		return "", err
	}
	return rankingArtifactHashContent(content), nil
}

// ---- Exported machine-interface wrappers ----

// ParseRegressionFixture is the exported fixture parser for the CLI/verifier.
func ParseRegressionFixture(raw []byte) (*RegressionFixture, error) {
	return parseRegressionFixture(raw)
}

// ParseRegressionResult is the exported result parser (closed schema).
func ParseRegressionResult(raw []byte) (*RegressionResult, error) {
	var r RegressionResult
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("result decode: %w", err)
	}
	if r.SchemaVersion != regressionResultSchemaVersion {
		return nil, fmt.Errorf("result schema_version %q != %q", r.SchemaVersion, regressionResultSchemaVersion)
	}
	return &r, nil
}

// ResultArtifactHash is the comparable result identity: sha256 of the canonical
// result JSON (deterministic field order, no timestamps/provenance). The
// baseline stores this value so a byte-identical rerun is provable.
func (r *RegressionResult) ResultArtifactHash() string {
	return sha256Hex(mustJSON(r))
}
