package main

// Task013 deterministic aggregation (plan §8/§9 Step 8). Offline, no network,
// no database. Anyone can recompute the same conclusion from the sanitized
// rows alone. Paired bootstrap uses the frozen seed 20260905 / 10000
// resamples (plan §6.1); changing them changes the artifact identity (N14).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	bootstrapSeed      = int64(20260905)
	bootstrapResamples = 10000
	minEffectAbsolute  = 0.10
	minValidPairs      = 10
)

// sanitizedRow is one authoritative allowlisted measurement row (plan §7).
type sanitizedRow struct {
	ExperimentID          string  `json:"experiment_id"`
	ProtocolHash          string  `json:"protocol_hash"`
	SampleID              string  `json:"sample_id"`
	Arm                   string  `json:"arm"`
	Ordinal               int     `json:"ordinal"`
	Warmup                bool    `json:"warmup"`
	LogicalCallID         string  `json:"logical_call_id"`
	TraceIDHash           string  `json:"trace_id_hash"`
	Provider              string  `json:"provider"`
	ModelExact            string  `json:"model_exact"`
	ReportedModelRevision string  `json:"reported_model_revision"`
	InputTokens           int     `json:"input_tokens"`
	OutputTokens          int     `json:"output_tokens"`
	CacheReadTokens       int     `json:"cache_read_tokens"`
	CacheWriteTokens      int     `json:"cache_write_tokens"`
	CacheMissTokens       int     `json:"cache_miss_tokens"`
	CacheReportedInput    int     `json:"cache_reported_input_tokens"`
	CacheStatus           string  `json:"cache_status"`
	UsageFinality         string  `json:"usage_finality"`
	AttemptCount          *int    `json:"attempt_count"`
	AttemptObservability  string  `json:"attempt_observability"`
	RequestElapsedMS      int     `json:"request_elapsed_ms"`
	ProviderLatencyMS     *int    `json:"provider_latency_ms_nullable"`
	PricingRuleID         string  `json:"pricing_rule_id"`
	EstimatedCostNanos    *int64  `json:"estimated_cost_nanos_nullable"`
	Currency              string  `json:"currency_nullable"`
	SchemaFormatPass      bool    `json:"schema_format_pass"`
	SummaryLinePass       bool    `json:"summary_line_pass"`
	ForbiddenHandles      int     `json:"forbidden_internal_handle_count"`
	RequiredFactRecall    float64 `json:"required_fact_recall"`
	UnsupportedFactCount  int     `json:"unsupported_fact_count"`
	ValidLinkPass         bool    `json:"valid_link_pass"`
	SourceGroundingPass   bool    `json:"source_grounding_pass"`
	ErrorType             string  `json:"error_type"`
	IncludedInPrimary     bool    `json:"included_in_primary"`
	ExclusionReason       string  `json:"exclusion_reason"`
}

func (r sanitizedRow) quality() Quality {
	return Quality{
		SchemaFormatPass:         r.SchemaFormatPass,
		SummaryLinePass:          r.SummaryLinePass,
		ForbiddenInternalHandles: r.ForbiddenHandles,
		RequiredFactRecall:       r.RequiredFactRecall,
		UnsupportedFactCount:     r.UnsupportedFactCount,
		ValidLinkPass:            r.ValidLinkPass,
		SourceGroundingPass:      r.SourceGroundingPass,
	}
}

// conservationViolation returns a non-empty string when the provider-reported
// counters break the plan §3.3 contract. No clamping, no patching.
func conservationViolation(r sanitizedRow) string {
	if r.CacheStatus == "unsupported" || r.CacheStatus == "unreported" {
		if r.CacheReadTokens != 0 || r.CacheReportedInput != 0 {
			return fmt.Sprintf("non-zero cache counters under status %q", r.CacheStatus)
		}
		return ""
	}
	if r.CacheReportedInput < 0 || r.CacheReadTokens < 0 || r.CacheMissTokens < 0 {
		return "negative cache counter"
	}
	if r.CacheReadTokens > r.CacheReportedInput {
		return "cache_read > cache_reported_input"
	}
	if r.CacheReportedInput > r.InputTokens {
		return "cache_reported_input > input_tokens"
	}
	if r.CacheReadTokens+r.CacheMissTokens != r.CacheReportedInput {
		return fmt.Sprintf("cache_read(%d)+cache_miss(%d) != cache_reported_input(%d)", r.CacheReadTokens, r.CacheMissTokens, r.CacheReportedInput)
	}
	return ""
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// AggregateResult is the machine-readable verdict (plan §8).
type AggregateResult struct {
	ControlReportingCoverage   float64           `json:"control_reporting_coverage"`
	TreatmentReportingCoverage float64           `json:"treatment_reporting_coverage"`
	ControlCachedTokenRatio    *float64          `json:"control_cached_token_ratio"`
	TreatmentCachedTokenRatio  *float64          `json:"treatment_cached_token_ratio"`
	AbsoluteDelta              *float64          `json:"absolute_delta"`
	BootstrapMeanDelta         float64           `json:"bootstrap_mean_delta"`
	BootstrapCI95Lower         float64           `json:"bootstrap_ci95_lower"`
	BootstrapCI95Upper         float64           `json:"bootstrap_ci95_upper"`
	ValidPairs                 int               `json:"valid_pairs"`
	Conservation               string            `json:"conservation"`
	QualityGate                string            `json:"quality_gate"`
	CostStatus                 string            `json:"cost_status"`
	ControlP50MS               int               `json:"control_p50_ms"`
	TreatmentP50MS             int               `json:"treatment_p50_ms"`
	ControlP95MS               int               `json:"control_p95_ms"`
	TreatmentP95MS             int               `json:"treatment_p95_ms"`
	PrimaryEffect              string            `json:"primary_effect"`
	ReportingCompletenessPass  bool              `json:"reporting_completeness_pass"`
	MeasurementAnomalies       []string          `json:"measurement_anomalies"`
	ExcludedPairs              map[string]string `json:"excluded_pairs"`
}

func percentile(sorted []int, p float64) int {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Aggregate consumes sanitized rows and returns the deterministic verdict.
func Aggregate(rows []sanitizedRow) (*AggregateResult, error) {
	res := &AggregateResult{Conservation: "PASS", QualityGate: "PASS", ExcludedPairs: map[string]string{}}

	bySample := map[string]map[string]sanitizedRow{}
	for _, r := range rows {
		if r.Warmup {
			continue
		}
		if !r.IncludedInPrimary {
			if r.ExclusionReason != "" {
				res.ExcludedPairs[r.SampleID] = r.ExclusionReason
			}
			continue
		}
		if r.UsageFinality != "REPORTED" {
			res.MeasurementAnomalies = append(res.MeasurementAnomalies, fmt.Sprintf("%s/%s: usage finality %q", r.SampleID, r.Arm, r.UsageFinality))
			continue
		}
		if v := conservationViolation(r); v != "" {
			res.MeasurementAnomalies = append(res.MeasurementAnomalies, fmt.Sprintf("%s/%s: %s", r.SampleID, r.Arm, v))
			res.Conservation = "FAIL"
			continue
		}
		if r.Provider == "" || r.ModelExact == "" || r.ProtocolHash == "" || r.ExperimentID == "" {
			res.MeasurementAnomalies = append(res.MeasurementAnomalies, fmt.Sprintf("%s/%s: missing identity fields", r.SampleID, r.Arm))
			continue
		}
		if m, ok := bySample[r.SampleID]; ok {
			m[r.Arm] = r
		} else {
			bySample[r.SampleID] = map[string]sanitizedRow{r.Arm: r}
		}
	}

	type armAgg struct {
		reported, eligible int
		read, denom        int
		latencies          []int
		rows               []sanitizedRow
	}
	agg := map[string]*armAgg{"control": {}, "treatment": {}}
	var pairDeltas []float64

	sampleIDs := make([]string, 0, len(bySample))
	for id := range bySample {
		sampleIDs = append(sampleIDs, id)
	}
	sort.Strings(sampleIDs)

	for _, id := range sampleIDs {
		pair := bySample[id]
		ctrl, cok := pair["control"]
		treat, tok := pair["treatment"]
		if !cok || !tok {
			res.MeasurementAnomalies = append(res.MeasurementAnomalies, fmt.Sprintf("%s: pair incomplete", id))
			continue
		}
		res.ValidPairs++
		for _, r := range []sanitizedRow{ctrl, treat} {
			a := agg[r.Arm]
			a.eligible++
			a.reported++
			a.read += r.CacheReadTokens
			a.denom += r.CacheReportedInput
			a.latencies = append(a.latencies, r.RequestElapsedMS)
			a.rows = append(a.rows, r)
		}
		cr, tr := ratio(ctrl), ratio(treat)
		if cr != nil && tr != nil {
			pairDeltas = append(pairDeltas, *tr-*cr)
		}
	}

	for _, arm := range []string{"control", "treatment"} {
		a := agg[arm]
		if a.eligible > 0 {
			cov := float64(a.reported) / float64(a.eligible)
			if arm == "control" {
				res.ControlReportingCoverage = cov
			} else {
				res.TreatmentReportingCoverage = cov
			}
		}
		if a.denom > 0 {
			r := float64(a.read) / float64(a.denom)
			if arm == "control" {
				res.ControlCachedTokenRatio = &r
			} else {
				res.TreatmentCachedTokenRatio = &r
			}
		}
		sort.Ints(a.latencies)
		if arm == "control" {
			res.ControlP50MS = percentile(a.latencies, 50)
			res.ControlP95MS = percentile(a.latencies, 95)
		} else {
			res.TreatmentP50MS = percentile(a.latencies, 50)
			res.TreatmentP95MS = percentile(a.latencies, 95)
		}
	}

	// Quality gates (plan §3.6/§8.3): per-arm 100% format pass, treatment
	// zero unsupported/handles, recall non-inferior, links non-inferior.
	qg := "PASS"
	treatRows, ctrlRows := agg["treatment"].rows, agg["control"].rows
	if len(treatRows) == 0 || len(ctrlRows) == 0 {
		qg = "FAIL"
	} else {
		for _, r := range treatRows {
			if !r.SchemaFormatPass || !r.SummaryLinePass {
				qg = "FAIL"
			}
			if r.UnsupportedFactCount != 0 || r.ForbiddenHandles != 0 || !r.ValidLinkPass {
				qg = "FAIL"
			}
		}
		for _, r := range ctrlRows {
			if !r.SchemaFormatPass || !r.SummaryLinePass || !r.ValidLinkPass {
				qg = "FAIL"
			}
		}
		if meanRecall(treatRows) < meanRecall(ctrlRows)-0.05 {
			qg = "FAIL"
		}
	}
	res.QualityGate = qg

	res.ReportingCompletenessPass = res.ControlReportingCoverage == 1.0 && res.TreatmentReportingCoverage == 1.0

	// Deterministic paired bootstrap over per-pair deltas.
	res.BootstrapMeanDelta = bootstrapMean(pairDeltas)
	res.BootstrapCI95Lower, res.BootstrapCI95Upper = bootstrapCI(pairDeltas)

	if res.ControlCachedTokenRatio != nil && res.TreatmentCachedTokenRatio != nil {
		d := *res.TreatmentCachedTokenRatio - *res.ControlCachedTokenRatio
		res.AbsoluteDelta = &d
	}

	switch {
	case !res.ReportingCompletenessPass:
		res.PrimaryEffect = "INCONCLUSIVE_TELEMETRY"
	case res.Conservation != "PASS":
		res.PrimaryEffect = "INCONCLUSIVE_TELEMETRY"
	case res.ValidPairs < minValidPairs:
		res.PrimaryEffect = "INCONCLUSIVE_SAMPLE"
	case res.AbsoluteDelta == nil:
		res.PrimaryEffect = "INCONCLUSIVE_TELEMETRY"
	case *res.AbsoluteDelta >= minEffectAbsolute && res.BootstrapCI95Lower > 0 && res.QualityGate == "PASS":
		res.PrimaryEffect = "POSITIVE_EFFECT"
	default:
		res.PrimaryEffect = "NO_DETECTABLE_EFFECT"
	}

	// Frozen price rule has no cache tier (Task012 facts) -> by contract no
	// price benefit can be claimed (§8.4); usage facts remain unaffected.
	res.CostStatus = "NO_PRICE_BENEFIT_BY_CONTRACT"
	return res, nil
}

func meanRecall(rows []sanitizedRow) float64 {
	if len(rows) == 0 {
		return 0
	}
	sum := 0.0
	for _, r := range rows {
		sum += r.RequiredFactRecall
	}
	return sum / float64(len(rows))
}

func ratio(r sanitizedRow) *float64 {
	if r.CacheStatus == "unsupported" || r.CacheStatus == "unreported" || r.CacheReportedInput == 0 {
		return nil
	}
	v := float64(r.CacheReadTokens) / float64(r.CacheReportedInput)
	return &v
}

func bootstrapMean(deltas []float64) float64 {
	if len(deltas) == 0 {
		return 0
	}
	rng := rand.New(rand.NewSource(bootstrapSeed))
	sum := 0.0
	for i := 0; i < bootstrapResamples; i++ {
		s := 0.0
		for j := 0; j < len(deltas); j++ {
			s += deltas[rng.Intn(len(deltas))]
		}
		sum += s / float64(len(deltas))
	}
	return sum / bootstrapResamples
}

func bootstrapCI(deltas []float64) (lower, upper float64) {
	if len(deltas) == 0 {
		return 0, 0
	}
	rng := rand.New(rand.NewSource(bootstrapSeed))
	means := make([]float64, bootstrapResamples)
	for i := 0; i < bootstrapResamples; i++ {
		s := 0.0
		for j := 0; j < len(deltas); j++ {
			s += deltas[rng.Intn(len(deltas))]
		}
		means[i] = s / float64(len(deltas))
	}
	sort.Float64s(means)
	lo := int(math.Floor(0.025 * float64(len(means))))
	hi := int(math.Ceil(0.975*float64(len(means)))) - 1
	if lo < 0 {
		lo = 0
	}
	if hi >= len(means) {
		hi = len(means) - 1
	}
	return means[lo], means[hi]
}

// goldenRows returns a synthetic 12-pair dataset for the offline aggregation
// golden test: treatment ratio dominates control by a wide margin, all
// conservation holds, all usage final. It is explicitly synthetic and never
// mixes with live rows.
func goldenRows() []sanitizedRow {
	var rows []sanitizedRow
	for i := 1; i <= 12; i++ {
		base := sanitizedRow{
			ExperimentID: "golden-offline-synthetic", ProtocolHash: strings.Repeat("ab", 32),
			SampleID: fmt.Sprintf("t013-cache-abi/instance-%02d", i), Ordinal: i,
			LogicalCallID: fmt.Sprintf("golden-%d", i), TraceIDHash: strings.Repeat("cd", 16),
			Provider: "siliconflow", ModelExact: "Qwen/Qwen3-14B", ReportedModelRevision: "Qwen/Qwen3-14B",
			UsageFinality: "REPORTED", AttemptObservability: "UNOBSERVABLE", PricingRuleID: "task012-frozen",
			Currency: "CNY", SchemaFormatPass: true, SummaryLinePass: true, ValidLinkPass: true,
			RequiredFactRecall: 1.0, SourceGroundingPass: true, IncludedInPrimary: true,
		}
		c := base
		c.Arm = "control"
		c.LogicalCallID += "-c"
		c.InputTokens = 4000 + i
		c.CacheReportedInput = c.InputTokens
		c.CacheReadTokens = 200
		c.CacheMissTokens = c.InputTokens - 200
		c.CacheStatus = "hit"
		c.RequestElapsedMS = 900 + i*7
		t := base
		t.Arm = "treatment"
		t.LogicalCallID += "-t"
		t.InputTokens = 4000 + i
		t.CacheReportedInput = t.InputTokens
		t.CacheReadTokens = 3200
		t.CacheMissTokens = t.InputTokens - 3200
		t.CacheStatus = "hit"
		t.RequestElapsedMS = 880 + i*6
		rows = append(rows, c, t)
	}
	return rows
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func parseRows(data []byte) ([]sanitizedRow, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("sanitized TSV missing header")
	}
	header := strings.Split(lines[0], "\t")
	col := func(h []string, name string) int {
		for i, c := range h {
			if c == name {
				return i
			}
		}
		return -1
	}
	req := []string{"sample_id", "arm", "cache_status", "cache_read_tokens", "cache_miss_tokens", "cache_reported_input_tokens", "usage_finality", "included_in_primary"}
	idxs := map[string]int{}
	for _, name := range req {
		i := col(header, name)
		if i < 0 {
			return nil, fmt.Errorf("sanitized TSV missing column %q", name)
		}
		idxs[name] = i
	}
	atoi := func(s string) int {
		v, _ := strconv.Atoi(strings.TrimSpace(s))
		return v
	}
	atob := func(s string) bool {
		return strings.TrimSpace(s) == "true"
	}
	var rows []sanitizedRow
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		get := func(name string) string {
			if i, ok := idxs[name]; ok && i < len(f) {
				return f[i]
			}
			return ""
		}
		rows = append(rows, sanitizedRow{
			SampleID:           get("sample_id"),
			Arm:                get("arm"),
			CacheStatus:        get("cache_status"),
			CacheReadTokens:    atoi(get("cache_read_tokens")),
			CacheMissTokens:    atoi(get("cache_miss_tokens")),
			CacheReportedInput: atoi(get("cache_reported_input_tokens")),
			UsageFinality:      get("usage_finality"),
			IncludedInPrimary:  atob(get("included_in_primary")),
		})
	}
	return rows, nil
}
