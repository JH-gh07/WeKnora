package types

import (
	"context"
	"time"
)

// EmbeddingCacheMode marks whether a logical embedding invocation participated
// in the local embedding cache critical path.
type EmbeddingCacheMode string

const (
	EmbeddingCacheModeOff EmbeddingCacheMode = "OFF"
	EmbeddingCacheModeOn  EmbeddingCacheMode = "ON"
)

// EmbeddingCacheImplementationStatus describes the rollout state of the local
// embedding cache capability for an API/UI window. It is a config fact, not a
// measurement-derived fact: a disabled cache still reports DISABLED even though
// its observation counts are necessarily zero.
type EmbeddingCacheImplementationStatus string

const (
	EmbeddingCacheImplementationEnabled  EmbeddingCacheImplementationStatus = "ENABLED"
	EmbeddingCacheImplementationDisabled EmbeddingCacheImplementationStatus = "DISABLED"
)

// EmbeddingCachePersistenceStatus is the durable lifecycle of one observation
// row. STARTED is written before the lookup/provider round-trip; a row that is
// never finalized remains STARTED so the window cannot be reported COMPLETE.
type EmbeddingCachePersistenceStatus string

const (
	EmbeddingCachePersistenceStarted   EmbeddingCachePersistenceStatus = "STARTED"
	EmbeddingCachePersistencePersisted EmbeddingCachePersistenceStatus = "PERSISTED"
	EmbeddingCachePersistenceFailed    EmbeddingCachePersistenceStatus = "FAILED"
)

// EmbeddingCacheIdentity is the expected computation identity used to validate
// a retrieved entry beyond the cache_key lookup. The cache_key already encodes
// all of these; re-checking them is cheap defense against a corrupt or
// mis-versioned row. The vector dimension is validated against the entry's own
// stored Dimensions (not this struct), because providers may omit the requested
// dimension and return a default.
type EmbeddingCacheIdentity struct {
	ModelID                string
	ProviderIdentity       string
	ModelConfigFingerprint string
	SchemaVersion          int
}

// EmbeddingCacheEntry is one tenant-scoped computation-identity cache entry.
// The vector payload is a JSON array of float32 stored as portable text so
// PostgreSQL and SQLite round-trip identically. Raw input text and credentials
// are never stored.
type EmbeddingCacheEntry struct {
	ID                     string    `gorm:"column:id;primaryKey;type:varchar(64)"`
	TenantID               uint64    `gorm:"column:tenant_id;not null"`
	CacheKey               string    `gorm:"column:cache_key;type:varchar(64);not null"`
	ModelID                string    `gorm:"column:model_id;type:varchar(128);index"`
	ProviderIdentity       string    `gorm:"column:provider_identity;type:varchar(256)"`
	ModelConfigFingerprint string    `gorm:"column:model_config_fingerprint;type:varchar(64)"`
	CacheSchemaVersion     int       `gorm:"column:cache_schema_version"`
	Dimensions             int       `gorm:"column:dimensions"`
	VectorPayload          string    `gorm:"column:vector_payload;type:text"`
	CreatedAt              time.Time `gorm:"column:created_at;index"`
	UpdatedAt              time.Time `gorm:"column:updated_at"`
}

func (EmbeddingCacheEntry) TableName() string { return "embedding_cache_entries" }

// EmbeddingCacheObservation is one logical embedding abstraction invocation that
// passed through the enabled cache layer. Pure OFF control writes no row. It is
// deliberately distinct from ModelCall: a cache HIT creates an observation but
// no provider-bound ModelCall.
type EmbeddingCacheObservation struct {
	ID                          string                          `gorm:"column:id;primaryKey;type:varchar(36)"`
	TenantID                    uint64                          `gorm:"column:tenant_id;index"`
	RunID                       *string                         `gorm:"column:run_id;type:varchar(36);index"`
	TaskID                      *string                         `gorm:"column:task_id;type:varchar(128);index"`
	TraceID                     string                          `gorm:"column:trace_id;type:varchar(128)"`
	ModelID                     string                          `gorm:"column:model_id;type:varchar(128);index"`
	Operation                   string                          `gorm:"column:operation;type:varchar(32)"`
	CacheMode                   EmbeddingCacheMode              `gorm:"column:cache_mode;type:varchar(8)"`
	LogicalEmbeddingItemCount   int64                           `gorm:"column:logical_embedding_item_count"`
	LocalEmbeddingHitCount      int64                           `gorm:"column:local_embedding_hit_count"`
	LocalEmbeddingMissCount     int64                           `gorm:"column:local_embedding_miss_count"`
	LocalEmbeddingBypassCount   int64                           `gorm:"column:local_embedding_bypass_count"`
	LookupFailureCount          int64                           `gorm:"column:lookup_failure_count"`
	CorruptionCount             int64                           `gorm:"column:corruption_count"`
	WriteFailureCount           int64                           `gorm:"column:write_failure_count"`
	ProviderBoundModelCallCount int64                           `gorm:"column:provider_bound_model_call_count"`
	PersistenceStatus           EmbeddingCachePersistenceStatus `gorm:"column:persistence_status;type:varchar(16)"`
	RequestElapsedMS            int64                           `gorm:"column:request_elapsed_ms"`
	CreatedAt                   time.Time                       `gorm:"column:created_at;index"`
	FinalizedAt                 *time.Time                      `gorm:"column:finalized_at"`
}

func (EmbeddingCacheObservation) TableName() string { return "embedding_cache_observations" }

// EmbeddingCacheObservationFinalize carries the counts written when an
// observation transitions STARTED -> PERSISTED.
type EmbeddingCacheObservationFinalize struct {
	LocalEmbeddingHitCount      int64
	LocalEmbeddingMissCount     int64
	LocalEmbeddingBypassCount   int64
	LookupFailureCount          int64
	CorruptionCount             int64
	WriteFailureCount           int64
	ProviderBoundModelCallCount int64
	RequestElapsedMS            int64
}

// EmbeddingCacheObservationFilter scopes an observation aggregation.
type EmbeddingCacheObservationFilter struct {
	TenantID uint64
	RunID    *string
	ModelID  string
	From, To *time.Time
}

// EmbeddingCacheAggregate is the additive local-cache fact returned alongside
// (never merged into) the ModelUsageAggregate prompt-cache fields.
type EmbeddingCacheAggregate struct {
	ImplementationStatus        EmbeddingCacheImplementationStatus `json:"implementation_status"`
	BatchInvocationCount        int64                              `json:"batch_invocation_count"`
	LogicalItemCount            int64                              `json:"logical_item_count"`
	HitCount                    int64                              `json:"hit_count"`
	MissCount                   int64                              `json:"miss_count"`
	BypassCount                 int64                              `json:"bypass_count"`
	LookupFailedCount           int64                              `json:"lookup_failed_count"`
	CorruptionCount             int64                              `json:"corruption_count"`
	WriteFailedCount            int64                              `json:"write_failed_count"`
	ProviderBoundModelCallCount int64                              `json:"provider_bound_model_call_count"`
	AttemptedCount              int64                              `json:"attempted_count"`
	PersistedCount              int64                              `json:"persisted_count"`
	FailedCount                 int64                              `json:"failed_count"`
	MeasurementStatus           MeasurementHealthStatus            `json:"measurement_status"`
}

// EmbeddingCacheLookup is the result of a validated cache read.
type EmbeddingCacheLookup struct {
	Entry *EmbeddingCacheEntry
	// Vector is the decoded, validated vector payload of Entry. It is non-nil
	// exactly when Entry is non-nil (i.e. a valid HIT). The decorator consumes
	// this directly instead of re-decoding the payload.
	Vector []float32
	// Corrupt reports that a row existed but failed validation and was rejected.
	Corrupt bool
}

// EmbeddingCacheStore is the narrow read/write dependency accepted by the cache
// decorator. Implementations live in the repository layer; the embedding package
// depends only on this interface to avoid an import cycle.
type EmbeddingCacheStore interface {
	GetValidEntry(ctx context.Context, tenantID uint64, cacheKey string, expected EmbeddingCacheIdentity) (EmbeddingCacheLookup, error)
	PutValidatedEntry(ctx context.Context, entry *EmbeddingCacheEntry) error
}

// EmbeddingCacheObserver is the narrow observation lifecycle dependency accepted
// by the cache decorator.
type EmbeddingCacheObserver interface {
	BeginObservation(ctx context.Context, obs *EmbeddingCacheObservation) error
	FinalizeObservation(ctx context.Context, tenantID uint64, obsID string, final EmbeddingCacheObservationFinalize) error
	// RecordFailedObservation writes a best-effort FAILED observation when a
	// begin failed so the invocation is not silently dropped from the
	// measurement window (it must count toward PARTIAL, never COMPLETE).
	RecordFailedObservation(ctx context.Context, obs *EmbeddingCacheObservation, final EmbeddingCacheObservationFinalize) error
}
