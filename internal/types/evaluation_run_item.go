package types

import "time"

// EvaluationItemStatus is the lifecycle state of one logical evaluation item
// (Task016 Step 6, §3.3). It is a durable fact, never an in-memory accumulator.
type EvaluationItemStatus string

const (
	EvaluationItemStatusPending      EvaluationItemStatus = "PENDING"
	EvaluationItemStatusRunning      EvaluationItemStatus = "RUNNING"
	EvaluationItemStatusSucceeded    EvaluationItemStatus = "SUCCEEDED"
	EvaluationItemStatusFailedTerm   EvaluationItemStatus = "FAILED_TERMINAL"
	EvaluationItemStatusRetryWait    EvaluationItemStatus = "RETRY_WAIT"
	EvaluationItemStatusReclaimable  EvaluationItemStatus = "RECLAIMABLE"
	EvaluationItemStatusCancelled    EvaluationItemStatus = "CANCELLED"
)

// IsTerminal reports whether the item will never transition again. SUCCEEDED,
// FAILED_TERMINAL and CANCELLED are terminal; PENDING/RUNNING/RETRY_WAIT/
// RECLAIMABLE are still open work.
func (s EvaluationItemStatus) IsTerminal() bool {
	switch s {
	case EvaluationItemStatusSucceeded, EvaluationItemStatusFailedTerm, EvaluationItemStatusCancelled:
		return true
	default:
		return false
	}
}

// EvaluationAttemptStatus is the lifecycle state of one attempt of an item.
type EvaluationAttemptStatus string

const (
	EvaluationAttemptStatusRunning       EvaluationAttemptStatus = "RUNNING"
	EvaluationAttemptStatusSucceeded     EvaluationAttemptStatus = "SUCCEEDED"
	EvaluationAttemptStatusFailedTerm    EvaluationAttemptStatus = "FAILED_TERMINAL"
	EvaluationAttemptStatusRetryWait     EvaluationAttemptStatus = "RETRY_WAIT"
	EvaluationAttemptStatusCancelled     EvaluationAttemptStatus = "CANCELLED"
)

// EvaluationRunItem is the durable fact of one logical evaluation item: the
// smallest unit of work a run is decomposed into (one QA pair). Its terminal
// result is protected by PRIMARY KEY (tenant_id, run_id, item_id), so at most
// one committed terminal result exists per item (I07).
type EvaluationRunItem struct {
	TenantID           uint64               `json:"tenant_id" gorm:"column:tenant_id;primaryKey"`
	RunID              string               `json:"run_id" gorm:"column:run_id;type:varchar(36);primaryKey"`
	ItemID             string               `json:"item_id" gorm:"column:item_id;type:varchar(36);primaryKey"`
	Status             EvaluationItemStatus `json:"status" gorm:"column:status;type:varchar(24)"`
	OwnerID            string               `json:"owner_id" gorm:"column:owner_id;type:varchar(64)"`
	LeaseUntil         *time.Time           `json:"lease_until" gorm:"column:lease_until"`
	FencingToken       int64                `json:"fencing_token" gorm:"column:fencing_token"`
	AttemptCount       int                  `json:"attempt_count" gorm:"column:attempt_count"`
	TerminalReason     string               `json:"terminal_reason" gorm:"column:terminal_reason;type:varchar(64)"`
	ResultArtifactHash string               `json:"result_artifact_hash" gorm:"column:result_artifact_hash;type:varchar(64)"`
	ResultJSON         JSON                 `json:"result_json" gorm:"column:result_json"`
	CreatedAt          time.Time            `json:"created_at" gorm:"column:created_at;autoCreateTime"`
	UpdatedAt          time.Time            `json:"updated_at" gorm:"column:updated_at;autoUpdateTime"`
}

func (EvaluationRunItem) TableName() string { return "evaluation_run_items" }

// IsTerminal reports whether the item is in a terminal state.
func (i *EvaluationRunItem) IsTerminal() bool { return i.Status.IsTerminal() }

// IsFailedTerminal reports whether the item ended in a terminal failure (the
// only terminal kind that counts toward PARTIAL/FAILED aggregation).
func (i *EvaluationRunItem) IsFailedTerminal() bool {
	return i.Status == EvaluationItemStatusFailedTerm
}

// EvaluationItemAttempt is one claim + execution of an item. attempt_no is the
// per-item monotonic attempt number; the composite key (tenant_id, run_id,
// item_id, attempt_no) guarantees each attempt is recorded exactly once (I08).
type EvaluationItemAttempt struct {
	TenantID           uint64                  `json:"tenant_id" gorm:"column:tenant_id;primaryKey"`
	RunID              string                  `json:"run_id" gorm:"column:run_id;type:varchar(36);primaryKey"`
	ItemID             string                  `json:"item_id" gorm:"column:item_id;type:varchar(36);primaryKey"`
	AttemptNo          int                     `json:"attempt_no" gorm:"column:attempt_no;primaryKey"`
	OwnerID            string                  `json:"owner_id" gorm:"column:owner_id;type:varchar(64)"`
	FencingToken       int64                   `json:"fencing_token" gorm:"column:fencing_token"`
	LeaseUntil         *time.Time              `json:"lease_until" gorm:"column:lease_until"`
	Status             EvaluationAttemptStatus `json:"status" gorm:"column:status;type:varchar(24)"`
	Reason             string                  `json:"reason" gorm:"column:reason;type:varchar(64)"`
	ResultArtifactHash string                  `json:"result_artifact_hash" gorm:"column:result_artifact_hash;type:varchar(64)"`
	ResultJSON         JSON                    `json:"result_json" gorm:"column:result_json"`
	StartedAt          *time.Time              `json:"started_at" gorm:"column:started_at"`
	EndedAt            *time.Time              `json:"ended_at" gorm:"column:ended_at"`
	CreatedAt          time.Time               `json:"created_at" gorm:"column:created_at;autoCreateTime"`
	UpdatedAt          time.Time               `json:"updated_at" gorm:"column:updated_at;autoUpdateTime"`
}

func (EvaluationItemAttempt) TableName() string { return "evaluation_item_attempts" }
