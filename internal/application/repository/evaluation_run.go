package repository

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ErrEvaluationRunNotFound is returned when a tenant-scoped run lookup misses.
var ErrEvaluationRunNotFound = errors.New("evaluation run not found")

// evaluationRunRepository implements interfaces.EvaluationRunRepository.
type evaluationRunRepository struct {
	db *gorm.DB
}

// NewEvaluationRunRepository creates the evaluation run persistence adapter.
func NewEvaluationRunRepository(db *gorm.DB) interfaces.EvaluationRunRepository {
	return &evaluationRunRepository{db: db}
}

func (r *evaluationRunRepository) Create(ctx context.Context, run *types.EvaluationRun) error {
	return r.db.WithContext(ctx).Create(run).Error
}

func (r *evaluationRunRepository) GetByRunID(
	ctx context.Context, tenantID uint64, runID string,
) (*types.EvaluationRun, error) {
	var run types.EvaluationRun
	err := r.db.WithContext(ctx).
		Where("run_id = ? AND tenant_id = ?", runID, tenantID).
		First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEvaluationRunNotFound
	}
	return &run, err
}

func (r *evaluationRunRepository) GetByTaskID(
	ctx context.Context, tenantID uint64, taskID string,
) (*types.EvaluationRun, error) {
	var run types.EvaluationRun
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND (task_id = ? OR run_id = ?)", tenantID, taskID, taskID).
		First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEvaluationRunNotFound
	}
	return &run, err
}

func (r *evaluationRunRepository) Update(ctx context.Context, run *types.EvaluationRun) error {
	result := r.db.WithContext(ctx).
		Model(&types.EvaluationRun{}).
		Where("run_id = ? AND tenant_id = ?", run.RunID, run.TenantID).
		Select("*").
		Updates(map[string]interface{}{
			"task_id":                run.TaskID,
			"protocol_hash":          run.ProtocolHash,
			"protocol_snapshot":      run.ProtocolSnapshot,
			"run_provenance":         run.RunProvenance,
			"git_commit":             run.GitCommit,
			"app_version":            run.AppVersion,
			"status":                 run.Status,
			"interruption_reason":    run.InterruptionReason,
			"started_at":             run.StartedAt,
			"ended_at":               run.EndedAt,
			"total_count":            run.TotalCount,
			"processed_count":        run.ProcessedCount,
			"finished_count":         run.FinishedCount,
			"metrics_json":           run.MetricsJSON,
			"metrics_valid":          run.MetricsValid,
			"error_type":             run.ErrorType,
			"error_message":          run.ErrorMessage,
			"temporary_resource_key": run.TemporaryResourceKey,
			"temporary_kb_id":        run.TemporaryKBID,
			"cleanup_status":         run.CleanupStatus,
			"measurement_status":     run.MeasurementStatus,
			"persistence_status":     run.PersistenceStatus,
			"updated_at":             run.UpdatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	// A zero-row update means the row does not exist (or was soft-deleted). It
	// must surface as an error rather than a silent success, otherwise callers
	// could "persist" a terminal state against a row that is already gone.
	if result.RowsAffected == 0 {
		return ErrEvaluationRunNotFound
	}
	return nil
}

func (r *evaluationRunRepository) ListByStatus(
	ctx context.Context, status types.EvaluationRunStatus,
) ([]*types.EvaluationRun, error) {
	var runs []*types.EvaluationRun
	if err := r.db.WithContext(ctx).
		Where("status = ?", status).
		Order("created_at ASC").
		Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
}

// ListReconciliationCandidates returns the runs a single-worker startup must
// reconcile:
//
//	status IN (RUNNING, PENDING)              — owner died before a terminal state
//	cleanup_status IN (CREATING, CREATED, FAILED) — temp resource may still exist
//	persistence_status = PERSIST_FAILED       — a durable write was known to fail
//
// This covers every terminal state × incomplete-cleanup combination (including
// COMPLETED + CREATED, the crash window between the terminal write and the
// deferred cleanup). Runs with complete cleanup (DONE / DELETE_REQUESTED) and a
// clean persistence status are excluded so reconciliation stays idempotent and
// never silently rewrites a finished run.
func (r *evaluationRunRepository) ListReconciliationCandidates(
	ctx context.Context,
) ([]*types.EvaluationRun, error) {
	var runs []*types.EvaluationRun
	if err := r.db.WithContext(ctx).
		Where("(status IN (?, ?)) OR (cleanup_status IN (?, ?, ?)) OR (persistence_status = ?)",
			types.EvaluationRunStatusRunning, types.EvaluationRunStatusPending,
			types.CleanupStatusCreating, types.CleanupStatusCreated, types.CleanupStatusFailed,
			types.PersistenceStatusPersistFailed,
		).
		Order("created_at ASC").
		Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
}
