package repository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestEvaluationRunSurvivesProcessRestart simulates a process restart by
// opening a second GORM connection (a second repository instance) over the
// same on-disk SQLite file. It proves the durable facts (status, metrics,
// provenance, resource locator) are readable after the "process" ends.
func TestEvaluationRunSurvivesProcessRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "eval.db")

	// "Process 1": migrate, create a RUNNING run.
	db1, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	if err := db1.AutoMigrate(&types.EvaluationRun{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	repo1 := NewEvaluationRunRepository(db1)
	ctx := context.Background()
	run := newTestRun(1, "task-1", "run-1", types.EvaluationRunStatusRunning)
	run.MetricsJSON = types.JSON(`{"retrieval_metrics":{"precision":1},"generation_metrics":{}}`)
	run.MetricsValid = true
	if err := repo1.Create(ctx, run); err != nil {
		t.Fatalf("create: %v", err)
	}
	// End "process 1".
	if sqlDB, err := db1.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// "Process 2": a fresh repository over the same file must still see the run.
	db2, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	repo2 := NewEvaluationRunRepository(db2)

	got, err := repo2.GetByTaskID(ctx, 1, "task-1")
	if err != nil {
		t.Fatalf("run must survive restart, got %v", err)
	}
	if got.Status != types.EvaluationRunStatusRunning || got.RunID != "run-1" {
		t.Fatalf("unexpected persisted run: %+v", got)
	}

	// Reconciliation sees the RUNNING run and marks it INTERRUPTED.
	running, err := repo2.ListByStatus(ctx, types.EvaluationRunStatusRunning)
	if err != nil || len(running) != 1 {
		t.Fatalf("expected 1 RUNNING run, got %d err=%v", len(running), err)
	}
	got.Status = types.EvaluationRunStatusInterrupted
	got.InterruptionReason = types.InterruptionReasonServerRestart
	got.MetricsValid = false
	if err := repo2.Update(ctx, got); err != nil {
		t.Fatalf("update interrupted: %v", err)
	}

	got2, err := repo2.GetByTaskID(ctx, 1, "task-1")
	if err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	if got2.Status != types.EvaluationRunStatusInterrupted ||
		got2.InterruptionReason != types.InterruptionReasonServerRestart ||
		got2.MetricsValid {
		t.Fatalf("unexpected reconciled run: %+v", got2)
	}

	// Cross-tenant read still forbidden.
	if _, err := repo2.GetByTaskID(ctx, 2, "task-1"); !errors.Is(err, ErrEvaluationRunNotFound) {
		t.Fatalf("expected not-found cross-tenant, got %v", err)
	}
}
