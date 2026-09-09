package repository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTestItemDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "items.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.EvaluationRunItem{}, &types.EvaluationItemAttempt{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func TestEvaluationRunItemRejectsInvalidFacts(t *testing.T) {
	repo := NewEvaluationRunItemRepository(newTestItemDB(t))
	ctx := context.Background()

	for name, input := range map[string]*types.EvaluationRunItem{
		"nil":           nil,
		"tenant zero":   newTestItem(0, "run-1", "item-1"),
		"empty run":     newTestItem(1, "", "item-1"),
		"empty item":    newTestItem(1, "run-1", ""),
		"wrong initial": {TenantID: 1, RunID: "run-1", ItemID: "item-1", Status: types.EvaluationItemStatusSucceeded},
	} {
		t.Run(name, func(t *testing.T) {
			if err := repo.CreateItem(ctx, input); !errors.Is(err, ErrEvaluationItemInvalid) {
				t.Fatalf("CreateItem() error = %v, want ErrEvaluationItemInvalid", err)
			}
		})
	}
}

func newTestItem(tenantID uint64, runID, itemID string) *types.EvaluationRunItem {
	return &types.EvaluationRunItem{
		TenantID: tenantID,
		RunID:    runID,
		ItemID:   itemID,
		Status:   types.EvaluationItemStatusPending,
	}
}

func TestEvaluationRunItemCreateAndDuplicate(t *testing.T) {
	repo := NewEvaluationRunItemRepository(newTestItemDB(t))
	ctx := context.Background()

	if err := repo.CreateItem(ctx, newTestItem(1, "run-1", "item-1")); err != nil {
		t.Fatalf("create item: %v", err)
	}
	// Duplicate (tenant_id, run_id, item_id) must conflict, not overwrite.
	if err := repo.CreateItem(ctx, newTestItem(1, "run-1", "item-1")); !errors.Is(err, ErrEvaluationItemDuplicate) {
		t.Fatalf("duplicate create = %v, want ErrEvaluationItemDuplicate", err)
	}
	// Same run_id/item_id in a DIFFERENT tenant is a distinct fact (I12).
	if err := repo.CreateItem(ctx, newTestItem(2, "run-1", "item-1")); err != nil {
		t.Fatalf("create same key in other tenant: %v", err)
	}
}

func TestEvaluationRunItemCommitTerminalExactlyOnce(t *testing.T) {
	repo := NewEvaluationRunItemRepository(newTestItemDB(t))
	ctx := context.Background()

	if err := repo.CreateItem(ctx, newTestItem(1, "run-1", "item-1")); err != nil {
		t.Fatalf("create item: %v", err)
	}

	// First commit succeeds.
	claimed, err := repo.ClaimNextItem(ctx, 1, "run-1", "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := repo.CommitTerminalItem(ctx, 1, "run-1", "item-1", "worker-a", claimed.FencingToken,
		types.EvaluationItemStatusSucceeded, types.JSON(`{"ok":1}`), "hash-a", ""); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// Second commit (a duplicate terminal) must conflict and NOT overwrite.
	if err := repo.CommitTerminalItem(ctx, 1, "run-1", "item-1", "worker-a", claimed.FencingToken,
		types.EvaluationItemStatusFailedTerm, types.JSON(`{"ok":0}`), "hash-b", "boom"); !errors.Is(err, ErrEvaluationItemAlreadyTerminal) {
		t.Fatalf("second commit = %v, want ErrEvaluationItemAlreadyTerminal", err)
	}

	got, err := repo.GetItem(ctx, 1, "run-1", "item-1")
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if got.Status != types.EvaluationItemStatusSucceeded {
		t.Fatalf("status overwritten to %s, want SUCCEEDED", got.Status)
	}
	if string(got.ResultJSON) != `{"ok":1}` {
		t.Fatalf("result overwritten to %s", got.ResultJSON)
	}
	if got.ResultArtifactHash != "hash-a" {
		t.Fatalf("artifact hash overwritten to %s", got.ResultArtifactHash)
	}
}

func TestEvaluationRunItemCommitTerminalErrors(t *testing.T) {
	repo := NewEvaluationRunItemRepository(newTestItemDB(t))
	ctx := context.Background()

	// Non-terminal status is rejected.
	if err := repo.CommitTerminalItem(ctx, 1, "run-1", "item-1", "owner", 1,
		types.EvaluationItemStatusRunning, types.JSON(`{}`), "", ""); !errors.Is(err, ErrEvaluationItemNotTerminalState) {
		t.Fatalf("non-terminal commit = %v, want ErrEvaluationItemNotTerminalState", err)
	}
	// Non-existent item is reported, not fabricated.
	if err := repo.CommitTerminalItem(ctx, 1, "run-1", "item-missing", "owner", 1,
		types.EvaluationItemStatusSucceeded, types.JSON(`{}`), "", ""); !errors.Is(err, ErrEvaluationItemNotFound) {
		t.Fatalf("missing commit = %v, want ErrEvaluationItemNotFound", err)
	}
}

func TestEvaluationRunItemTenantIsolation(t *testing.T) {
	repo := NewEvaluationRunItemRepository(newTestItemDB(t))
	ctx := context.Background()

	if err := repo.CreateItem(ctx, newTestItem(1, "run-1", "item-1")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := repo.GetItem(ctx, 2, "run-1", "item-1"); !errors.Is(err, ErrEvaluationItemNotFound) {
		t.Fatalf("cross-tenant get = %v, want not found", err)
	}
	// Cross-tenant commit must not affect tenant 1's item.
	if err := repo.CommitTerminalItem(ctx, 2, "run-1", "item-1", "owner", 1,
		types.EvaluationItemStatusFailedTerm, types.JSON(`{}`), "", "x"); !errors.Is(err, ErrEvaluationItemNotFound) {
		t.Fatalf("cross-tenant commit = %v, want not found", err)
	}
	got, err := repo.GetItem(ctx, 1, "run-1", "item-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != types.EvaluationItemStatusPending {
		t.Fatalf("cross-tenant commit polluted tenant 1 item: %s", got.Status)
	}
}

func TestEvaluationRunItemListOrdered(t *testing.T) {
	repo := NewEvaluationRunItemRepository(newTestItemDB(t))
	ctx := context.Background()

	// Insert out of order; listing must be item_id-ascending (deterministic
	// aggregate recomputation).
	for _, id := range []string{"item-3", "item-1", "item-2"} {
		if err := repo.CreateItem(ctx, newTestItem(1, "run-1", id)); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	items, err := repo.ListItems(ctx, 1, "run-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 3 || items[0].ItemID != "item-1" || items[1].ItemID != "item-2" || items[2].ItemID != "item-3" {
		t.Fatalf("unexpected order: %+v", items)
	}
	// Tenant 2 sees nothing for the same run_id.
	other, err := repo.ListItems(ctx, 2, "run-1")
	if err != nil {
		t.Fatalf("list other tenant: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("cross-tenant list leaked %d items", len(other))
	}
}

func TestEvaluationItemAttemptCreateAndDuplicate(t *testing.T) {
	repo := NewEvaluationRunItemRepository(newTestItemDB(t))
	ctx := context.Background()

	if err := repo.CreateItem(ctx, newTestItem(1, "run-1", "item-1")); err != nil {
		t.Fatalf("create item: %v", err)
	}
	a1 := &types.EvaluationItemAttempt{TenantID: 1, RunID: "run-1", ItemID: "item-1", AttemptNo: 1, Status: types.EvaluationAttemptStatusRunning}
	if err := repo.CreateAttempt(ctx, a1); err != nil {
		t.Fatalf("create attempt 1: %v", err)
	}
	// Duplicate attempt_no must conflict.
	if err := repo.CreateAttempt(ctx, &types.EvaluationItemAttempt{TenantID: 1, RunID: "run-1", ItemID: "item-1", AttemptNo: 1, Status: types.EvaluationAttemptStatusRunning}); !errors.Is(err, ErrEvaluationItemDuplicate) {
		t.Fatalf("duplicate attempt = %v, want ErrEvaluationItemDuplicate", err)
	}
	// A second RUNNING attempt number is fine at the persistence layer.
	if err := repo.CreateAttempt(ctx, &types.EvaluationItemAttempt{TenantID: 1, RunID: "run-1", ItemID: "item-1", AttemptNo: 2, Status: types.EvaluationAttemptStatusRunning}); err != nil {
		t.Fatalf("create attempt 2: %v", err)
	}

	attempts, err := repo.ListAttempts(ctx, 1, "run-1", "item-1")
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 2 || attempts[0].AttemptNo != 1 || attempts[1].AttemptNo != 2 {
		t.Fatalf("unexpected attempts: %+v", attempts)
	}
	// Cross-tenant attempt listing must be empty.
	other, err := repo.ListAttempts(ctx, 2, "run-1", "item-1")
	if err != nil {
		t.Fatalf("list other tenant attempts: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("cross-tenant attempt list leaked %d", len(other))
	}
}

func TestEvaluationItemAttemptRejectsInvalidOrOrphanFacts(t *testing.T) {
	repo := NewEvaluationRunItemRepository(newTestItemDB(t))
	ctx := context.Background()
	if err := repo.CreateItem(ctx, newTestItem(1, "run-1", "item-1")); err != nil {
		t.Fatalf("create item: %v", err)
	}

	for name, attempt := range map[string]*types.EvaluationItemAttempt{
		"nil":            nil,
		"tenant zero":    {TenantID: 0, RunID: "run-1", ItemID: "item-1", AttemptNo: 1, Status: types.EvaluationAttemptStatusRunning},
		"empty run":      {TenantID: 1, RunID: "", ItemID: "item-1", AttemptNo: 1, Status: types.EvaluationAttemptStatusRunning},
		"empty item":     {TenantID: 1, RunID: "run-1", ItemID: "", AttemptNo: 1, Status: types.EvaluationAttemptStatusRunning},
		"attempt zero":   {TenantID: 1, RunID: "run-1", ItemID: "item-1", AttemptNo: 0, Status: types.EvaluationAttemptStatusRunning},
		"terminal start": {TenantID: 1, RunID: "run-1", ItemID: "item-1", AttemptNo: 3, Status: types.EvaluationAttemptStatusSucceeded},
		"orphan":         {TenantID: 1, RunID: "run-1", ItemID: "missing", AttemptNo: 1, Status: types.EvaluationAttemptStatusRunning},
		"other tenant":   {TenantID: 2, RunID: "run-1", ItemID: "item-1", AttemptNo: 1, Status: types.EvaluationAttemptStatusRunning},
	} {
		t.Run(name, func(t *testing.T) {
			if err := repo.CreateAttempt(ctx, attempt); !errors.Is(err, ErrEvaluationAttemptInvalid) && !errors.Is(err, ErrEvaluationItemNotFound) {
				t.Fatalf("CreateAttempt() error = %v, want invalid or parent-not-found", err)
			}
		})
	}
}

func TestEvaluationRunItemClaimHeartbeatAndFencedTerminalCommit(t *testing.T) {
	repo := NewEvaluationRunItemRepository(newTestItemDB(t))
	ctx := context.Background()
	if err := repo.CreateItem(ctx, newTestItem(1, "run-1", "item-1")); err != nil {
		t.Fatalf("create item: %v", err)
	}

	claimed, err := repo.ClaimNextItem(ctx, 1, "run-1", "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.Status != types.EvaluationItemStatusRunning || claimed.FencingToken != 1 || claimed.AttemptCount != 1 {
		t.Fatalf("claimed item = %+v", claimed)
	}
	if err := repo.HeartbeatItem(ctx, 1, "run-1", "item-1", "worker-b", 1, time.Minute); !errors.Is(err, ErrEvaluationItemFenced) {
		t.Fatalf("foreign-owner heartbeat = %v, want fenced", err)
	}
	if err := repo.HeartbeatItem(ctx, 1, "run-1", "item-1", "worker-a", 1, time.Minute); err != nil {
		t.Fatalf("valid heartbeat: %v", err)
	}
	if err := repo.CommitTerminalItem(ctx, 1, "run-1", "item-1", "worker-b", 1,
		types.EvaluationItemStatusSucceeded, types.JSON(`{"ok":true}`), "hash", ""); !errors.Is(err, ErrEvaluationItemFenced) {
		t.Fatalf("foreign-owner commit = %v, want fenced", err)
	}
	if err := repo.CommitTerminalItem(ctx, 1, "run-1", "item-1", "worker-a", 1,
		types.EvaluationItemStatusSucceeded, types.JSON(`{ "ok": true }`), "hash", ""); err != nil {
		t.Fatalf("valid commit: %v", err)
	}
	if err := repo.CommitTerminalItem(ctx, 1, "run-1", "item-1", "worker-a", 1,
		types.EvaluationItemStatusFailedTerm, types.JSON(`{"ok":false}`), "other", "failed"); !errors.Is(err, ErrEvaluationItemAlreadyTerminal) {
		t.Fatalf("duplicate terminal commit = %v, want already terminal", err)
	}

	attempts, err := repo.ListAttempts(ctx, 1, "run-1", "item-1")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %+v, err = %v", attempts, err)
	}
	if attempts[0].Status != types.EvaluationAttemptStatusSucceeded || attempts[0].EndedAt == nil || string(attempts[0].ResultJSON) != `{"ok":true}` {
		t.Fatalf("attempt not closed with canonical result: %+v", attempts[0])
	}
}

func TestEvaluationRunItemExpiredLeaseRejectsStaleOwner(t *testing.T) {
	db := newTestItemDB(t)
	repo := NewEvaluationRunItemRepository(db)
	ctx := context.Background()
	if err := repo.CreateItem(ctx, newTestItem(1, "run-1", "item-1")); err != nil {
		t.Fatalf("create item: %v", err)
	}
	first, err := repo.ClaimNextItem(ctx, 1, "run-1", "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := db.Model(&types.EvaluationRunItem{}).
		Where("tenant_id = ? AND run_id = ? AND item_id = ?", 1, "run-1", "item-1").
		Update("lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	second, err := repo.ClaimNextItem(ctx, 1, "run-1", "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if second.FencingToken <= first.FencingToken || second.AttemptCount != 2 {
		t.Fatalf("reclaim did not advance token/attempt: first=%+v second=%+v", first, second)
	}
	if err := repo.CommitTerminalItem(ctx, 1, "run-1", "item-1", "worker-a", first.FencingToken,
		types.EvaluationItemStatusSucceeded, types.JSON(`{"stale":true}`), "stale", ""); !errors.Is(err, ErrEvaluationItemFenced) {
		t.Fatalf("stale commit = %v, want fenced", err)
	}
}
