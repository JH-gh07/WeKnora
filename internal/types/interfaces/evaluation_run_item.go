package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// EvaluationRunItemRepository persists the Run -> Item -> Attempt durable truth
// model (Task016 Step 6). Every single-item read/write is tenant-scoped (I12);
// the tenant is taken from the authenticated context, never from a query/body.
type EvaluationRunItemRepository interface {
	// CreateItem creates a PENDING item. A duplicate (tenant_id, run_id, item_id)
	// returns ErrEvaluationItemDuplicate without overwriting the existing fact.
	CreateItem(ctx context.Context, item *types.EvaluationRunItem) error
	// CommitTerminalItem transitions a non-terminal item into a terminal state
	// exactly once. A second commit returns ErrEvaluationItemAlreadyTerminal and
	// never overwrites the first committed result (I07).
	CommitTerminalItem(
		ctx context.Context,
		tenantID uint64, runID, itemID, ownerID string, fencingToken int64,
		status types.EvaluationItemStatus,
		resultJSON types.JSON, resultArtifactHash, terminalReason string,
	) error
	// ClaimNextItem atomically claims one eligible item using database time,
	// increments its fencing token and appends a RUNNING attempt fact.
	ClaimNextItem(ctx context.Context, tenantID uint64, runID, ownerID string, leaseTTL time.Duration) (*types.EvaluationRunItem, error)
	// HeartbeatItem extends a live lease only for its current owner/token.
	HeartbeatItem(ctx context.Context, tenantID uint64, runID, itemID, ownerID string, fencingToken int64, leaseTTL time.Duration) error
	// GetItem returns a single item scoped to the tenant.
	GetItem(ctx context.Context, tenantID uint64, runID, itemID string) (*types.EvaluationRunItem, error)
	// ListItems returns every item of a run (terminal and non-terminal), ordered
	// by item_id, for aggregate recomputation.
	ListItems(ctx context.Context, tenantID uint64, runID string) ([]*types.EvaluationRunItem, error)
	// CreateAttempt appends one attempt record. A duplicate (tenant_id, run_id,
	// item_id, attempt_no) returns ErrEvaluationItemDuplicate.
	CreateAttempt(ctx context.Context, attempt *types.EvaluationItemAttempt) error
	// ListAttempts returns every attempt of one item, ordered by attempt_no.
	ListAttempts(ctx context.Context, tenantID uint64, runID, itemID string) ([]*types.EvaluationItemAttempt, error)
}
