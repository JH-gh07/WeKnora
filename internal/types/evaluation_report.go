package types

// EvaluationRunReport is the versioned run-level unified report. It is a
// read-time projection over existing durable facts (EvaluationRun, ModelCall
// aggregate, embedding cache aggregate) — never a materialized table.
type EvaluationRunReport struct {
	SchemaVersion         string                  `json:"schema_version"`
	Run                   ReportRunSection        `json:"run"`
	Quality               ReportQualitySection    `json:"quality"`
	Usage                 ReportUsageSection      `json:"usage"`
	Cost                  ReportCostSection       `json:"cost"`
	Latency               ReportLatencySection    `json:"latency"`
	SupportingObservation ReportSupportingSection `json:"supporting_observation"`
	Warnings              []ReportWarning         `json:"warnings"`
}

// ReportAvailability is the top-level availability vocabulary for report
// sections. It is deliberately distinct from raw business enums so a missing,
// partial, unknown and unsupported fact can be told apart.
type ReportAvailability string

const (
	AvailabilityAvailable   ReportAvailability = "AVAILABLE"
	AvailabilityPartial     ReportAvailability = "PARTIAL"
	AvailabilityUnknown     ReportAvailability = "UNKNOWN"
	AvailabilityNotFinal    ReportAvailability = "NOT_FINAL"
	AvailabilityUnsupported ReportAvailability = "UNSUPPORTED"
	AvailabilityDisabled    ReportAvailability = "DISABLED"
)

// Report warning/reason codes. These are stable and machine-readable; the
// human-facing copy is derived from them and must not participate in tests.
const (
	ReasonQualityComplete            = "QUALITY_COMPLETE"
	ReasonRunNotTerminal             = "RUN_NOT_TERMINAL"
	ReasonMetricsInvalid             = "METRICS_INVALID"
	ReasonMetricsMissing             = "METRICS_MISSING"
	ReasonMetricsMalformed           = "METRICS_MALFORMED"
	ReasonNoObservedModelCall        = "NO_OBSERVED_MODEL_CALL"
	ReasonMeasurementIncomplete      = "MEASUREMENT_INCOMPLETE"
	ReasonRunMeasurementScopeUnavail = "RUN_MEASUREMENT_SCOPE_UNAVAILABLE"
	ReasonAllCostUnknown             = "ALL_COST_UNKNOWN"
	ReasonSomeCostUnknown            = "SOME_COST_UNKNOWN"
	ReasonMixedCurrency              = "MIXED_CURRENCY"
	ReasonTimestampMissing           = "TIMESTAMP_MISSING"
	ReasonTimestampInvalid           = "TIMESTAMP_INVALID"
	ReasonProviderCacheUnsupported   = "PROVIDER_CACHE_UNSUPPORTED"
	ReasonProviderCacheUnreported    = "PROVIDER_CACHE_UNREPORTED"
	ReasonLocalCacheDisabled         = "LOCAL_CACHE_DISABLED"
)

// ReportWarning is a stable, reason-coded warning attached to a report section.
type ReportWarning struct {
	ReasonCode string `json:"reason_code"`
	Section    string `json:"section"`
	Message    string `json:"message"`
}

// ReportRunSection is the lifecycle / identity / provenance summary of the run.
type ReportRunSection struct {
	RunID              string `json:"run_id"`
	LegacyTaskID       string `json:"legacy_task_id"`
	Status             string `json:"status"`
	StartedAt          string `json:"started_at,omitempty"`
	EndedAt            string `json:"ended_at,omitempty"`
	ProtocolHash       string `json:"protocol_hash,omitempty"`
	GitCommit          string `json:"git_commit,omitempty"`
	AppVersion         string `json:"app_version,omitempty"`
	PersistenceStatus  string `json:"persistence_status,omitempty"`
	CleanupStatus      string `json:"cleanup_status,omitempty"`
	InterruptionReason string `json:"interruption_reason,omitempty"`
}

// ReportQualitySection holds retrieval and answer quality. It is only
// AVAILABLE when the run is terminal and metrics are valid; a real zero score
// is preserved, an unknown score is never rendered as zero.
type ReportQualitySection struct {
	Availability ReportAvailability `json:"availability"`
	ReasonCode   string             `json:"reason_code,omitempty"`
	MetricsValid bool               `json:"metrics_valid"`
	Retrieval    *RetrievalMetrics  `json:"retrieval,omitempty"`
	Answer       *GenerationMetrics `json:"answer,omitempty"`
}

// ReportUsageSection is the run_id-scoped observed usage fact.
type ReportUsageSection struct {
	Availability     ReportAvailability `json:"availability"`
	ReasonCode       string             `json:"reason_code,omitempty"`
	LogicalCallCount int64              `json:"logical_call_count"`
	SuccessCount     int64              `json:"success_count"`
	FailureCount     int64              `json:"failure_count"`
	InputTokens      *int64             `json:"input_tokens,omitempty"`
	OutputTokens     *int64             `json:"output_tokens,omitempty"`
}

// ReportCostSection is the run_id-scoped cost fact. It distinguishes known
// subtotal, unknown calls and mixed currency; it is always an estimate.
type ReportCostSection struct {
	Availability         ReportAvailability `json:"availability"`
	ReasonCode           string             `json:"reason_code,omitempty"`
	KnownCostTotal       *float64           `json:"known_cost_total,omitempty"`
	Currency             *string            `json:"currency,omitempty"`
	UnknownCostCallCount int64              `json:"unknown_cost_call_count"`
	MixedCurrency        bool               `json:"mixed_currency"`
	IsEstimate           bool               `json:"is_estimate"`
}

// ReportLatencySection is the evaluation wall-clock duration, derived only from
// the persisted run timestamps.
type ReportLatencySection struct {
	Availability          ReportAvailability `json:"availability"`
	ReasonCode            string             `json:"reason_code,omitempty"`
	EvaluationWallClockMS *int64             `json:"evaluation_wall_clock_ms,omitempty"`
	Definition            string             `json:"definition"`
}

// ReportSupportingSection carries supporting observations that are NOT part of
// the four primary results: prompt cache, local embedding cache, measurement
// health and trust boundaries.
type ReportSupportingSection struct {
	PromptCache                     *ReportPromptCacheSupport `json:"prompt_cache,omitempty"`
	LocalEmbeddingCache             *ReportLocalCacheSupport  `json:"local_embedding_cache,omitempty"`
	RunMeasurementStatus            string                    `json:"run_measurement_status"`
	TenantWindowHealth              *ReportMeasurementHealth  `json:"tenant_window_health,omitempty"`
	TenantWindowHealthIsNotRunScope bool                      `json:"tenant_window_health_is_not_run_completeness"`
}

// ReportPromptCacheSupport is the reported prompt-cache denominator contract.
type ReportPromptCacheSupport struct {
	EligibleCount            int64  `json:"eligible_count"`
	ReportedCount            int64  `json:"reported_count"`
	UnsupportedCount         int64  `json:"unsupported_count"`
	CacheReadTokens          *int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens         *int64 `json:"cache_write_tokens,omitempty"`
	CacheMissTokens          *int64 `json:"cache_miss_tokens,omitempty"`
	CacheReportedInputTokens *int64 `json:"cache_reported_input_tokens,omitempty"`
}

// ReportLocalCacheSupport is the independent local embedding cache fact.
type ReportLocalCacheSupport struct {
	ImplementationStatus string `json:"implementation_status"`
	BatchInvocationCount int64  `json:"batch_invocation_count"`
	LogicalItemCount     int64  `json:"logical_item_count"`
	HitCount             int64  `json:"hit_count"`
	MissCount            int64  `json:"miss_count"`
	MeasurementStatus    string `json:"measurement_status"`
}

// ReportMeasurementHealth is the tenant-window metering health (NOT run-level).
type ReportMeasurementHealth struct {
	From                   string `json:"from"`
	To                     string `json:"to"`
	Status                 string `json:"status"`
	MeteringAttemptedCount int64  `json:"metering_attempted_count"`
	MeteringPersistedCount int64  `json:"metering_persisted_count"`
	MeteringFailedCount    int64  `json:"metering_failed_count"`
}
