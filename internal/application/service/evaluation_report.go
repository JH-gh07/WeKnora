package service

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// evaluationReportService composes the run-level unified report from existing
// durable facts. It reads only; it never writes a report table, never recomputes
// metrics or costs, and never converts an unknown fact into a fabricated zero.
type evaluationReportService struct {
	runRepository interfaces.EvaluationRunRepository
	usageService  interfaces.ModelUsageService
}

// NewEvaluationReportService wires the report composition service.
func NewEvaluationReportService(
	runRepository interfaces.EvaluationRunRepository,
	usageService interfaces.ModelUsageService,
) interfaces.EvaluationReportService {
	return &evaluationReportService{runRepository: runRepository, usageService: usageService}
}

const reportSchemaVersion = "evaluation-run-report/v1"

// GetRunReport returns the versioned report for a tenant-scoped run_id.
//
// Failure authority (Task008): the Run is a Control Fact — a failed run lookup
// fails the whole request. Usage/Cost/Health are Observations — their failure
// degrades the corresponding section to UNKNOWN with a safe reason code and
// never fails the whole report.
func (s *evaluationReportService) GetRunReport(ctx context.Context, runID string) (*types.EvaluationRunReport, error) {
	tenantID := types.MustTenantIDFromContext(ctx)

	run, err := s.runRepository.GetByRunID(ctx, tenantID, runID)
	if err != nil {
		return nil, err
	}

	report := &types.EvaluationRunReport{
		SchemaVersion: reportSchemaVersion,
		Run:           deriveRunSection(run),
		Quality:       deriveQualitySection(run),
		Latency:       deriveLatencySection(run),
		SupportingObservation: types.ReportSupportingSection{
			RunMeasurementStatus: string(run.MeasurementStatus),
		},
		Warnings: make([]types.ReportWarning, 0),
	}

	// Task016 Step 1 containment: a protocol v1 snapshot whose measurement
	// contract is UNVERSIONED (or absent) must be surfaced as NOT_COMPARABLE,
	// never silently treated as blocking-comparable quality evidence.
	if report.Quality.MeasurementContractStatus == "" ||
		report.Quality.MeasurementContractStatus == measurementContractUnversioned {
		report.Warnings = append(report.Warnings, types.ReportWarning{
			ReasonCode: types.ReasonMeasurementContractVersion,
			Section:    "quality",
			Message:    "measurement contract is UNVERSIONED; quality numbers are NOT_COMPARABLE_MEASUREMENT_CONTRACT and must not enter a blocking comparison",
		})
	}

	// Observation: run-scoped model usage + cost + cache.
	agg, aggErr := s.usageService.Aggregate(ctx, types.ModelCallFilter{TenantID: tenantID, RunID: &runID})
	if aggErr != nil {
		report.Usage = types.ReportUsageSection{
			Availability: types.AvailabilityUnknown,
			ReasonCode:   types.ReasonMeasurementIncomplete,
		}
		report.Cost = types.ReportCostSection{
			Availability: types.AvailabilityUnknown,
			ReasonCode:   types.ReasonMeasurementIncomplete,
			IsEstimate:   true,
		}
		report.Warnings = append(report.Warnings, types.ReportWarning{
			ReasonCode: types.ReasonMeasurementIncomplete,
			Section:    "usage",
			Message:    "model usage aggregation unavailable for this run",
		})
		return report, nil
	}

	report.Usage = deriveUsageSection(agg)
	report.Cost = deriveCostSection(agg)
	report.SupportingObservation.PromptCache = derivePromptCacheSupport(agg)
	report.SupportingObservation.LocalEmbeddingCache = deriveLocalCacheSupport(agg)
	report.SupportingObservation.RunMeasurementStatus = string(agg.MeasurementStatus)
	report.SupportingObservation.RunMeasurementHealth = deriveAggregateHealthSupport(runID, agg)
	if agg.LogicalCallCount == 0 {
		report.SupportingObservation.ProviderAttemptObservability = types.AttemptObservabilityUnobservable
	} else if agg.UnobservableProviderAttemptCount > 0 {
		report.SupportingObservation.ProviderAttemptObservability = types.AttemptObservabilityUnobservable
	} else {
		report.SupportingObservation.ProviderAttemptObservability = types.AttemptObservabilityFull
	}
	if agg.MeasurementStatus != types.MeasurementHealthComplete {
		report.Warnings = append(report.Warnings, types.ReportWarning{
			ReasonCode: types.ReasonMeasurementIncomplete,
			Section:    "supporting_observation",
			Message:    "run-scoped logical-call measurement is not complete",
		})
	}

	return report, nil
}

func deriveRunSection(run *types.EvaluationRun) types.ReportRunSection {
	s := types.ReportRunSection{
		RunID:              run.RunID,
		LegacyTaskID:       run.TaskID,
		Status:             string(run.Status),
		ProtocolHash:       run.ProtocolHash,
		GitCommit:          run.GitCommit,
		AppVersion:         run.AppVersion,
		PersistenceStatus:  string(run.PersistenceStatus),
		CleanupStatus:      string(run.CleanupStatus),
		InterruptionReason: run.InterruptionReason,
	}
	if run.StartedAt != nil {
		s.StartedAt = run.StartedAt.UTC().Format(time.RFC3339)
	}
	if run.EndedAt != nil {
		s.EndedAt = run.EndedAt.UTC().Format(time.RFC3339)
	}
	return s
}

// legacyMetricDefinitions lists the retrieval metric JSON field names whose
// current implementation uses legacy (non-standard) semantics (Task016 Step 1).
// They are advisory-only and never blocking. The names are explicitly prefixed
// legacy_nonstandard_ so they cannot collide with standard metric names (AC07).
var legacyMetricDefinitions = []string{"legacy_nonstandard_precision", "legacy_nonstandard_map"}

// measurementContractStatus extracts the measurement_contract_status from the
// secret-free protocol snapshot. Empty/missing/unparseable snapshots yield an
// empty string, which is treated as not-comparable by the caller.
func measurementContractStatus(snapshot types.JSON) string {
	if len(snapshot) == 0 {
		return ""
	}
	var proto struct {
		MeasurementContractStatus string `json:"measurement_contract_status"`
		MeasurementContractHash   string `json:"measurement_contract_hash"`
	}
	if err := json.Unmarshal(snapshot, &proto); err != nil {
		return ""
	}
	if proto.MeasurementContractHash != "" {
		return proto.MeasurementContractHash
	}
	return proto.MeasurementContractStatus
}

func deriveQualitySection(run *types.EvaluationRun) types.ReportQualitySection {
	q := types.ReportQualitySection{
		MeasurementContractStatus: measurementContractStatus(run.ProtocolSnapshot),
		LegacyMetricDefinitions:   legacyMetricDefinitions,
	}
	if !run.IsTerminal() {
		q.Availability = types.AvailabilityNotFinal
		q.ReasonCode = types.ReasonRunNotTerminal
		return q
	}
	if !run.MetricsValid {
		q.Availability = types.AvailabilityUnknown
		q.ReasonCode = types.ReasonMetricsInvalid
		return q
	}
	if len(run.MetricsJSON) == 0 {
		q.Availability = types.AvailabilityUnknown
		q.ReasonCode = types.ReasonMetricsMissing
		return q
	}
	metric, ok := decodeCompleteMetricResult(json.RawMessage(run.MetricsJSON))
	if !ok {
		q.Availability = types.AvailabilityUnknown
		q.ReasonCode = types.ReasonMetricsMalformed
		return q
	}
	q.Availability = types.AvailabilityAvailable
	q.ReasonCode = types.ReasonQualityComplete
	q.MetricsValid = true
	q.Retrieval = &metric.RetrievalMetrics
	q.Answer = &metric.GenerationMetrics
	// Standard measurement-contract/v1 metrics + the contract hash under which
	// they were computed (Task016 Step 4). These are optional: older/legacy runs
	// carry only the legacy result and so have a nil StandardRetrieval and empty
	// MeasurementContractHash (never fabricated from the legacy numbers).
	q.StandardRetrieval, q.MeasurementContractHash = decodeStandardMetrics(json.RawMessage(run.MetricsJSON))
	return q
}

// decodeStandardMetrics extracts the optional standard retrieval metrics and the
// measurement contract hash from the persisted MetricsJSON. A legacy/UNVERSIONED
// run without these keys yields (nil, "") and must never have its legacy numbers
// reinterpreted as standard (I04, AC08).
func decodeStandardMetrics(raw json.RawMessage) (*types.StandardRetrievalMetrics, string) {
	var top struct {
		StandardRetrieval       *types.StandardRetrievalMetrics `json:"standard_retrieval_metrics"`
		MeasurementContractHash string                          `json:"measurement_contract_hash"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, ""
	}
	return top.StandardRetrieval, top.MeasurementContractHash
}

func decodeCompleteMetricResult(raw json.RawMessage) (types.MetricResult, bool) {
	var metric types.MetricResult
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return metric, false
	}
	required := map[string][]string{
		"retrieval_metrics":  {"legacy_nonstandard_precision", "recall", "ndcg3", "ndcg10", "mrr", "legacy_nonstandard_map"},
		"generation_metrics": {"bleu1", "bleu2", "bleu4", "rouge1", "rouge2", "rougel"},
	}
	for section, fields := range required {
		sectionRaw, exists := top[section]
		if !exists || string(sectionRaw) == "null" {
			return metric, false
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(sectionRaw, &values); err != nil {
			return metric, false
		}
		for _, field := range fields {
			valueRaw, exists := values[field]
			if !exists || string(valueRaw) == "null" {
				return metric, false
			}
			var value float64
			if err := json.Unmarshal(valueRaw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
				return metric, false
			}
		}
	}
	if err := json.Unmarshal(raw, &metric); err != nil {
		return types.MetricResult{}, false
	}
	return metric, true
}

func deriveLatencySection(run *types.EvaluationRun) types.ReportLatencySection {
	l := types.ReportLatencySection{Definition: "ended_at - started_at"}
	if run.Status == types.EvaluationRunStatusRunning || run.Status == types.EvaluationRunStatusPending {
		l.Availability = types.AvailabilityNotFinal
		l.ReasonCode = types.ReasonRunNotTerminal
		return l
	}
	if run.StartedAt == nil || run.EndedAt == nil {
		l.Availability = types.AvailabilityUnknown
		l.ReasonCode = types.ReasonTimestampMissing
		return l
	}
	d := run.EndedAt.Sub(*run.StartedAt)
	if d < 0 {
		l.Availability = types.AvailabilityUnknown
		l.ReasonCode = types.ReasonTimestampInvalid
		return l
	}
	ms := d.Milliseconds()
	l.Availability = types.AvailabilityAvailable
	l.EvaluationWallClockMS = &ms
	return l
}

func deriveUsageSection(agg *types.ModelUsageAggregate) types.ReportUsageSection {
	u := types.ReportUsageSection{}
	if agg == nil {
		u.Availability = types.AvailabilityUnknown
		u.ReasonCode = types.ReasonNoObservedModelCall
		return u
	}
	u.LogicalCallCount = agg.LogicalCallCount
	u.SuccessCount = agg.SuccessCount
	u.FailureCount = agg.FailureCount
	u.InputTokens = agg.InputTokens
	u.OutputTokens = agg.OutputTokens
	if agg.LogicalCallCount == 0 {
		u.Availability = types.AvailabilityUnknown
		u.ReasonCode = types.ReasonNoObservedModelCall
		return u
	}
	u.Availability = types.AvailabilityAvailable
	return u
}

func deriveCostSection(agg *types.ModelUsageAggregate) types.ReportCostSection {
	c := types.ReportCostSection{IsEstimate: true}
	if agg == nil {
		c.Availability = types.AvailabilityUnknown
		c.ReasonCode = types.ReasonNoObservedModelCall
		return c
	}
	c.KnownCostTotal = agg.KnownCostTotal
	c.Currency = agg.Currency
	c.UnknownCostCallCount = agg.UnknownCostCallCount
	c.MixedCurrency = agg.MixedCurrency
	switch {
	case agg.MixedCurrency:
		c.Availability = types.AvailabilityUnknown
		c.ReasonCode = types.ReasonMixedCurrency
	case agg.LogicalCallCount == 0:
		c.Availability = types.AvailabilityUnknown
		c.ReasonCode = types.ReasonNoObservedModelCall
	case agg.KnownCostTotal == nil:
		c.Availability = types.AvailabilityUnknown
		c.ReasonCode = types.ReasonAllCostUnknown
	case agg.UnknownCostCallCount > 0:
		c.Availability = types.AvailabilityPartial
		c.ReasonCode = types.ReasonSomeCostUnknown
	default:
		c.Availability = types.AvailabilityAvailable
	}
	return c
}

func derivePromptCacheSupport(agg *types.ModelUsageAggregate) *types.ReportPromptCacheSupport {
	if agg == nil {
		return nil
	}
	return &types.ReportPromptCacheSupport{
		EligibleCount:            agg.CacheEligibleCount,
		ReportedCount:            agg.CacheReportedCount,
		UnsupportedCount:         agg.CacheUnsupportedCount,
		CacheReadTokens:          agg.CacheReadTokens,
		CacheWriteTokens:         agg.CacheWriteTokens,
		CacheMissTokens:          agg.CacheMissTokens,
		CacheReportedInputTokens: agg.CacheReportedInputTokens,
	}
}

func deriveLocalCacheSupport(agg *types.ModelUsageAggregate) *types.ReportLocalCacheSupport {
	if agg == nil || agg.LocalEmbeddingCache == nil {
		return nil
	}
	l := agg.LocalEmbeddingCache
	return &types.ReportLocalCacheSupport{
		ImplementationStatus: string(l.ImplementationStatus),
		BatchInvocationCount: l.BatchInvocationCount,
		LogicalItemCount:     l.LogicalItemCount,
		HitCount:             l.HitCount,
		MissCount:            l.MissCount,
		MeasurementStatus:    string(l.MeasurementStatus),
	}
}

func deriveHealthSupport(h *types.MeasurementHealth) *types.ReportMeasurementHealth {
	if h == nil {
		return nil
	}
	return &types.ReportMeasurementHealth{
		RunID:                            h.RunID,
		From:                             h.From.UTC().Format(time.RFC3339),
		To:                               h.To.UTC().Format(time.RFC3339),
		Status:                           string(h.Status),
		MeteringAttemptedCount:           h.MeteringAttemptedCount,
		MeteringPersistedCount:           h.MeteringPersistedCount,
		MeteringFailedCount:              h.MeteringFailedCount,
		ExpectedLogicalCallCount:         h.ExpectedLogicalCallCount,
		UnobservableProviderAttemptCount: h.UnobservableProviderAttemptCount,
	}
}

func deriveAggregateHealthSupport(runID string, agg *types.ModelUsageAggregate) *types.ReportMeasurementHealth {
	if agg == nil {
		return nil
	}
	return &types.ReportMeasurementHealth{
		RunID:                            runID,
		Status:                           string(agg.MeasurementStatus),
		ExpectedLogicalCallCount:         agg.ExpectedLogicalCallCount,
		MeteringAttemptedCount:           agg.MeteringAttemptedCount,
		MeteringPersistedCount:           agg.MeteringPersistedCount,
		MeteringFailedCount:              agg.MeteringFailedCount,
		UnobservableProviderAttemptCount: agg.UnobservableProviderAttemptCount,
	}
}

// runWindow returns the tenant-window health interval for a run. It defaults to
// a 24h window ending now and is narrowed by the run's persisted timestamps.
func runWindow(run *types.EvaluationRun) (time.Time, time.Time) {
	now := time.Now().UTC()
	from := now.Add(-24 * time.Hour)
	to := now
	if run.StartedAt != nil {
		from = run.StartedAt.UTC()
	}
	if run.EndedAt != nil {
		to = run.EndedAt.UTC()
	}
	return from, to
}
