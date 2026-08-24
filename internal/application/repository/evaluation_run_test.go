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

func newTestRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "eval.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.EvaluationRun{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func newTestRun(tenantID uint64, taskID, runID string, status types.EvaluationRunStatus) *types.EvaluationRun {
	now := time.Now()
	return &types.EvaluationRun{
		RunID:                runID,
		TaskID:               taskID,
		TenantID:             tenantID,
		ProtocolHash:         "hash-" + runID,
		ProtocolSnapshot:     types.JSON(`{"dataset_id":"default"}`),
		RunProvenance:        types.JSON(`{"git_commit":"AAA"}`),
		GitCommit:            "AAA",
		AppVersion:           "0.7.2",
		Status:               status,
		StartedAt:            &now,
		TemporaryResourceKey: "resource-" + runID,
		CleanupStatus:        types.CleanupStatusCreated,
		MeasurementStatus:    types.MeasurementStatusUnknown,
	}
}

func TestEvaluationRunRepositoryCRUDAndTenantIsolation(t *testing.T) {
	repo := NewEvaluationRunRepository(newTestRunDB(t))
	ctx := context.Background()

	runA := newTestRun(1, "task-a", "run-a", types.EvaluationRunStatusRunning)
	if err := repo.Create(ctx, runA); err != nil {
		t.Fatalf("create run A: %v", err)
	}
	runB := newTestRun(2, "task-b", "run-b", types.EvaluationRunStatusCompleted)
	if err := repo.Create(ctx, runB); err != nil {
		t.Fatalf("create run B: %v", err)
	}

	// GetByRunID scoped to the correct tenant.
	got, err := repo.GetByRunID(ctx, 1, "run-a")
	if err != nil {
		t.Fatalf("get run A: %v", err)
	}
	if got.RunID != "run-a" || got.TenantID != 1 {
		t.Fatalf("unexpected run: %+v", got)
	}

	// Tenant isolation: tenant 2 must not read tenant 1's run.
	if _, err := repo.GetByRunID(ctx, 2, "run-a"); !errors.Is(err, ErrEvaluationRunNotFound) {
		t.Fatalf("expected not-found for cross-tenant read, got %v", err)
	}

	// GetByTaskID resolves by task_id AND run_id.
	byTask, err := repo.GetByTaskID(ctx, 1, "task-a")
	if err != nil {
		t.Fatalf("get by task: %v", err)
	}
	if byTask.RunID != "run-a" {
		t.Fatalf("unexpected by-task run: %+v", byTask)
	}
	byRunID, err := repo.GetByTaskID(ctx, 1, "run-a")
	if err != nil {
		t.Fatalf("get by run_id alias: %v", err)
	}
	if byRunID.RunID != "run-a" {
		t.Fatalf("unexpected by-run-id run: %+v", byRunID)
	}

	// Update preserves tenant scope.
	got.Status = types.EvaluationRunStatusInterrupted
	got.InterruptionReason = types.InterruptionReasonServerRestart
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("update run: %v", err)
	}
	got2, err := repo.GetByRunID(ctx, 1, "run-a")
	if err != nil {
		t.Fatalf("get updated run: %v", err)
	}
	if got2.Status != types.EvaluationRunStatusInterrupted || got2.InterruptionReason != types.InterruptionReasonServerRestart {
		t.Fatalf("update not persisted: %+v", got2)
	}

	// ListByStatus spans tenants (single-worker reconciliation).
	running, err := repo.ListByStatus(ctx, types.EvaluationRunStatusRunning)
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	if len(running) != 0 {
		t.Fatalf("expected no RUNNING after update, got %d", len(running))
	}
	interrupted, err := repo.ListByStatus(ctx, types.EvaluationRunStatusInterrupted)
	if err != nil {
		t.Fatalf("list interrupted: %v", err)
	}
	if len(interrupted) != 1 || interrupted[0].RunID != "run-a" {
		t.Fatalf("expected exactly run-a interrupted, got %+v", interrupted)
	}
}

func TestEvaluationRunListReconciliationCandidates(t *testing.T) {
	repo := NewEvaluationRunRepository(newTestRunDB(t))
	ctx := context.Background()

	// RUNNING → candidate (owner died mid-run).
	running := newTestRun(1, "t-running", "run-running", types.EvaluationRunStatusRunning)
	// PENDING + CREATED → candidate (crash after KB create before start).
	pending := newTestRun(1, "t-pending", "run-pending", types.EvaluationRunStatusPending)
	// FAILED + CREATED → candidate (crash before cleanup).
	failedCreated := newTestRun(1, "t-failed", "run-failed", types.EvaluationRunStatusFailed)
	// COMPLETED + CREATED → candidate (crash between terminal write and cleanup).
	completedCreated := newTestRun(1, "t-comp-created", "run-comp-created", types.EvaluationRunStatusCompleted)
	// INTERRUPTED + CREATED → candidate (interrupted but cleanup never ran).
	interruptedCreated := newTestRun(1, "t-int-created", "run-int-created", types.EvaluationRunStatusInterrupted)
	// FAILED + FAILED → candidate (cleanup delete failed, must retry).
	failedFailed := newTestRun(1, "t-failed-failed", "run-failed-failed", types.EvaluationRunStatusFailed)
	failedFailed.CleanupStatus = types.CleanupStatusFailed
	// PERSIST_FAILED → candidate even with complete cleanup.
	completedPersistFailed := newTestRun(1, "t-comp-pf", "run-comp-pf", types.EvaluationRunStatusCompleted)
	completedPersistFailed.CleanupStatus = types.CleanupStatusDeleteRequested
	completedPersistFailed.PersistenceStatus = types.PersistenceStatusPersistFailed
	// FAILED + DELETE_REQUESTED + PERSISTED → NOT candidate (terminal + cleaned).
	failedDone := newTestRun(1, "t-failed-done", "run-failed-done", types.EvaluationRunStatusFailed)
	failedDone.CleanupStatus = types.CleanupStatusDeleteRequested
	failedDone.PersistenceStatus = types.PersistenceStatusPersisted
	// COMPLETED + DELETE_REQUESTED + PERSISTED → NOT candidate.
	completedDone := newTestRun(1, "t-comp-done", "run-comp-done", types.EvaluationRunStatusCompleted)
	completedDone.CleanupStatus = types.CleanupStatusDeleteRequested
	completedDone.PersistenceStatus = types.PersistenceStatusPersisted

	all := []*types.EvaluationRun{
		running, pending, failedCreated, completedCreated, interruptedCreated,
		failedFailed, completedPersistFailed, failedDone, completedDone,
	}
	for _, r := range all {
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("create %s: %v", r.RunID, err)
		}
	}

	got, err := repo.ListReconciliationCandidates(ctx)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	ids := map[string]bool{}
	for _, r := range got {
		ids[r.RunID] = true
	}

	want := map[string]bool{
		"run-running":       true,
		"run-pending":       true,
		"run-failed":        true,
		"run-comp-created":  true,
		"run-int-created":   true,
		"run-failed-failed": true,
		"run-comp-pf":       true,
	}
	for id := range want {
		if !ids[id] {
			t.Fatalf("expected candidate %s, got %v", id, ids)
		}
	}
	if ids["run-failed-done"] || ids["run-comp-done"] {
		t.Fatalf("terminal runs with complete cleanup must not be candidates, got %v", ids)
	}
}

// TestEvaluationRunReconciliationConvergesInTwoPasses proves the P2 convergence at
// the persistence layer: once a run's persistence_status is reset to PERSISTED (as
// EvaluationService.persistRun does after a successful write), a second candidate
// scan returns nothing — the PERSIST_FAILED marker no longer keeps it a permanent
// candidate.
func TestEvaluationRunReconciliationConvergesInTwoPasses(t *testing.T) {
	repo := NewEvaluationRunRepository(newTestRunDB(t))
	ctx := context.Background()

	run := newTestRun(1, "t-converge", "run-converge", types.EvaluationRunStatusCompleted)
	run.CleanupStatus = types.CleanupStatusDeleteRequested
	run.PersistenceStatus = types.PersistenceStatusPersistFailed
	if err := repo.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// First scan: PERSIST_FAILED makes it a candidate.
	first, err := repo.ListReconciliationCandidates(ctx)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(first) != 1 || first[0].RunID != "run-converge" {
		t.Fatalf("expected run-converge to be the only candidate, got %+v", first)
	}

	// Simulate a successful reconciliation write: reset persistence_status and
	// converge cleanup (mirrors EvaluationService.persistRun after a full Update).
	first[0].PersistenceStatus = types.PersistenceStatusPersisted
	first[0].CleanupStatus = types.CleanupStatusDone
	if err := repo.Update(ctx, first[0]); err != nil {
		t.Fatalf("update run: %v", err)
	}

	// Second scan: the run has fully persisted → no longer a candidate.
	second, err := repo.ListReconciliationCandidates(ctx)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("expected no candidates after convergence, got %+v", second)
	}
}

func TestTemporaryKnowledgeBaseFinder(t *testing.T) {
	db := newTestRunDB(t)
	// Also need the knowledge_bases table for the finder; migrate it here.
	if err := db.AutoMigrate(&types.KnowledgeBase{}); err != nil {
		t.Fatalf("automigrate kb: %v", err)
	}

	finder := NewTemporaryKnowledgeBaseFinder(db)
	ctx := context.Background()

	// Seed a temporary KB owned by tenant 1 with a resource key in Description.
	kb := &types.KnowledgeBase{
		ID:          "kb-1",
		Name:        "evaluation-12345678",
		TenantID:    1,
		Description: "resource-1",
		IsTemporary: true,
	}
	if err := db.WithContext(ctx).Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}

	got, err := finder.GetTemporaryKnowledgeBaseByResourceKey(ctx, 1, "resource-1")
	if err != nil {
		t.Fatalf("find by resource key: %v", err)
	}
	if got.ID != "kb-1" {
		t.Fatalf("unexpected kb: %+v", got)
	}

	// Wrong tenant must not find it.
	if _, err := finder.GetTemporaryKnowledgeBaseByResourceKey(ctx, 2, "resource-1"); !errors.Is(err, ErrKnowledgeBaseNotFound) {
		t.Fatalf("expected not-found for cross-tenant resource key, got %v", err)
	}
	// Wrong resource key must not find it.
	if _, err := finder.GetTemporaryKnowledgeBaseByResourceKey(ctx, 1, "resource-2"); !errors.Is(err, ErrKnowledgeBaseNotFound) {
		t.Fatalf("expected not-found for unknown resource key, got %v", err)
	}
}
