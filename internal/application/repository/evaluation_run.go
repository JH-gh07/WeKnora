package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ErrEvaluationRunNotFound is returned when a tenant-scoped run lookup misses.
var ErrEvaluationRunNotFound = errors.New("evaluation run not found")
var ErrEvaluationRunFenced = errors.New("evaluation run owner or fencing token is stale")

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
			"task_id":                             run.TaskID,
			"protocol_hash":                       run.ProtocolHash,
			"protocol_snapshot":                   run.ProtocolSnapshot,
			"run_provenance":                      run.RunProvenance,
			"git_commit":                          run.GitCommit,
			"app_version":                         run.AppVersion,
			"status":                              run.Status,
			"interruption_reason":                 run.InterruptionReason,
			"started_at":                          run.StartedAt,
			"ended_at":                            run.EndedAt,
			"total_count":                         run.TotalCount,
			"processed_count":                     run.ProcessedCount,
			"finished_count":                      run.FinishedCount,
			"metrics_json":                        run.MetricsJSON,
			"metrics_valid":                       run.MetricsValid,
			"error_type":                          run.ErrorType,
			"error_message":                       run.ErrorMessage,
			"temporary_resource_key":              run.TemporaryResourceKey,
			"temporary_kb_id":                     run.TemporaryKBID,
			"cleanup_status":                      run.CleanupStatus,
			"cleanup_owner_id":                    run.CleanupOwnerID,
			"cleanup_lease_until":                 run.CleanupLeaseUntil,
			"cleanup_fencing_token":               run.CleanupFencingToken,
			"measurement_status":                  run.MeasurementStatus,
			"expected_logical_calls":              run.ExpectedLogicalCalls,
			"metering_attempted_count":            run.MeteringAttemptedCount,
			"metering_persisted_count":            run.MeteringPersistedCount,
			"metering_failed_count":               run.MeteringFailedCount,
			"unobservable_provider_attempt_count": run.UnobservableProviderAttemptCount,
			"persistence_status":                  run.PersistenceStatus,
			"updated_at":                          run.UpdatedAt,
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
	activeItemLease := "i.lease_until > CURRENT_TIMESTAMP"
	activeCleanupLease := "cleanup_lease_until > CURRENT_TIMESTAMP"
	if r.db.Dialector.Name() != "postgres" {
		activeItemLease = "julianday(i.lease_until) > julianday(CURRENT_TIMESTAMP)"
		activeCleanupLease = "julianday(cleanup_lease_until) > julianday(CURRENT_TIMESTAMP)"
	}
	if err := r.db.WithContext(ctx).
		Where("(status IN (?, ?)) OR (cleanup_status IN (?, ?, ?)) OR (persistence_status = ?)",
			types.EvaluationRunStatusRunning, types.EvaluationRunStatusPending,
			types.CleanupStatusCreating, types.CleanupStatusCreated, types.CleanupStatusFailed,
			types.PersistenceStatusPersistFailed,
		).
		Where("cleanup_lease_until IS NULL OR NOT ("+activeCleanupLease+")").
		Where("NOT EXISTS (SELECT 1 FROM evaluation_run_items i WHERE i.tenant_id = evaluation_runs.tenant_id AND i.run_id = evaluation_runs.run_id AND i.status = ? AND "+activeItemLease+")", types.EvaluationItemStatusRunning).
		Order("created_at ASC").
		Find(&runs).Error; err != nil {
		return nil, err
	}
	return runs, nil
}

func (r *evaluationRunRepository) ClaimCleanup(ctx context.Context, tenantID uint64, runID, ownerID string, leaseTTL time.Duration) (int64, error) {
	if tenantID == 0 || runID == "" || ownerID == "" || leaseTTL <= 0 {
		return 0, ErrEvaluationRunFenced
	}
	var token int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&types.EvaluationRun{}).
			Where("tenant_id = ? AND run_id = ? AND (cleanup_lease_until IS NULL OR "+leaseExpiredPredicateColumn(tx, "cleanup_lease_until")+")", tenantID, runID).
			Updates(map[string]interface{}{
				"cleanup_owner_id": ownerID, "cleanup_lease_until": leaseUntilExpr(tx, leaseTTL),
				"cleanup_fencing_token": gorm.Expr("cleanup_fencing_token + 1"),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrEvaluationRunFenced
		}
		return tx.Model(&types.EvaluationRun{}).Select("cleanup_fencing_token").Where("tenant_id = ? AND run_id = ?", tenantID, runID).Scan(&token).Error
	})
	return token, err
}

func (r *evaluationRunRepository) UpdateCleanupStatusFenced(ctx context.Context, tenantID uint64, runID, ownerID string, fencingToken int64, status types.CleanupStatus, temporaryKBID string) error {
	if tenantID == 0 || runID == "" || ownerID == "" || fencingToken <= 0 {
		return ErrEvaluationRunFenced
	}
	res := r.db.WithContext(ctx).Model(&types.EvaluationRun{}).
		Where("tenant_id = ? AND run_id = ? AND cleanup_owner_id = ? AND cleanup_fencing_token = ? AND "+leaseActivePredicateColumn(r.db, "cleanup_lease_until"), tenantID, runID, ownerID, fencingToken).
		Updates(map[string]interface{}{
			"cleanup_status": status, "temporary_kb_id": temporaryKBID,
			"cleanup_owner_id": "", "cleanup_lease_until": nil,
			"persistence_status": types.PersistenceStatusPersisted,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrEvaluationRunFenced
	}
	return nil
}

func leaseExpiredPredicateColumn(db *gorm.DB, column string) string {
	if db.Dialector.Name() == "postgres" {
		return column + " <= CURRENT_TIMESTAMP"
	}
	return "julianday(" + column + ") <= julianday(CURRENT_TIMESTAMP)"
}

func leaseActivePredicateColumn(db *gorm.DB, column string) string {
	if db.Dialector.Name() == "postgres" {
		return column + " > CURRENT_TIMESTAMP"
	}
	return "julianday(" + column + ") > julianday(CURRENT_TIMESTAMP)"
}
