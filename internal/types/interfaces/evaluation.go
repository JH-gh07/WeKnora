package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// EvaluationService defines operations for evaluation tasks
type EvaluationService interface {
	// Evaluation starts a new evaluation task
	Evaluation(ctx context.Context, datasetID string, knowledgeBaseID string,
		chatModelID string, rerankModelID string,
	) (*types.EvaluationDetail, error)
	// EvaluationResult retrieves evaluation result by task ID
	EvaluationResult(ctx context.Context, taskID string) (*types.EvaluationDetail, error)
	// ReconcileInterruptedRuns marks persisted RUNNING runs as INTERRUPTED on
	// startup (current single-worker baseline) and discovers/cleans temporary
	// resources via their resource key. It returns the number of runs reconciled.
	ReconcileInterruptedRuns(ctx context.Context) (int, error)
}

// Metrics defines interface for computing evaluation metrics
type Metrics interface {
	// Compute calculates metric score based on input data
	Compute(metricInput *types.MetricInput) float64
}

// EvalHook defines interface for evaluation process hooks
type EvalHook interface {
	// Handle processes evaluation state change
	Handle(ctx context.Context, state types.EvalState, index int, data interface{}) error
}

// DatasetService defines operations for dataset management
type DatasetService interface {
	// GetDatasetByID retrieves QA pairs from dataset by ID
	GetDatasetByID(ctx context.Context, datasetID string) ([]*types.QAPair, error)
}

// TemporaryKnowledgeBaseFinder discovers a temporary knowledge base by its
// deterministic resource locator (stored in the KB Description) within a
// tenant. It is a narrow, dedicated interface so reconciliation can recover a
// KB whose actual ID was not yet persisted before a crash, without widening the
// heavily-mocked KnowledgeBaseRepository interface.
type TemporaryKnowledgeBaseFinder interface {
	GetTemporaryKnowledgeBaseByResourceKey(ctx context.Context, tenantID uint64, resourceKey string) (*types.KnowledgeBase, error)
}

// EvaluationRunRepository persists the durable fact of an evaluation run.
// Every method that reads a single run is tenant-scoped; reconciliation uses
// ListByStatus which intentionally spans tenants because the current baseline
// is a single worker that owns every run.
type EvaluationRunRepository interface {
	// Create persists a new run.
	Create(ctx context.Context, run *types.EvaluationRun) error
	// GetByRunID returns a run by its stable run_id, scoped to the tenant.
	GetByRunID(ctx context.Context, tenantID uint64, runID string) (*types.EvaluationRun, error)
	// GetByTaskID returns a run by its legacy task_id (or run_id) for the tenant.
	GetByTaskID(ctx context.Context, tenantID uint64, taskID string) (*types.EvaluationRun, error)
	// Update persists changes to an existing run keyed by run_id + tenant.
	Update(ctx context.Context, run *types.EvaluationRun) error
	// ListByStatus lists runs in the given lifecycle state across tenants.
	ListByStatus(ctx context.Context, status types.EvaluationRunStatus) ([]*types.EvaluationRun, error)
	// ListReconciliationCandidates lists the runs a single-worker startup must
	// reconcile: RUNNING runs (owner died mid-run) and PENDING/FAILED runs whose
	// temporary resource cleanup never completed. It spans tenants because the
	// current baseline is a single worker that owns every run.
	ListReconciliationCandidates(ctx context.Context) ([]*types.EvaluationRun, error)
}
