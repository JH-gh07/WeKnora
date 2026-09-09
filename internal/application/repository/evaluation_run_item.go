package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Item/attempt repository errors. These are typed so callers can distinguish
// "already committed" (a conflict to be honored, I07) from "not found" (I12)
// and "duplicate" (an attempt number collision).
var (
	ErrEvaluationItemNotFound         = errors.New("evaluation item not found")
	ErrEvaluationItemAlreadyTerminal  = errors.New("evaluation item already terminal")
	ErrEvaluationItemDuplicate        = errors.New("evaluation item already exists")
	ErrEvaluationItemNotTerminalState = errors.New("status is not a terminal item state")
	ErrEvaluationItemInvalid          = errors.New("evaluation item is invalid")
	ErrEvaluationAttemptInvalid       = errors.New("evaluation item attempt is invalid")
	ErrEvaluationItemFenced           = errors.New("evaluation item owner or fencing token is stale")
	ErrEvaluationItemNoClaimable      = errors.New("no claimable evaluation item")
)

// evaluationRunItemRepository implements interfaces.EvaluationRunItemRepository.
type evaluationRunItemRepository struct {
	db *gorm.DB
}

// NewEvaluationRunItemRepository creates the item/attempt persistence adapter.
func NewEvaluationRunItemRepository(db *gorm.DB) interfaces.EvaluationRunItemRepository {
	return &evaluationRunItemRepository{db: db}
}

func (r *evaluationRunItemRepository) CreateItem(ctx context.Context, item *types.EvaluationRunItem) error {
	if item == nil || item.TenantID == 0 || strings.TrimSpace(item.RunID) == "" || strings.TrimSpace(item.ItemID) == "" || item.Status != types.EvaluationItemStatusPending {
		return ErrEvaluationItemInvalid
	}
	res := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(item)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrEvaluationItemDuplicate
	}
	return nil
}

func (r *evaluationRunItemRepository) CommitTerminalItem(
	ctx context.Context,
	tenantID uint64, runID, itemID, ownerID string, fencingToken int64,
	status types.EvaluationItemStatus,
	resultJSON types.JSON, resultArtifactHash, terminalReason string,
) error {
	if !status.IsTerminal() {
		return ErrEvaluationItemNotTerminalState
	}
	if tenantID == 0 || strings.TrimSpace(runID) == "" || strings.TrimSpace(itemID) == "" || strings.TrimSpace(ownerID) == "" || fencingToken <= 0 {
		return ErrEvaluationItemInvalid
	}
	canonical, err := canonicalJSONBytes(resultJSON)
	if err != nil {
		return fmt.Errorf("%w: result_json: %v", ErrEvaluationItemInvalid, err)
	}
	attemptStatus := types.EvaluationAttemptStatusSucceeded
	switch status {
	case types.EvaluationItemStatusFailedTerm:
		attemptStatus = types.EvaluationAttemptStatusFailedTerm
	case types.EvaluationItemStatusCancelled:
		attemptStatus = types.EvaluationAttemptStatusCancelled
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt := tx.Model(&types.EvaluationItemAttempt{}).
			Where("tenant_id = ? AND run_id = ? AND item_id = ? AND owner_id = ? AND fencing_token = ? AND status = ?",
				tenantID, runID, itemID, ownerID, fencingToken, types.EvaluationAttemptStatusRunning).
			Updates(map[string]interface{}{
				"status":               attemptStatus,
				"reason":               terminalReason,
				"result_artifact_hash": resultArtifactHash,
				"result_json":          types.JSON(canonical),
				"ended_at":             gorm.Expr("CURRENT_TIMESTAMP"),
			})
		if attempt.Error != nil {
			return attempt.Error
		}
		if attempt.RowsAffected != 1 {
			return r.classifyCommitMiss(ctx, tx, tenantID, runID, itemID)
		}
		item := tx.Model(&types.EvaluationRunItem{}).
			Where("tenant_id = ? AND run_id = ? AND item_id = ? AND owner_id = ? AND fencing_token = ? AND status = ? AND "+leaseActivePredicate(tx),
				tenantID, runID, itemID, ownerID, fencingToken, types.EvaluationItemStatusRunning).
			Updates(map[string]interface{}{
				"status":               status,
				"terminal_reason":      terminalReason,
				"result_artifact_hash": resultArtifactHash,
				"result_json":          types.JSON(canonical),
				"lease_until":          nil,
			})
		if item.Error != nil {
			return item.Error
		}
		if item.RowsAffected != 1 {
			return r.classifyCommitMiss(ctx, tx, tenantID, runID, itemID)
		}
		return nil
	})
	return err
}

func (r *evaluationRunItemRepository) classifyCommitMiss(ctx context.Context, db *gorm.DB, tenantID uint64, runID, itemID string) error {
	var item types.EvaluationRunItem
	err := db.WithContext(ctx).Where("tenant_id = ? AND run_id = ? AND item_id = ?", tenantID, runID, itemID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrEvaluationItemNotFound
	}
	if err != nil {
		return err
	}
	if item.IsTerminal() {
		return ErrEvaluationItemAlreadyTerminal
	}
	return ErrEvaluationItemFenced
}

func (r *evaluationRunItemRepository) ClaimNextItem(ctx context.Context, tenantID uint64, runID, ownerID string, leaseTTL time.Duration) (*types.EvaluationRunItem, error) {
	if tenantID == 0 || strings.TrimSpace(runID) == "" || strings.TrimSpace(ownerID) == "" || leaseTTL <= 0 {
		return nil, ErrEvaluationItemInvalid
	}
	var claimed types.EvaluationRunItem
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Where("tenant_id = ? AND run_id = ? AND ((status IN (?, ?, ?)) OR (status = ? AND "+leaseExpiredPredicate(tx)+"))",
			tenantID, runID,
			types.EvaluationItemStatusPending, types.EvaluationItemStatusRetryWait, types.EvaluationItemStatusReclaimable,
			types.EvaluationItemStatusRunning,
		).Order("item_id ASC")
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}
		var candidate types.EvaluationRunItem
		if err := query.First(&candidate).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEvaluationItemNoClaimable
		} else if err != nil {
			return err
		}
		if candidate.Status == types.EvaluationItemStatusRunning {
			if err := tx.Model(&types.EvaluationItemAttempt{}).
				Where("tenant_id = ? AND run_id = ? AND item_id = ? AND fencing_token = ? AND status = ?", tenantID, runID, candidate.ItemID, candidate.FencingToken, types.EvaluationAttemptStatusRunning).
				Updates(map[string]interface{}{"status": types.EvaluationAttemptStatusRetryWait, "reason": "LEASE_EXPIRED", "ended_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error; err != nil {
				return err
			}
		}
		leaseExpr := leaseUntilExpr(tx, leaseTTL)
		res := tx.Model(&types.EvaluationRunItem{}).
			Where("tenant_id = ? AND run_id = ? AND item_id = ? AND fencing_token = ?", tenantID, runID, candidate.ItemID, candidate.FencingToken).
			Updates(map[string]interface{}{
				"status": types.EvaluationItemStatusRunning, "owner_id": ownerID,
				"lease_until": leaseExpr, "fencing_token": gorm.Expr("fencing_token + 1"),
				"attempt_count": gorm.Expr("attempt_count + 1"),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrEvaluationItemFenced
		}
		if err := tx.Where("tenant_id = ? AND run_id = ? AND item_id = ?", tenantID, runID, candidate.ItemID).First(&claimed).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO evaluation_item_attempts
			(tenant_id, run_id, item_id, attempt_no, owner_id, fencing_token, lease_until, status, started_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
			claimed.TenantID, claimed.RunID, claimed.ItemID, claimed.AttemptCount, claimed.OwnerID, claimed.FencingToken, claimed.LeaseUntil, types.EvaluationAttemptStatusRunning).Error
	})
	if err != nil {
		return nil, err
	}
	return &claimed, nil
}

func (r *evaluationRunItemRepository) HeartbeatItem(ctx context.Context, tenantID uint64, runID, itemID, ownerID string, fencingToken int64, leaseTTL time.Duration) error {
	if tenantID == 0 || strings.TrimSpace(runID) == "" || strings.TrimSpace(itemID) == "" || strings.TrimSpace(ownerID) == "" || fencingToken <= 0 || leaseTTL <= 0 {
		return ErrEvaluationItemInvalid
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		leaseExpr := leaseUntilExpr(tx, leaseTTL)
		item := tx.Model(&types.EvaluationRunItem{}).
			Where("tenant_id = ? AND run_id = ? AND item_id = ? AND owner_id = ? AND fencing_token = ? AND status = ? AND "+leaseActivePredicate(tx), tenantID, runID, itemID, ownerID, fencingToken, types.EvaluationItemStatusRunning).
			Update("lease_until", leaseExpr)
		if item.Error != nil {
			return item.Error
		}
		if item.RowsAffected != 1 {
			return ErrEvaluationItemFenced
		}
		attempt := tx.Model(&types.EvaluationItemAttempt{}).
			Where("tenant_id = ? AND run_id = ? AND item_id = ? AND owner_id = ? AND fencing_token = ? AND status = ?", tenantID, runID, itemID, ownerID, fencingToken, types.EvaluationAttemptStatusRunning).
			Update("lease_until", leaseExpr)
		if attempt.Error != nil {
			return attempt.Error
		}
		if attempt.RowsAffected != 1 {
			return ErrEvaluationItemFenced
		}
		return nil
	})
}

func leaseUntilExpr(db *gorm.DB, ttl time.Duration) clause.Expr {
	seconds := int64(ttl / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	if db.Dialector.Name() == "postgres" {
		return gorm.Expr("CURRENT_TIMESTAMP + (? * INTERVAL '1 second')", seconds)
	}
	return gorm.Expr("datetime(CURRENT_TIMESTAMP, '+' || ? || ' seconds')", seconds)
}

func leaseExpiredPredicate(db *gorm.DB) string {
	if db.Dialector.Name() == "postgres" {
		return "lease_until <= CURRENT_TIMESTAMP"
	}
	return "julianday(lease_until) <= julianday(CURRENT_TIMESTAMP)"
}

func leaseActivePredicate(db *gorm.DB) string {
	if db.Dialector.Name() == "postgres" {
		return "lease_until > CURRENT_TIMESTAMP"
	}
	return "julianday(lease_until) > julianday(CURRENT_TIMESTAMP)"
}

func canonicalJSONBytes(raw []byte) ([]byte, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return nil, errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return json.Marshal(value)
}

func (r *evaluationRunItemRepository) GetItem(
	ctx context.Context, tenantID uint64, runID, itemID string,
) (*types.EvaluationRunItem, error) {
	var item types.EvaluationRunItem
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND run_id = ? AND item_id = ?", tenantID, runID, itemID).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEvaluationItemNotFound
	}
	return &item, err
}

func (r *evaluationRunItemRepository) ListItems(
	ctx context.Context, tenantID uint64, runID string,
) ([]*types.EvaluationRunItem, error) {
	var items []*types.EvaluationRunItem
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND run_id = ?", tenantID, runID).
		Order("item_id ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *evaluationRunItemRepository) CreateAttempt(ctx context.Context, attempt *types.EvaluationItemAttempt) error {
	if attempt == nil || attempt.TenantID == 0 || strings.TrimSpace(attempt.RunID) == "" || strings.TrimSpace(attempt.ItemID) == "" || attempt.AttemptNo < 1 || attempt.Status != types.EvaluationAttemptStatusRunning {
		return ErrEvaluationAttemptInvalid
	}
	var parent types.EvaluationRunItem
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND run_id = ? AND item_id = ?", attempt.TenantID, attempt.RunID, attempt.ItemID).First(&parent).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrEvaluationItemNotFound
	} else if err != nil {
		return err
	}
	var existing int64
	if err := r.db.WithContext(ctx).Model(&types.EvaluationItemAttempt{}).
		Where("tenant_id = ? AND run_id = ? AND item_id = ? AND attempt_no = ?", attempt.TenantID, attempt.RunID, attempt.ItemID, attempt.AttemptNo).
		Count(&existing).Error; err != nil {
		return err
	}
	if existing != 0 {
		return ErrEvaluationItemDuplicate
	}
	var maxAttempt int
	if err := r.db.WithContext(ctx).Model(&types.EvaluationItemAttempt{}).
		Select("COALESCE(MAX(attempt_no), 0)").
		Where("tenant_id = ? AND run_id = ? AND item_id = ?", attempt.TenantID, attempt.RunID, attempt.ItemID).
		Scan(&maxAttempt).Error; err != nil {
		return err
	}
	if attempt.AttemptNo != maxAttempt+1 {
		return ErrEvaluationAttemptInvalid
	}
	res := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(attempt)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrEvaluationItemDuplicate
	}
	return nil
}

func (r *evaluationRunItemRepository) ListAttempts(
	ctx context.Context, tenantID uint64, runID, itemID string,
) ([]*types.EvaluationItemAttempt, error) {
	var attempts []*types.EvaluationItemAttempt
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND run_id = ? AND item_id = ?", tenantID, runID, itemID).
		Order("attempt_no ASC").
		Find(&attempts).Error; err != nil {
		return nil, err
	}
	return attempts, nil
}
