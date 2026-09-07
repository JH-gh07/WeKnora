package types

import (
	"context"
	"time"
)

// ModelCallRecorder is the narrow dependency accepted by model decorators.
// Implementations must not return prompt or response data, and callers treat
// recorder errors as observability failures rather than business failures.
//
// The lifecycle is durable STARTED -> PERSISTED/FAILED:
//   - BeginModelCall writes a "started, not yet persisted" health marker before
//     the provider round-trip, so a crash, context cancellation, or DB outage
//     after the call still leaves a trace the window can count as not-persisted
//     (never silently COMPLETE).
//   - RecordModelCall persists the call fact and finalizes the marker as
//     persisted in one transaction. If that write fails the marker stays
//     not-persisted and the window degrades to PARTIAL.
//
// Both writes use a context detached from cancellation with a short timeout,
// so a cancelled business context cannot silently drop metering.
type ModelCallRecorder interface {
	BeginModelCall(ctx context.Context, tenantID uint64, at time.Time) (healthID string, err error)
	RecordModelCall(ctx context.Context, healthID string, call *ModelCall) error
}

// ModelOperation identifies the logical model abstraction that was invoked.
type ModelOperation string

const (
	ModelOperationChat      ModelOperation = "chat"
	ModelOperationEmbedding ModelOperation = "embedding"
	ModelOperationRerank    ModelOperation = "rerank"
)

type UsageFinality string

const (
	UsageFinalityReported    UsageFinality = "reported"
	UsageFinalityPartial     UsageFinality = "partial"
	UsageFinalityUnavailable UsageFinality = "unavailable"
)

type AttemptObservability string

const (
	AttemptObservabilityFull         AttemptObservability = "full"
	AttemptObservabilityPartial      AttemptObservability = "partial"
	AttemptObservabilityUnobservable AttemptObservability = "unobservable"
)

type MeasurementHealthStatus string

const (
	MeasurementHealthComplete MeasurementHealthStatus = "COMPLETE"
	MeasurementHealthPartial  MeasurementHealthStatus = "PARTIAL"
	MeasurementHealthUnknown  MeasurementHealthStatus = "UNKNOWN"
)

// PricingIdentity is copied onto every call. It is intentionally immutable so
// changing current model pricing cannot rewrite historical costs.
type PricingIdentity struct {
	Version     string     `json:"pricing_version,omitempty"`
	Source      string     `json:"pricing_source,omitempty"`
	EffectiveAt *time.Time `json:"pricing_effective_at,omitempty"`
}

// PricingStatus is the pricing availability of a ModelCall. A call is only
// PRICED when a frozen exact rule and final provider-reported usage produce a
// reproducible fixed-point estimate. Everything else is UNKNOWN with a reason.
type PricingStatus string

const (
	PricingStatusPriced  PricingStatus = "PRICED"
	PricingStatusUnknown PricingStatus = "UNKNOWN"
)

// PricingReason is the fail-closed allowlist explaining an UNKNOWN cost fact.
// It must never carry provider error bodies, prompts, or response content.
type PricingReason string

const (
	PricingReasonLegacyUnpriced               PricingReason = "LEGACY_UNPRICED"
	PricingReasonNoExactRule                  PricingReason = "NO_EXACT_RULE"
	PricingReasonRuleNotYetValid              PricingReason = "RULE_NOT_YET_VALID"
	PricingReasonRuleExpired                  PricingReason = "RULE_EXPIRED"
	PricingReasonCatalogUnavailable           PricingReason = "CATALOG_UNAVAILABLE"
	PricingReasonUsageUnavailable             PricingReason = "USAGE_UNAVAILABLE"
	PricingReasonUsagePartial                 PricingReason = "USAGE_PARTIAL"
	PricingReasonInvalidUsage                 PricingReason = "INVALID_USAGE"
	PricingReasonUnobservableBillingDimension PricingReason = "UNOBSERVABLE_BILLING_DIMENSION"
	PricingReasonCalculationOverflow          PricingReason = "CALCULATION_OVERFLOW"
	PricingReasonNonProviderPricedModel       PricingReason = "NON_PROVIDER_PRICED_MODEL"
)

// ModelCall is one logical Chat, Embedding, or Rerank abstraction invocation.
// Nullable usage and cost fields represent unknown values; zero is a real,
// observed zero and must not be used as a stand-in for unavailable telemetry.
type ModelCall struct {
	ID       string  `json:"id" gorm:"column:id;primaryKey;type:varchar(36)"`
	TenantID uint64  `json:"tenant_id" gorm:"column:tenant_id;index"`
	RunID    *string `json:"run_id,omitempty" gorm:"column:run_id;type:varchar(36);index"`
	TaskID   *string `json:"task_id,omitempty" gorm:"column:task_id;type:varchar(128);index"`
	TraceID  string  `json:"trace_id,omitempty" gorm:"column:trace_id;type:varchar(128)"`

	ModelID   string         `json:"model_id" gorm:"column:model_id;type:varchar(128);index"`
	ModelName string         `json:"model_name" gorm:"column:model_name;type:varchar(256)"`
	Provider  string         `json:"provider" gorm:"column:provider;type:varchar(64)"`
	Operation ModelOperation `json:"operation" gorm:"column:operation;type:varchar(32);index"`
	Purpose   string         `json:"purpose,omitempty" gorm:"column:purpose;type:varchar(128)"`

	ReportedModelRevision *string `json:"reported_model_revision,omitempty" gorm:"column:reported_model_revision;type:varchar(128)"`
	DeploymentID          *string `json:"deployment_id,omitempty" gorm:"column:deployment_id;type:varchar(128)"`

	InputTokens      *int `json:"input_tokens,omitempty" gorm:"column:input_tokens"`
	OutputTokens     *int `json:"output_tokens,omitempty" gorm:"column:output_tokens"`
	CacheReadTokens  *int `json:"cache_read_tokens,omitempty" gorm:"column:cache_read_tokens"`
	CacheWriteTokens *int `json:"cache_write_tokens,omitempty" gorm:"column:cache_write_tokens"`
	CacheMissTokens  *int `json:"cache_miss_tokens,omitempty" gorm:"column:cache_miss_tokens"`
	// CacheReportedInputTokens is the provider-reported input-token denominator
	// for this call. It is populated only when prompt-cache telemetry is reported;
	// callers must not reconstruct it from read/write/miss subsets.
	CacheReportedInputTokens *int              `json:"cache_reported_input_tokens,omitempty" gorm:"column:cache_reported_input_tokens"`
	UsageFinality            UsageFinality     `json:"usage_finality" gorm:"column:usage_finality;type:varchar(24)"`
	CacheStatus              PromptCacheStatus `json:"cache_status" gorm:"column:cache_status;type:varchar(24)"`

	ProviderLatencyMS    *int                 `json:"provider_latency_ms,omitempty" gorm:"column:provider_latency_ms"`
	RequestElapsedMS     int                  `json:"request_elapsed_ms" gorm:"column:request_elapsed_ms"`
	Success              bool                 `json:"success" gorm:"column:success"`
	ErrorType            string               `json:"error_type,omitempty" gorm:"column:error_type;type:varchar(128)"`
	AttemptCount         *int                 `json:"attempt_count,omitempty" gorm:"column:attempt_count"`
	AttemptObservability AttemptObservability `json:"attempt_observability" gorm:"column:attempt_observability;type:varchar(24)"`

	EstimatedCost      *float64   `json:"estimated_cost,omitempty" gorm:"column:estimated_cost;type:decimal(20,10)"`
	Currency           string     `json:"currency,omitempty" gorm:"column:currency;type:varchar(8)"`
	PricingVersion     string     `json:"pricing_version,omitempty" gorm:"column:pricing_version;type:varchar(64)"`
	PricingSource      string     `json:"pricing_source,omitempty" gorm:"column:pricing_source;type:varchar(128)"`
	PricingEffectiveAt *time.Time `json:"pricing_effective_at,omitempty" gorm:"column:pricing_effective_at"`

	// Recomputable pricing snapshot (Task012 §6.2). All fields are nullable so
	// legacy rows stay NULL and are interpreted as legacy-unknown. Only a
	// PRICED row fills every non-nullable field here together with the
	// compatibility EstimatedCost/Currency/Pricing* fields above.
	PricingStatus        PricingStatus `json:"pricing_status,omitempty" gorm:"column:pricing_status;type:varchar(24)"`
	PricingReason        PricingReason `json:"pricing_reason,omitempty" gorm:"column:pricing_reason;type:varchar(48)"`
	PricingRuleID        string        `json:"pricing_rule_id,omitempty" gorm:"column:pricing_rule_id;type:varchar(128)"`
	PricingCatalogHash   string        `json:"pricing_catalog_hash,omitempty" gorm:"column:pricing_catalog_hash;type:varchar(64)"`
	PricingUnit          string        `json:"pricing_unit,omitempty" gorm:"column:pricing_unit;type:varchar(32)"`
	InputUnitPriceNanos  *int64        `json:"input_unit_price_nanos_per_million,omitempty" gorm:"column:input_unit_price_nanos_per_million"`
	OutputUnitPriceNanos *int64        `json:"output_unit_price_nanos_per_million,omitempty" gorm:"column:output_unit_price_nanos_per_million"`
	CacheReadUnitNanos   *int64        `json:"cache_read_unit_price_nanos_per_million,omitempty" gorm:"column:cache_read_unit_price_nanos_per_million"`
	CacheWriteUnitNanos  *int64        `json:"cache_write_unit_price_nanos_per_million,omitempty" gorm:"column:cache_write_unit_price_nanos_per_million"`
	EstimatedCostNanos   *int64        `json:"estimated_cost_nanos,omitempty" gorm:"column:estimated_cost_nanos"`

	CreatedAt time.Time `json:"created_at" gorm:"column:created_at;index"`
}

func (ModelCall) TableName() string { return "model_calls" }

type ModelCallFilter struct {
	TenantID  uint64
	RunID     *string
	ModelID   string
	Operation ModelOperation
	Purpose   string
	From, To  *time.Time
}

type ModelUsageAggregate struct {
	LogicalCallCount         int64    `json:"logical_call_count"`
	SuccessCount             int64    `json:"success_count"`
	FailureCount             int64    `json:"failure_count"`
	InputTokens              *int64   `json:"input_tokens,omitempty"`
	OutputTokens             *int64   `json:"output_tokens,omitempty"`
	CacheReadTokens          *int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens         *int64   `json:"cache_write_tokens,omitempty"`
	CacheMissTokens          *int64   `json:"cache_miss_tokens,omitempty"`
	CacheReportedInputTokens *int64   `json:"cache_reported_input_tokens,omitempty"`
	KnownCostTotal           *float64 `json:"known_cost_total,omitempty"`
	Currency                 *string  `json:"currency,omitempty"`
	MixedCurrency            bool     `json:"mixed_currency"`
	UnknownCostCallCount     int64    `json:"unknown_cost_call_count"`
	// PricedCallCount and PricingUnknownReasonCounts are additive diagnostics:
	// how many calls carry a PRICED fact and, for UNKNOWN rows, the fail-closed
	// reason distribution. They never change the cost total semantics above.
	PricedCallCount            int64                   `json:"priced_call_count"`
	PricingUnknownReasonCounts map[string]int64        `json:"pricing_unknown_reason_counts,omitempty"`
	CacheEligibleCount         int64                   `json:"cache_eligible_count"`
	CacheReportedCount         int64                   `json:"cache_reported_count"`
	CacheUnsupportedCount      int64                   `json:"cache_unsupported_count"`
	MeasurementStatus          MeasurementHealthStatus `json:"measurement_status"`
	MeteringAttemptedCount     int64                   `json:"metering_attempted_count"`
	MeteringPersistedCount     int64                   `json:"metering_persisted_count"`
	MeteringFailedCount        int64                   `json:"metering_failed_count"`
	// LocalEmbeddingCache is the additive local-cache fact. It is always
	// present (never merged into the Prompt Cache fields above) and reports
	// DISABLED when the capability is implemented but the rollout switch is
	// off, so a zero hit count is never confused with "not implemented".
	LocalEmbeddingCache *EmbeddingCacheAggregate `json:"local_embedding_cache"`
}

type MeasurementHealth struct {
	TenantID               uint64                  `json:"tenant_id"`
	From                   time.Time               `json:"from"`
	To                     time.Time               `json:"to"`
	MeteringAttemptedCount int64                   `json:"metering_attempted_count"`
	MeteringPersistedCount int64                   `json:"metering_persisted_count"`
	MeteringFailedCount    int64                   `json:"metering_failed_count"`
	Status                 MeasurementHealthStatus `json:"measurement_status"`
}
