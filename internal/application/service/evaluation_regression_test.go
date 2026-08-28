package service

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	testEvaluatorHash    = "evaluator-artifact-sha256-64hex-placeholder"
	testMetricDefVersion = "retrieval-metrics/1"
	testPolicyHash       = "policy-sha256-64hex-placeholder"
	testRankingHash      = "ranking-sha256-64hex-placeholder"
)

func loadTestFixture(t *testing.T) *RegressionFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "evaluation", "fixtures", "retrieval_core_v1.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	f, err := parseRegressionFixture(raw)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return f
}

func TestRunRegressionFixture_Determinism(t *testing.T) {
	f := loadTestFixture(t)
	ctx := context.Background()

	var first string
	for i := 0; i < 20; i++ {
		res, err := RunRegressionFixture(ctx, f, testEvaluatorHash, testMetricDefVersion, testPolicyHash, testRankingHash)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		h := res.ResultArtifactHash()
		if i == 0 {
			first = h
			continue
		}
		if h != first {
			t.Fatalf("determinism broken at run %d: %s != %s", i, h, first)
		}
	}
	t.Logf("20-run comparable result hash: %s", first)
}

func TestRunRegressionFixture_GoldenMetrics(t *testing.T) {
	f := loadTestFixture(t)
	res, err := RunRegressionFixture(context.Background(), f, testEvaluatorHash, testMetricDefVersion, testPolicyHash, testRankingHash)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.MetricsValid {
		t.Fatalf("metrics_valid should be true")
	}
	if res.SampleCount != 8 {
		t.Fatalf("sample_count = %d, want 8", res.SampleCount)
	}

	// Q1 exact match: recall=1.0, mrr=1.0.
	// Q3 zero relevant: recall=0, mrr=0.
	// Q4 relevant beyond top_k: recall=0, mrr=0.
	// Q5 empty retrieval: recall=0, mrr=0.
	// Q6 hybrid: recall=1.0, mrr=1.0.
	// Q7 duplicate: recall=1.0, mrr=1.0.
	// Q8 relevant at rank 10: recall=1.0, mrr=0.1.
	// Q2 multi-relevant: recall=1.0, mrr=0.5.
	// recall mean = (1+1+0+0+0+1+1+1)/8 = 5/8 = 0.625
	wantRecall := 5.0 / 8.0
	if math.Abs(res.Metrics.Recall-wantRecall) > 1e-12 {
		t.Errorf("recall = %v, want %v", res.Metrics.Recall, wantRecall)
	}
	// mrr mean = (1 + 0.5 + 0 + 0 + 0 + 1 + 1 + 0.1)/8 = 3.6/8 = 0.45
	wantMRR := (1 + 0.5 + 0 + 0 + 0 + 1 + 1 + 0.1) / 8.0
	if math.Abs(res.Metrics.MRR-wantMRR) > 1e-12 {
		t.Errorf("mrr = %v, want %v", res.Metrics.MRR, wantMRR)
	}
	t.Logf("metrics: recall=%.6f mrr=%.6f ndcg3=%.6f ndcg10=%.6f map=%.6f precision=%.6f",
		res.Metrics.Recall, res.Metrics.MRR, res.Metrics.NDCG3, res.Metrics.NDCG10, res.Metrics.MAP, res.Metrics.Precision)
}

func TestRankQuery_TieBreakerChunkIDLexicographicAsc(t *testing.T) {
	// Tied scores must be broken by chunk_id_lexicographic_asc on ChunkID strings,
	// not map iteration order. Multi-digit IDs prove it is LEXICOGRAPHIC on the
	// string form, not numeric: "10" < "2" lexicographically.
	q := RegressionQuery{
		QID: 1,
		VectorCandidates: []ScoredCandidate{
			{DocID: 5, Score: 0.5},
			{DocID: 1, Score: 0.5},
			{DocID: 3, Score: 0.5},
		},
	}
	cfg := &types.RetrievalConfig{RRFK: 60, RRFVectorWeight: 0.7, RRFKeywordWeight: 0.3}
	got := rankQuery(context.Background(), q, 10, cfg)
	want := []int{1, 3, 5}
	assertRank(t, got, want)

	// Multi-digit: lexicographic order is [10, 2] (not numeric [2, 10]).
	q2 := RegressionQuery{
		QID: 2,
		VectorCandidates: []ScoredCandidate{
			{DocID: 2, Score: 0.5},
			{DocID: 10, Score: 0.5},
		},
	}
	got2 := rankQuery(context.Background(), q2, 10, cfg)
	assertRank(t, got2, []int{10, 2})
}

func assertRank(t *testing.T, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestParseFixture_ClosedSchema(t *testing.T) {
	// Unknown field must fail (closed schema).
	raw := []byte(`{"schema_version":"retrieval-core/1","fixture_id":"x","top_k":10,"tie_breaker":"chunk_id_lexicographic_asc","rrf_k":60,"rrf_vector_weight":0.7,"rrf_keyword_weight":0.3,"queries":[],"bogus":1}`)
	if _, err := parseRegressionFixture(raw); err == nil {
		t.Fatalf("expected unknown-field error")
	}
	// Duplicate qid must fail.
	dup := []byte(`{"schema_version":"retrieval-core/1","fixture_id":"x","top_k":10,"tie_breaker":"chunk_id_lexicographic_asc","rrf_k":60,"rrf_vector_weight":0.7,"rrf_keyword_weight":0.3,"queries":[{"qid":1,"query":"a","relevant_ids":[1],"vector_candidates":[],"keyword_candidates":[]},{"qid":1,"query":"b","relevant_ids":[1],"vector_candidates":[],"keyword_candidates":[]}]}`)
	if _, err := parseRegressionFixture(dup); err == nil {
		t.Fatalf("expected duplicate-qid error")
	}
	// Unsupported tie_breaker must fail (P2 rename: old name is invalid).
	old := []byte(`{"schema_version":"retrieval-core/1","fixture_id":"x","top_k":10,"tie_breaker":"document_id_asc","rrf_k":60,"rrf_vector_weight":0.7,"rrf_keyword_weight":0.3,"queries":[]}`)
	if _, err := parseRegressionFixture(old); err == nil {
		t.Fatalf("expected unsupported-tie-breaker error")
	}
}

// makeBaseline builds a fully-populated, integrity-valid BaselineManifest from a
// canonical RegressionResult (so verifyBaselineIntegrity passes).
func makeBaseline(res *RegressionResult) *BaselineManifest {
	return &BaselineManifest{
		SchemaVersion:              baselineSchemaVersion,
		BaselineID:                 "B001",
		SourceCommit:               "9b4f792a",
		SourceRef:                  "refs/heads/main",
		FixtureID:                  res.FixtureID,
		TopK:                       res.TopK,
		ProtocolHash:               res.ProtocolHash,
		DatasetContentHash:         res.DatasetContentHash,
		FixtureArtifactHash:        res.FixtureArtifactHash,
		MeasurementContractVersion: res.MeasurementContractVersion,
		EvaluatorArtifactHash:      res.EvaluatorArtifactHash,
		MetricDefinitionVersion:    res.MetricDefinitionVersion,
		ComparisonPolicyHash:       res.ComparisonPolicyHash,
		RankingArtifactHash:        res.RankingArtifactHash,
		ResultArtifactHash:         res.ResultArtifactHash(),
		MetricsValid:               true,
		SampleCount:                res.SampleCount,
		Metrics:                    res.Metrics,
	}
}

func TestRunComparator_BlockAndPass(t *testing.T) {
	policyRaw, _ := os.ReadFile(filepath.Join("..", "..", "..", "tests", "evaluation", "policies", "quality_core_v1.json"))
	policy, err := parseComparisonPolicy(policyRaw)
	if err != nil {
		t.Fatalf("parse policy: %v", err)
	}
	policyHash := ComparisonPolicyHash(policy)

	f := loadTestFixture(t)
	baselineRes, _ := RunRegressionFixture(context.Background(), f, testEvaluatorHash, testMetricDefVersion, policyHash, testRankingHash)
	baseline := makeBaseline(baselineRes)

	if !verifyBaselineIntegrity(baseline) {
		t.Fatalf("baseline integrity should pass for a fresh baseline")
	}

	// Identical candidate -> PASS.
	pass := RunComparator(baseline, baselineRes, policy)
	if pass.Decision != DecisionPass {
		t.Fatalf("identical candidate decision = %s, want PASS", pass.Decision)
	}

	// Regressed candidate: zero every retrieval metric.
	regressed := *baselineRes
	regressed.Metrics.Recall = 0
	regressed.Metrics.MRR = 0
	regressed.Metrics.NDCG3 = 0
	regressed.Metrics.NDCG10 = 0
	block := RunComparator(baseline, &regressed, policy)
	if block.Decision != DecisionBlock {
		t.Fatalf("regressed candidate decision = %s, want BLOCK", block.Decision)
	}
	if len(block.FailedRules) != 4 {
		t.Fatalf("failed rules = %v, want 4", block.FailedRules)
	}
}

func TestRunComparator_PreflightMismatches(t *testing.T) {
	policyRaw, _ := os.ReadFile(filepath.Join("..", "..", "..", "tests", "evaluation", "policies", "quality_core_v1.json"))
	policy, _ := parseComparisonPolicy(policyRaw)
	policyHash := ComparisonPolicyHash(policy)

	f := loadTestFixture(t)
	res, _ := RunRegressionFixture(context.Background(), f, testEvaluatorHash, testMetricDefVersion, policyHash, testRankingHash)
	baseline := makeBaseline(res)

	// Fixture mismatch -> NOT_COMPARABLE_FIXTURE, never BLOCK.
	candidate := *res
	candidate.FixtureArtifactHash = "different"
	cmp := RunComparator(baseline, &candidate, policy)
	if cmp.Decision != DecisionNotComparable {
		t.Fatalf("fixture mismatch decision = %s, want NOT_COMPARABLE", cmp.Decision)
	}
	if !hasReason(cmp.Preflight.ReasonCodes, ReasonFixture) {
		t.Fatalf("reason codes %v missing %s", cmp.Preflight.ReasonCodes, ReasonFixture)
	}
}

func TestRunComparator_PreflightDatasetMismatch(t *testing.T) {
	// P1-1: dataset content identity MUST_EQUAL across baseline and candidate.
	policyRaw, _ := os.ReadFile(filepath.Join("..", "..", "..", "tests", "evaluation", "policies", "quality_core_v1.json"))
	policy, _ := parseComparisonPolicy(policyRaw)
	policyHash := ComparisonPolicyHash(policy)

	f := loadTestFixture(t)
	res, _ := RunRegressionFixture(context.Background(), f, testEvaluatorHash, testMetricDefVersion, policyHash, testRankingHash)
	baseline := makeBaseline(res)

	candidate := *res
	candidate.DatasetContentHash = "tampered-dataset-content"
	cmp := RunComparator(baseline, &candidate, policy)
	if cmp.Decision != DecisionNotComparable {
		t.Fatalf("dataset mismatch decision = %s, want NOT_COMPARABLE", cmp.Decision)
	}
	if !hasReason(cmp.Preflight.ReasonCodes, ReasonDataset) {
		t.Fatalf("reason codes %v missing %s", cmp.Preflight.ReasonCodes, ReasonDataset)
	}
}

func TestRunComparator_PreflightBaselineIntegrity(t *testing.T) {
	// P1-1: a baseline whose frozen result identity cannot be reproduced must
	// be NOT_COMPARABLE, never silently trusted.
	policyRaw, _ := os.ReadFile(filepath.Join("..", "..", "..", "tests", "evaluation", "policies", "quality_core_v1.json"))
	policy, _ := parseComparisonPolicy(policyRaw)
	policyHash := ComparisonPolicyHash(policy)

	f := loadTestFixture(t)
	res, _ := RunRegressionFixture(context.Background(), f, testEvaluatorHash, testMetricDefVersion, policyHash, testRankingHash)
	baseline := makeBaseline(res)
	baseline.ResultArtifactHash = "stale-or-forged-hash"

	cmp := RunComparator(baseline, res, policy)
	if cmp.Decision != DecisionNotComparable {
		t.Fatalf("tampered baseline decision = %s, want NOT_COMPARABLE", cmp.Decision)
	}
	if !hasReason(cmp.Preflight.ReasonCodes, ReasonBaselineIntegrity) {
		t.Fatalf("reason codes %v missing %s", cmp.Preflight.ReasonCodes, ReasonBaselineIntegrity)
	}

	// Also tampering a metric in the manifest must break integrity (reconstructed
	// hash no longer matches the frozen result hash).
	baseline2 := makeBaseline(res)
	baseline2.Metrics.Recall = 0.999
	if verifyBaselineIntegrity(baseline2) {
		t.Fatalf("metric-tampered baseline must fail integrity")
	}
}

func TestParseComparisonPolicy_RejectsUnknownAndAdvisoryMetric(t *testing.T) {
	// P0-2: an unknown metric id must be rejected, never silently coerced to 0.
	unknown := []byte(`{"schema_version":"regression_policy/1","policy_id":"p","numeric_epsilon":1e-12,"required_sample_count":8,"required_metric_coverage":1.0,"required_metrics":[{"metric":"retrieval.quantum","direction":"HIGHER_IS_BETTER","max_absolute_regression":0}]}`)
	if _, err := parseComparisonPolicy(unknown); err == nil {
		t.Fatalf("expected unknown-metric error")
	}

	// Advisory-only metrics (precision/map) must not be accepted as blocking rules.
	advisory := []byte(`{"schema_version":"regression_policy/1","policy_id":"p","numeric_epsilon":1e-12,"required_sample_count":8,"required_metric_coverage":1.0,"required_metrics":[{"metric":"retrieval.precision","direction":"HIGHER_IS_BETTER","max_absolute_regression":0}]}`)
	if _, err := parseComparisonPolicy(advisory); err == nil {
		t.Fatalf("expected advisory-metric rejection")
	}

	// Invalid numeric_epsilon must be rejected.
	badEps := []byte(`{"schema_version":"regression_policy/1","policy_id":"p","numeric_epsilon":-1,"required_sample_count":8,"required_metric_coverage":1.0,"required_metrics":[{"metric":"retrieval.recall","direction":"HIGHER_IS_BETTER","max_absolute_regression":0}]}`)
	if _, err := parseComparisonPolicy(badEps); err == nil {
		t.Fatalf("expected negative-epsilon error")
	}
}

func hasReason(codes []string, want string) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

func TestDecisionExitCodes(t *testing.T) {
	cases := map[RegressionDecision]int{
		DecisionPass:          0,
		DecisionBlock:         2,
		DecisionNotComparable: 3,
		DecisionError:         4,
	}
	for d, want := range cases {
		if d.ExitCode() != want {
			t.Fatalf("%s exit = %d, want %d", d, d.ExitCode(), want)
		}
	}
}

// Ensure the fixture JSON round-trips through canonicalization deterministically.
func TestFixtureArtifactHash_Stable(t *testing.T) {
	f := loadTestFixture(t)
	h1 := FixtureArtifactHash(f)
	h2 := FixtureArtifactHash(f)
	if h1 != h2 {
		t.Fatalf("fixture hash unstable: %s != %s", h1, h2)
	}
	// A re-marshaled copy must hash identically (order-insensitive canonical form).
	var copy RegressionFixture
	b, _ := json.Marshal(canonicalFixture(f))
	if err := json.Unmarshal(b, &copy); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if FixtureArtifactHash(&copy) != h1 {
		t.Fatalf("canonical roundtrip hash drift")
	}
}

// ---- Round 2: SUT boundary, metric presence, coverage==1 ----

// The ranking seam is the ONLY candidate-owned file in the gate. Its boundary
// must be closed: no network/provider/db/subprocess/file-write/init content.
func TestValidateRankingSeamSource(t *testing.T) {
	clean := []byte("package service\n\nfunc sortByScoreDesc(items []RankedChunk) {\n\tslices.SortFunc(items, func(a, b RankedChunk) int {\n\t\tif a.Score != b.Score {\n\t\t\treturn int(b.Score - a.Score)\n\t\t}\n\t\tif a.ChunkID < b.ChunkID {\n\t\t\treturn -1\n\t\t}\n\t\treturn 1\n\t})\n}\n")
	if err := ValidateRankingSeamSource(clean); err != nil {
		t.Fatalf("clean seam rejected: %v", err)
	}

	forbidden := map[string]string{
		"net/http import": `package service
import "net/http"
func fetch() { http.Get("https://example.com") }`,
		"net.Dial": `package service
import "net"
func dial() { net.Dial("tcp", "1.2.3.4:80") }`,
		"provider client": `package service
import "github.com/openai/openai-go"
func call() {}`,
		"database/sql": `package service
import "database/sql"
func open() { sql.Open("sqlite", "x") }`,
		"os/exec": `package service
import "os/exec"
func run() { exec.Command("sh", "-c", "id") }`,
		"file write": `package service
import "os"
func write() { os.WriteFile("/tmp/x", nil, 0644) }`,
		"func init": `package service
func init() { os.Setenv("X", "1") }`,
	}
	for name, src := range forbidden {
		if err := ValidateRankingSeamSource([]byte(src)); err == nil {
			t.Fatalf("%s: expected boundary rejection", name)
		}
	}
}

func TestValidateRankingSeamSource_RejectsAliasAndPackageInitializationEscapes(t *testing.T) {
	tests := map[string]string{
		"network import alias": `package service
import n "net"
func dial() { _, _ = n.Dial("tcp", "127.0.0.1:9") }`,
		"file import alias": `package service
import f "os"
func write() { _ = f.WriteFile("/tmp/x", nil, 0o600) }`,
		"package initializer": `package service
var sideEffect = func() int { panic("ran during package initialization"); return 0 }()`,
		"dot import": `package service
import . "os"
func write() { _ = WriteFile("/tmp/x", nil, 0o600) }`,
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRankingSeamSource([]byte(src)); err == nil {
				t.Fatalf("expected boundary rejection")
			}
		})
	}
}

// RankingSeamIdentity must validate BEFORE hashing: a violating seam can never
// be recorded as a comparable ranking identity.
func TestRankingSeamIdentity_ValidatesBeforeHashing(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "seam.go")
	if err := os.WriteFile(bad, []byte("package service\nimport \"net/http\"\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := RankingSeamIdentity(bad); err == nil {
		t.Fatalf("expected validation error for network seam")
	}

	good := filepath.Join(dir, "clean.go")
	if err := os.WriteFile(good, []byte("package service\nfunc f() {}\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h, err := RankingSeamIdentity(good)
	if err != nil {
		t.Fatalf("clean seam: %v", err)
	}
	if len(h) != 64 {
		t.Fatalf("hash length %d", len(h))
	}
	if h != mustRankingHash(t, good) {
		t.Fatalf("identity hash mismatch")
	}

	// Identity represents SUT content at its frozen logical code location, not
	// the caller's checkout/temp path. Local and CI candidate paths must agree.
	otherDir := t.TempDir()
	other := filepath.Join(otherDir, "candidate-ranking.go")
	if err := os.WriteFile(other, []byte("package service\nfunc f() {}\n"), 0644); err != nil {
		t.Fatalf("write second path: %v", err)
	}
	otherHash, err := RankingSeamIdentity(other)
	if err != nil {
		t.Fatalf("second path identity: %v", err)
	}
	if otherHash != h {
		t.Fatalf("same SUT content changed identity across paths: %s != %s", h, otherHash)
	}
}

func mustRankingHash(t *testing.T, path string) string {
	t.Helper()
	h, err := RankingArtifactHash(path)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return h
}

// RegressionMetrics presence: a decoded JSON that omits a blocking metric must
// report it missing — distinct from a real observed 0.
func TestRegressionMetrics_PresenceTracking(t *testing.T) {
	// In-process metrics (runner-built) report everything present.
	var built RegressionMetrics
	built.Recall = 0
	if !built.HasMetric("recall") {
		t.Fatalf("in-process metrics must report all fields present")
	}

	// Decoded JSON: recall absent → HasMetric("recall") false, even though the
	// typed value reads 0.
	decoded := RegressionMetrics{}
	if err := json.Unmarshal([]byte(`{"precision":0.2,"mrr":0.1,"map":0.1,"ndcg3":0.1,"ndcg10":0.1}`), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Recall != 0 {
		t.Fatalf("recall typed value = %v", decoded.Recall)
	}
	if decoded.HasMetric("recall") {
		t.Fatalf("absent recall must report missing")
	}
	if !decoded.HasMetric("mrr") {
		t.Fatalf("present mrr must report present")
	}

	// Round-trip: presence map must not leak into marshal output.
	out, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(out, []byte("present")) {
		t.Fatalf("presence map leaked into marshal: %s", out)
	}
}

func TestRegressionMetrics_NullBlockingMetricIsMissing(t *testing.T) {
	var metrics RegressionMetrics
	if err := json.Unmarshal([]byte(`{"precision":0.2,"recall":null,"mrr":0.4,"map":0.3,"ndcg3":0.4,"ndcg10":0.5}`), &metrics); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if metrics.HasMetric("recall") {
		t.Fatalf("null recall must not be treated as an observed zero")
	}
}

func mustPolicy(t *testing.T) *ComparisonPolicy {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "evaluation", "policies", "quality_core_v1.json"))
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	p, err := parseComparisonPolicy(raw)
	if err != nil {
		t.Fatalf("parse policy: %v", err)
	}
	return p
}

func mustRunnerResult(t *testing.T, policy *ComparisonPolicy) *RegressionResult {
	t.Helper()
	f := loadTestFixture(t)
	res, err := RunRegressionFixture(context.Background(), f, testEvaluatorHash, testMetricDefVersion, ComparisonPolicyHash(policy), testRankingHash)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

// A blocking metric silently dropped from the candidate result JSON must yield
// NOT_COMPARABLE with INCOMPLETE_QUALITY_METRICS — never PASS, never a silent 0.
func TestRunComparator_PreflightMetricMissing(t *testing.T) {
	policy := mustPolicy(t)
	res := mustRunnerResult(t, policy)
	baseline := makeBaseline(res)

	// Strip recall from the candidate result JSON.
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	metrics := asMap["metrics"].(map[string]any)
	delete(metrics, "recall")
	stripped, err := json.Marshal(asMap)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	candidate, err := ParseRegressionResult(stripped)
	if err != nil {
		t.Fatalf("parse candidate: %v", err)
	}

	out := runComparator(baseline, candidate, policy)
	if out.Decision != DecisionNotComparable {
		t.Fatalf("decision = %s, want NOT_COMPARABLE", out.Decision)
	}
	if !hasReason(out.Preflight.ReasonCodes, ReasonMetricMissing) {
		t.Fatalf("reason codes = %v, want %s", out.Preflight.ReasonCodes, ReasonMetricMissing)
	}

	// Sanity: the intact result still compares PASS.
	if intact := runComparator(baseline, res, policy); intact.Decision != DecisionPass {
		t.Fatalf("intact decision = %s, want PASS", intact.Decision)
	}
}

func TestRunComparator_PreflightNullAndOutOfRangeMetrics(t *testing.T) {
	policy := mustPolicy(t)
	res := mustRunnerResult(t, policy)
	baseline := makeBaseline(res)

	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	metrics := asMap["metrics"].(map[string]any)
	metrics["recall"] = nil
	nullRaw, _ := json.Marshal(asMap)
	nullCandidate, err := ParseRegressionResult(nullRaw)
	if err != nil {
		t.Fatalf("parse null candidate: %v", err)
	}
	nullResult := runComparator(baseline, nullCandidate, policy)
	if nullResult.Decision != DecisionNotComparable || !hasReason(nullResult.Preflight.ReasonCodes, ReasonMetricMissing) {
		t.Fatalf("null metric result = %s %v, want NOT_COMPARABLE/%s", nullResult.Decision, nullResult.Preflight.ReasonCodes, ReasonMetricMissing)
	}

	metrics["recall"] = 1.1
	outOfRangeRaw, _ := json.Marshal(asMap)
	outOfRangeCandidate, err := ParseRegressionResult(outOfRangeRaw)
	if err != nil {
		t.Fatalf("parse out-of-range candidate: %v", err)
	}
	outOfRangeResult := runComparator(baseline, outOfRangeCandidate, policy)
	if outOfRangeResult.Decision != DecisionNotComparable || !hasReason(outOfRangeResult.Preflight.ReasonCodes, ReasonInvalidMetrics) {
		t.Fatalf("out-of-range result = %s %v, want NOT_COMPARABLE/%s", outOfRangeResult.Decision, outOfRangeResult.Preflight.ReasonCodes, ReasonInvalidMetrics)
	}
}

// required_metric_coverage other than exactly 1 is rejected at parse time.
func TestParseComparisonPolicy_RejectsPartialCoverage(t *testing.T) {
	partial := []byte(`{"schema_version":"regression_policy/1","policy_id":"p","numeric_epsilon":1e-12,"required_sample_count":8,"required_metric_coverage":0.75,"required_metrics":[{"metric":"retrieval.recall","direction":"HIGHER_IS_BETTER","max_absolute_regression":0}]}`)
	if _, err := parseComparisonPolicy(partial); err == nil {
		t.Fatalf("expected partial-coverage rejection")
	}
	full := []byte(`{"schema_version":"regression_policy/1","policy_id":"p","numeric_epsilon":1e-12,"required_sample_count":8,"required_metric_coverage":1.0,"required_metrics":[{"metric":"retrieval.recall","direction":"HIGHER_IS_BETTER","max_absolute_regression":0}]}`)
	if _, err := parseComparisonPolicy(full); err != nil {
		t.Fatalf("coverage==1 must be accepted: %v", err)
	}
}
