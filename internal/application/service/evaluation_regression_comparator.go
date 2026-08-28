package service

// Task007 (G5) — Comparison Preflight, Policy and Comparator.
//
// The preflight is fail-closed and reason-coded: any unprovable compatibility
// defaults to NOT_COMPARABLE, never PASS. The comparator emits a canonical
// machine-readable decision with the stable exit mapping 0/2/3/4.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// ---- Stable preflight reason codes ----

const (
	ReasonComparable          = "COMPARABLE"
	ReasonProtocol            = "NOT_COMPARABLE_PROTOCOL"
	ReasonDataset             = "NOT_COMPARABLE_DATASET"
	ReasonFixture             = "NOT_COMPARABLE_FIXTURE"
	ReasonComparisonPolicy    = "NOT_COMPARABLE_COMPARISON_POLICY"
	ReasonMeasurementContract = "NOT_COMPARABLE_MEASUREMENT_CONTRACT"
	ReasonEvaluator           = "NOT_COMPARABLE_EVALUATOR"
	ReasonMetricDefinition    = "NOT_COMPARABLE_METRIC_DEFINITION"
	ReasonRankingContract     = "NOT_COMPARABLE_RANKING_CONTRACT"
	ReasonBaselinePolicy      = "NOT_COMPARABLE_BASELINE_POLICY"
	ReasonBaselineIntegrity   = "NOT_COMPARABLE_BASELINE_INTEGRITY"
	ReasonInvalidMetrics      = "INVALID_QUALITY_METRICS"
	ReasonMetricMissing       = "INCOMPLETE_QUALITY_METRICS"
	ReasonIncompleteCoverage  = "INCOMPLETE_QUALITY_COVERAGE"
	ReasonIncompleteUsage     = "INCOMPLETE_USAGE_MEASUREMENT"
	ReasonError               = "ERROR"
)

// ---- Comparison Policy ----

const comparisonPolicySchemaVersion = "regression_policy/1"

// blockingMetricAllowlist is the closed set of metric IDs that may be declared
// as a blocking required metric. precision and map are deliberately excluded:
// per the metric definition audit they are advisory-only (precision is computed
// over all retrieved ids rather than precision@k; map is normalized by hitCount
// rather than total relevant). Any metric outside this set is rejected at parse
// time — it can never silently coerce to zero and pass. See
// metric_definition_audit.md.
var blockingMetricAllowlist = map[string]struct{}{
	"retrieval.recall":  {},
	"retrieval.mrr":     {},
	"retrieval.ndcg@3":  {},
	"retrieval.ndcg@10": {},
}

// RequiredMetricRule is a single blocking quality rule.
type RequiredMetricRule struct {
	Metric                string  `json:"metric"`
	Direction             string  `json:"direction"`
	MaxAbsoluteRegression float64 `json:"max_absolute_regression"`
}

// ComparisonPolicy is the versioned, protected comparison policy.
type ComparisonPolicy struct {
	SchemaVersion          string               `json:"schema_version"`
	PolicyID               string               `json:"policy_id"`
	NumericEpsilon         float64              `json:"numeric_epsilon"`
	RequiredSampleCount    int                  `json:"required_sample_count"`
	RequiredMetricCoverage float64              `json:"required_metric_coverage"`
	RequiredMetrics        []RequiredMetricRule `json:"required_metrics"`
}

func parseComparisonPolicy(raw []byte) (*ComparisonPolicy, error) {
	var p ComparisonPolicy
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("policy decode: %w", err)
	}
	if p.SchemaVersion != comparisonPolicySchemaVersion {
		return nil, fmt.Errorf("policy schema_version %q != %q", p.SchemaVersion, comparisonPolicySchemaVersion)
	}
	if p.PolicyID == "" {
		return nil, fmt.Errorf("policy_id is empty")
	}
	if len(p.RequiredMetrics) == 0 {
		return nil, fmt.Errorf("policy has no required metrics")
	}
	if math.IsNaN(p.NumericEpsilon) || math.IsInf(p.NumericEpsilon, 0) || p.NumericEpsilon < 0 {
		return nil, fmt.Errorf("numeric_epsilon must be finite and >= 0, got %v", p.NumericEpsilon)
	}
	if p.RequiredSampleCount <= 0 {
		return nil, fmt.Errorf("required_sample_count must be positive, got %d", p.RequiredSampleCount)
	}
	if math.IsNaN(p.RequiredMetricCoverage) || math.IsInf(p.RequiredMetricCoverage, 0) ||
		p.RequiredMetricCoverage < 0 || p.RequiredMetricCoverage > 1 {
		return nil, fmt.Errorf("required_metric_coverage must be in [0,1], got %v", p.RequiredMetricCoverage)
	}
	if p.RequiredMetricCoverage != 1 {
		return nil, fmt.Errorf("required_metric_coverage must be exactly 1 (partial coverage would let missing metrics be conflated with zero), got %v", p.RequiredMetricCoverage)
	}
	seen := map[string]struct{}{}
	for _, m := range p.RequiredMetrics {
		if _, dup := seen[m.Metric]; dup {
			return nil, fmt.Errorf("duplicate required metric %q", m.Metric)
		}
		seen[m.Metric] = struct{}{}
		if _, ok := blockingMetricAllowlist[m.Metric]; !ok {
			return nil, fmt.Errorf("metric %q is not a blocking-eligible metric (allowlist: retrieval.recall, retrieval.mrr, retrieval.ndcg@3, retrieval.ndcg@10)", m.Metric)
		}
		if m.Direction != "HIGHER_IS_BETTER" {
			return nil, fmt.Errorf("metric %q direction %q unsupported", m.Metric, m.Direction)
		}
		if math.IsNaN(m.MaxAbsoluteRegression) || math.IsInf(m.MaxAbsoluteRegression, 0) || m.MaxAbsoluteRegression < 0 {
			return nil, fmt.Errorf("metric %q has invalid max_absolute_regression %v", m.Metric, m.MaxAbsoluteRegression)
		}
	}
	return &p, nil
}

// ComparisonPolicyHash is the sha256 of the canonical policy bytes.
func ComparisonPolicyHash(p *ComparisonPolicy) string {
	return sha256Hex(mustJSON(canonicalPolicy(p)))
}

func canonicalPolicy(p *ComparisonPolicy) *ComparisonPolicy {
	out := *p
	out.RequiredMetrics = append([]RequiredMetricRule(nil), p.RequiredMetrics...)
	sort.SliceStable(out.RequiredMetrics, func(i, j int) bool {
		return out.RequiredMetrics[i].Metric < out.RequiredMetrics[j].Metric
	})
	return &out
}

// ---- Baseline Manifest ----

const baselineSchemaVersion = "regression_baseline/1"

// BaselineManifest is the immutable known-good baseline identity. It carries
// every field needed to reconstruct the canonical RegressionResult byte-for-byte
// and re-verify the frozen ResultArtifactHash (baseline integrity).
type BaselineManifest struct {
	SchemaVersion              string            `json:"schema_version"`
	BaselineID                 string            `json:"baseline_id"`
	SourceCommit               string            `json:"source_commit"`
	SourceRef                  string            `json:"source_ref"`
	FixtureID                  string            `json:"fixture_id"`
	TopK                       int               `json:"top_k"`
	ProtocolHash               string            `json:"protocol_hash"`
	DatasetContentHash         string            `json:"dataset_content_hash"`
	FixtureArtifactHash        string            `json:"fixture_artifact_hash"`
	MeasurementContractVersion string            `json:"measurement_contract_version"`
	EvaluatorArtifactHash      string            `json:"evaluator_artifact_hash"`
	MetricDefinitionVersion    string            `json:"metric_definition_version"`
	ComparisonPolicyHash       string            `json:"comparison_policy_hash"`
	RankingArtifactHash        string            `json:"ranking_artifact_hash"`
	ResultArtifactHash         string            `json:"result_artifact_hash"`
	MetricsValid               bool              `json:"metrics_valid"`
	SampleCount                int               `json:"sample_count"`
	Metrics                    RegressionMetrics `json:"metrics"`
}

func parseBaselineManifest(raw []byte) (*BaselineManifest, error) {
	var b BaselineManifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("baseline decode: %w", err)
	}
	if b.SchemaVersion != baselineSchemaVersion {
		return nil, fmt.Errorf("baseline schema_version %q != %q", b.SchemaVersion, baselineSchemaVersion)
	}
	if b.BaselineID == "" {
		return nil, fmt.Errorf("baseline_id is empty")
	}
	if b.FixtureID == "" {
		return nil, fmt.Errorf("fixture_id is empty")
	}
	if b.TopK <= 0 {
		return nil, fmt.Errorf("top_k must be positive")
	}
	if b.DatasetContentHash == "" {
		return nil, fmt.Errorf("dataset_content_hash is empty")
	}
	if b.EvaluatorArtifactHash == "" {
		return nil, fmt.Errorf("evaluator_artifact_hash is empty")
	}
	if b.RankingArtifactHash == "" {
		return nil, fmt.Errorf("ranking_artifact_hash is empty")
	}
	if b.ResultArtifactHash == "" {
		return nil, fmt.Errorf("result_artifact_hash is empty")
	}
	return &b, nil
}

// verifyBaselineIntegrity reconstructs the canonical RegressionResult from the
// manifest fields and checks that its ResultArtifactHash matches the frozen
// value. A manifest whose stored result identity cannot be reproduced is
// unusable as a baseline (NOT_COMPARABLE_BASELINE_INTEGRITY).
func verifyBaselineIntegrity(b *BaselineManifest) bool {
	r := RegressionResult{
		SchemaVersion:              regressionResultSchemaVersion,
		FixtureID:                  b.FixtureID,
		FixtureArtifactHash:        b.FixtureArtifactHash,
		DatasetContentHash:         b.DatasetContentHash,
		TopK:                       b.TopK,
		SampleCount:                b.SampleCount,
		Metrics:                    b.Metrics,
		MetricsValid:               b.MetricsValid,
		ProtocolHash:               b.ProtocolHash,
		MeasurementContractVersion: b.MeasurementContractVersion,
		EvaluatorArtifactHash:      b.EvaluatorArtifactHash,
		MetricDefinitionVersion:    b.MetricDefinitionVersion,
		ComparisonPolicyHash:       b.ComparisonPolicyHash,
		RankingArtifactHash:        b.RankingArtifactHash,
	}
	return r.ResultArtifactHash() == b.ResultArtifactHash
}

// ---- Preflight ----

// PreflightResult is the reason-coded comparison-preflight decision.
type PreflightResult struct {
	Decision    RegressionDecision `json:"decision"`
	ReasonCodes []string           `json:"reason_codes"`
}

// runPreflight verifies candidate vs baseline vs policy compatibility,
// fail-closed. Unknown or unprovable compatibility is NOT_COMPARABLE.
func runPreflight(baseline *BaselineManifest, candidate *RegressionResult, policy *ComparisonPolicy) PreflightResult {
	codes := []string{}

	// Baseline integrity first: a baseline whose frozen result identity cannot
	// be reproduced (or whose metrics are invalid) is unusable as a comparator
	// reference, regardless of the candidate.
	if !baseline.MetricsValid || !baseline.Metrics.valuesValid() || !verifyBaselineIntegrity(baseline) {
		codes = append(codes, ReasonBaselineIntegrity)
	}
	if baseline.ProtocolHash != candidate.ProtocolHash {
		codes = append(codes, ReasonProtocol)
	}
	if baseline.DatasetContentHash != candidate.DatasetContentHash {
		codes = append(codes, ReasonDataset)
	}
	if baseline.FixtureArtifactHash != candidate.FixtureArtifactHash {
		codes = append(codes, ReasonFixture)
	}
	if baseline.ComparisonPolicyHash != candidate.ComparisonPolicyHash {
		codes = append(codes, ReasonComparisonPolicy)
	}
	if baseline.MeasurementContractVersion != candidate.MeasurementContractVersion {
		codes = append(codes, ReasonMeasurementContract)
	}
	if baseline.EvaluatorArtifactHash != candidate.EvaluatorArtifactHash {
		codes = append(codes, ReasonEvaluator)
	}
	if baseline.MetricDefinitionVersion != candidate.MetricDefinitionVersion {
		codes = append(codes, ReasonMetricDefinition)
	}
	if !candidate.MetricsValid || !candidate.Metrics.valuesValid() {
		codes = append(codes, ReasonInvalidMetrics)
	}
	if candidate.SampleCount != policy.RequiredSampleCount {
		codes = append(codes, ReasonIncompleteCoverage)
	}
	// Presence check: every blocking metric must be PRESENT in both baseline and
	// candidate JSON, not merely zero-valued. A silently dropped blocking metric
	// field is indistinguishable from a real observed 0 without this check, and
	// it would defeat the gate's coverage guarantee.
	for _, rule := range policy.RequiredMetrics {
		field, ok := metricFieldName(rule.Metric)
		if !ok || !baseline.Metrics.HasMetric(field) || !candidate.Metrics.HasMetric(field) {
			codes = append(codes, ReasonMetricMissing)
			break
		}
	}

	if len(codes) > 0 {
		return PreflightResult{Decision: DecisionNotComparable, ReasonCodes: uniqueSorted(codes)}
	}
	return PreflightResult{Decision: DecisionPass, ReasonCodes: []string{ReasonComparable}}
}

func uniqueSorted(codes []string) []string {
	sort.Strings(codes)
	out := codes[:0]
	for i, c := range codes {
		if i == 0 || c != codes[i-1] {
			out = append(out, c)
		}
	}
	return out
}

// ---- Comparator ----

// MetricComparison is the per-metric comparison row.
type MetricComparison struct {
	Metric    string             `json:"metric"`
	Baseline  float64            `json:"baseline"`
	Candidate float64            `json:"candidate"`
	Delta     float64            `json:"delta"`
	Direction string             `json:"direction"`
	Threshold float64            `json:"threshold"`
	Epsilon   float64            `json:"numeric_epsilon"`
	Decision  RegressionDecision `json:"decision"`
}

// ComparisonResult is the canonical machine-readable decision artifact.
type ComparisonResult struct {
	SchemaVersion string             `json:"schema_version"`
	Decision      RegressionDecision `json:"decision"`
	BaselineID    string             `json:"baseline_id"`
	PolicyID      string             `json:"policy_id"`
	Preflight     PreflightResult    `json:"preflight"`
	Metrics       []MetricComparison `json:"metrics"`
	FailedRules   []string           `json:"failed_rules"`
}

const comparisonSchemaVersion = "regression_comparison/1"

// metricFieldName maps a policy metric id to the RegressionMetrics JSON field
// name used for presence tracking (HasMetric is keyed by JSON field names).
func metricFieldName(id string) (string, bool) {
	switch id {
	case "retrieval.precision":
		return "precision", true
	case "retrieval.recall":
		return "recall", true
	case "retrieval.mrr":
		return "mrr", true
	case "retrieval.map":
		return "map", true
	case "retrieval.ndcg@3":
		return "ndcg3", true
	case "retrieval.ndcg@10":
		return "ndcg10", true
	default:
		return "", false
	}
}

// metricValue returns the metric value for a metric id plus a found flag.
// Unknown ids report found=false and MUST NOT be coerced to zero: the policy
// parser already rejects unknown ids against the blocking allowlist, so a
// missing metric here is a programming error and is handled fail-closed by the
// comparator.
func metricValue(m RegressionMetrics, id string) (float64, bool) {
	switch id {
	case "retrieval.precision":
		return m.Precision, true
	case "retrieval.recall":
		return m.Recall, true
	case "retrieval.mrr":
		return m.MRR, true
	case "retrieval.map":
		return m.MAP, true
	case "retrieval.ndcg@3":
		return m.NDCG3, true
	case "retrieval.ndcg@10":
		return m.NDCG10, true
	default:
		return 0, false
	}
}

// runComparator performs preflight then the blocking comparison. If the
// preflight is not comparable, no metric is compared and the decision is
// NOT_COMPARABLE (never BLOCK for a non-comparable input).
func runComparator(baseline *BaselineManifest, candidate *RegressionResult, policy *ComparisonPolicy) ComparisonResult {
	res := ComparisonResult{
		SchemaVersion: comparisonSchemaVersion,
		BaselineID:    baseline.BaselineID,
		PolicyID:      policy.PolicyID,
		Preflight:     runPreflight(baseline, candidate, policy),
	}
	if res.Preflight.Decision != DecisionPass {
		res.Decision = res.Preflight.Decision
		return res
	}

	rows := make([]MetricComparison, 0, len(policy.RequiredMetrics))
	failed := []string{}
	anyBlock := false
	for _, rule := range policy.RequiredMetrics {
		b, bOK := metricValue(baseline.Metrics, rule.Metric)
		c, cOK := metricValue(candidate.Metrics, rule.Metric)
		if !bOK || !cOK {
			// Defensive: parseComparisonPolicy already allowlists every required
			// metric, so this branch is unreachable. Fail closed as ERROR rather
			// than fabricate a zero delta.
			rows = append(rows, MetricComparison{
				Metric:    rule.Metric,
				Baseline:  b,
				Candidate: c,
				Decision:  DecisionError,
			})
			res.Metrics = rows
			res.FailedRules = failed
			res.Decision = DecisionError
			return res
		}
		delta := c - b
		row := MetricComparison{
			Metric:    rule.Metric,
			Baseline:  b,
			Candidate: c,
			Delta:     delta,
			Direction: rule.Direction,
			Threshold: rule.MaxAbsoluteRegression,
			Epsilon:   policy.NumericEpsilon,
			Decision:  DecisionPass,
		}
		// HIGHER_IS_BETTER: BLOCK when delta < -(max_abs + epsilon).
		if delta < -(rule.MaxAbsoluteRegression + policy.NumericEpsilon) {
			row.Decision = DecisionBlock
			failed = append(failed, rule.Metric)
			anyBlock = true
		}
		rows = append(rows, row)
	}
	res.Metrics = rows
	res.FailedRules = failed
	if anyBlock {
		res.Decision = DecisionBlock
	} else {
		res.Decision = DecisionPass
	}
	return res
}

// ---- Exported machine-interface wrappers ----

// ParseComparisonPolicy is the exported policy parser for the CLI/verifier.
func ParseComparisonPolicy(raw []byte) (*ComparisonPolicy, error) {
	return parseComparisonPolicy(raw)
}

// ParseBaselineManifest is the exported baseline parser for the CLI/verifier.
func ParseBaselineManifest(raw []byte) (*BaselineManifest, error) {
	return parseBaselineManifest(raw)
}

// RunComparator is the exported comparator entry point for the CLI/verifier.
func RunComparator(baseline *BaselineManifest, candidate *RegressionResult, policy *ComparisonPolicy) ComparisonResult {
	return runComparator(baseline, candidate, policy)
}

// VerifyBaselineIntegrity is the exported baseline-integrity check for the
// CLI/verifier: it reproduces the canonical result from the manifest and checks
// the frozen ResultArtifactHash.
func VerifyBaselineIntegrity(b *BaselineManifest) bool {
	return verifyBaselineIntegrity(b)
}
