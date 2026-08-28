package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// EvaluationRunStatus is the lifecycle state of a persisted evaluation run.
type EvaluationRunStatus string

const (
	EvaluationRunStatusPending     EvaluationRunStatus = "PENDING"
	EvaluationRunStatusRunning     EvaluationRunStatus = "RUNNING"
	EvaluationRunStatusCompleted   EvaluationRunStatus = "COMPLETED"
	EvaluationRunStatusFailed      EvaluationRunStatus = "FAILED"
	EvaluationRunStatusInterrupted EvaluationRunStatus = "INTERRUPTED"
)

// Interruption reasons. These are reasons, not states: a RUNNING run whose
// owner process died is marked INTERRUPTED with one of these reasons.
const (
	InterruptionReasonProcessLost   = "PROCESS_LOST"
	InterruptionReasonServerRestart = "SERVER_RESTART"
	InterruptionReasonOwnerExpired  = "OWNER_EXPIRED"
)

// CleanupStatus tracks the lifecycle of the temporary knowledge base.
//
// Semantics (honest about the async physical cleanup):
//   - CREATING / CREATED: the temporary KB may exist and has not been cleaned.
//   - DELETE_REQUESTED: the logical delete (soft-delete + async cleanup task
//     enqueue) was accepted; physical vector/knowledge/chunk removal is
//     asynchronous and NOT confirmed by this value.
//   - DONE: nothing left to clean (no resource existed) — not used to claim
//     physical cleanup completion, which this Task cannot observe synchronously.
//   - FAILED: the delete request itself errored.
type CleanupStatus string

const (
	CleanupStatusCreating        CleanupStatus = "CREATING"
	CleanupStatusCreated         CleanupStatus = "CREATED"
	CleanupStatusDeleteRequested CleanupStatus = "DELETE_REQUESTED"
	CleanupStatusDone            CleanupStatus = "DONE"
	CleanupStatusFailed          CleanupStatus = "FAILED"
)

// PersistenceStatus records whether the run's durable fact is complete. A
// lifecycle-critical write failure flips this to PERSIST_FAILED so the run can
// never be mistaken for fully persisted after a crash.
type PersistenceStatus string

const (
	PersistenceStatusPersisted     PersistenceStatus = "PERSISTED"
	PersistenceStatusPersistFailed PersistenceStatus = "PERSIST_FAILED"
)

// MeasurementStatus records how complete the metering observation is. This
// Task does not implement metering, so the honest initial value is UNKNOWN.
type MeasurementStatus string

const (
	MeasurementStatusUnknown  MeasurementStatus = "UNKNOWN"
	MeasurementStatusComplete MeasurementStatus = "COMPLETE"
	MeasurementStatusPartial  MeasurementStatus = "PARTIAL"
)

// EvaluationRun is the persisted fact of one logical evaluation execution.
// It is deliberately NOT a ModelCall, Provider Attempt or ItemResult table:
// COUNT(EvaluationRun) counts executions.
type EvaluationRun struct {
	// RunID is the stable run identity (primary key). It is allocated BEFORE
	// any temporary resource side effect so reconciliation can always refer
	// back to a durable identity.
	RunID string `json:"run_id" gorm:"column:run_id;type:varchar(36);primaryKey"`
	// TaskID is the legacy task identity (GenerateTaskID) kept so existing
	// clients querying ?task_id=... still resolve after restart.
	TaskID string `json:"task_id" gorm:"column:task_id;type:varchar(128);index"`
	// TenantID is the workspace that owns this run. All queries are scoped by it.
	TenantID uint64 `json:"tenant_id" gorm:"column:tenant_id;index"`

	// ProtocolHash is the comparison identity. It never includes git commit or
	// runtime/environment fields; it changes when the comparable protocol
	// (dataset, model ids, top_k, thresholds, prompt version, metric defs) changes.
	ProtocolHash string `json:"protocol_hash" gorm:"column:protocol_hash;type:varchar(64);index"`
	// ProtocolSnapshot is the immutable, secret-free, prompt-free snapshot the
	// hash is derived from. Prompt fields are stored as {version/hash/length}.
	ProtocolSnapshot JSON `json:"protocol_snapshot" gorm:"column:protocol_snapshot;type:jsonb"`

	// Provenance explains where a result came from. git_commit and environment
	// are provenance, NOT comparison identity.
	RunProvenance JSON   `json:"run_provenance" gorm:"column:run_provenance;type:jsonb"`
	GitCommit     string `json:"git_commit" gorm:"column:git_commit;type:varchar(64)"`
	AppVersion    string `json:"app_version" gorm:"column:app_version;type:varchar(32)"`

	// Lifecycle.
	Status             EvaluationRunStatus `json:"status" gorm:"column:status;type:varchar(16);index"`
	InterruptionReason string              `json:"interruption_reason" gorm:"column:interruption_reason;type:varchar(32)"`
	StartedAt          *time.Time          `json:"started_at" gorm:"column:started_at"`
	EndedAt            *time.Time          `json:"ended_at" gorm:"column:ended_at"`

	// Progress (best available; does not imply resumability).
	TotalCount     int `json:"total_count" gorm:"column:total_count"`
	ProcessedCount int `json:"processed_count" gorm:"column:processed_count"`
	FinishedCount  int `json:"finished_count" gorm:"column:finished_count"`

	// Metrics. metrics_valid=true only when aggregation is durably persisted.
	MetricsJSON  JSON `json:"metrics_json" gorm:"column:metrics_json;type:jsonb"`
	MetricsValid bool `json:"metrics_valid" gorm:"column:metrics_valid"`

	// Error (sanitized: no token/prompt).
	ErrorType    string `json:"error_type" gorm:"column:error_type;type:varchar(64)"`
	ErrorMessage string `json:"error_message" gorm:"column:error_message;type:text"`

	// Temporary resource locator + cleanup state.
	TemporaryResourceKey string        `json:"temporary_resource_key" gorm:"column:temporary_resource_key;type:varchar(128);index"`
	TemporaryKBID        string        `json:"temporary_kb_id" gorm:"column:temporary_kb_id;type:varchar(36)"`
	CleanupStatus        CleanupStatus `json:"cleanup_status" gorm:"column:cleanup_status;type:varchar(16)"`

	// Measurement boundary. UNKNOWN until a later Task implements metering.
	MeasurementStatus MeasurementStatus `json:"measurement_status" gorm:"column:measurement_status;type:varchar(16)"`

	// PersistenceStatus records whether all lifecycle-critical durable writes
	// succeeded. PERSIST_FAILED means the DB fact may be incomplete (a terminal
	// state, resource locator, or cleanup write failed) and must not be reported
	// as fully persisted.
	PersistenceStatus PersistenceStatus `json:"persistence_status" gorm:"column:persistence_status;type:varchar(16);default:PERSISTED"`

	CreatedAt time.Time      `json:"created_at" gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt gorm.DeletedAt `json:"deleted_at" gorm:"column:deleted_at;index"`
}

// TableName returns the evaluation_runs table name.
func (EvaluationRun) TableName() string { return "evaluation_runs" }

// BeforeCreate assigns the primary key before persistence.
func (r *EvaluationRun) BeforeCreate(_ *gorm.DB) error {
	if r.RunID == "" {
		r.RunID = uuid.NewString()
	}
	return nil
}

// IsTerminal reports whether the run is in a terminal state.
func (r *EvaluationRun) IsTerminal() bool {
	return r.Status == EvaluationRunStatusCompleted ||
		r.Status == EvaluationRunStatusFailed ||
		r.Status == EvaluationRunStatusInterrupted
}
