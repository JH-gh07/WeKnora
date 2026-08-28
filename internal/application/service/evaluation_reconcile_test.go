package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type fakeReconcileRunRepo struct {
	interfaces.EvaluationRunRepository
	running []*types.EvaluationRun
	updated []*types.EvaluationRun
}

func (f *fakeReconcileRunRepo) ListReconciliationCandidates(_ context.Context) ([]*types.EvaluationRun, error) {
	return f.running, nil
}

func (f *fakeReconcileRunRepo) Update(_ context.Context, run *types.EvaluationRun) error {
	f.updated = append(f.updated, run)
	return nil
}

type fakeReconcileKBFinder struct {
	interfaces.TemporaryKnowledgeBaseFinder
	kb  *types.KnowledgeBase
	err error
}

func (f *fakeReconcileKBFinder) GetTemporaryKnowledgeBaseByResourceKey(
	_ context.Context, _ uint64, _ string,
) (*types.KnowledgeBase, error) {
	return f.kb, f.err
}

type fakeReconcileKBService struct {
	interfaces.KnowledgeBaseService
	deleted []string
	fail    bool
	err     error
}

func (f *fakeReconcileKBService) DeleteKnowledgeBase(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	if f.err != nil {
		return f.err
	}
	if f.fail {
		return context.DeadlineExceeded
	}
	return nil
}

type fakeReconcileTenantService struct {
	interfaces.TenantService
	tenant *types.Tenant
	err    error
}

func (f *fakeReconcileTenantService) GetTenantByID(_ context.Context, _ uint64) (*types.Tenant, error) {
	return f.tenant, f.err
}

func TestReconcileInterruptedRunsMarksInterruptedAndCleans(t *testing.T) {
	run := &types.EvaluationRun{
		RunID:                "run-1",
		TaskID:               "task-1",
		TenantID:             1,
		Status:               types.EvaluationRunStatusRunning,
		TemporaryResourceKey: "resource-1",
		TemporaryKBID:        "",   // simulate crash before kb id was persisted
		MetricsValid:         true, // stale in-memory aggregate must be downgraded
	}

	runRepo := &fakeReconcileRunRepo{running: []*types.EvaluationRun{run}}
	kbFinder := &fakeReconcileKBFinder{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 1}}
	kbSvc := &fakeReconcileKBService{}

	svc := &EvaluationService{
		runRepository:        runRepo,
		temporaryKBFinder:    kbFinder,
		knowledgeBaseService: kbSvc,
		tenantService:        &fakeReconcileTenantService{tenant: &types.Tenant{ID: 1}},
	}

	n, err := svc.ReconcileInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reconciled, got %d", n)
	}
	if len(runRepo.updated) == 0 {
		t.Fatalf("expected run to be updated")
	}
	got := runRepo.updated[0]
	if got.Status != types.EvaluationRunStatusInterrupted {
		t.Fatalf("expected INTERRUPTED, got %s", got.Status)
	}
	if got.InterruptionReason != types.InterruptionReasonServerRestart {
		t.Fatalf("expected SERVER_RESTART reason, got %q", got.InterruptionReason)
	}
	if got.MetricsValid {
		t.Fatalf("metrics_valid must be downgraded to false for interrupted run")
	}
	if got.ProcessedCount > got.TotalCount {
		t.Fatalf("processed_count must not exceed total_count")
	}
	if got.EndedAt == nil {
		t.Fatalf("ended_at must be set")
	}
	// The temp KB must have been discovered and deleted by resource key.
	if len(kbSvc.deleted) != 1 || kbSvc.deleted[0] != "kb-1" {
		t.Fatalf("expected kb-1 to be deleted, got %v", kbSvc.deleted)
	}
}

// TestReconcileInterruptedRunsHandlesPendingAndFailed covers the orphan windows
// the RUNNING-only listing missed: a PENDING run (crash after KB create before
// start) and a FAILED run (crash before cleanup) must both get their temporary
// resource cleaned, with PENDING becoming INTERRUPTED and FAILED staying FAILED.
func TestReconcileInterruptedRunsHandlesPendingAndFailed(t *testing.T) {
	pending := &types.EvaluationRun{
		RunID:                "run-pending",
		TaskID:               "task-pending",
		TenantID:             1,
		Status:               types.EvaluationRunStatusPending,
		CleanupStatus:        types.CleanupStatusCreated,
		TemporaryResourceKey: "resource-pending",
		TemporaryKBID:        "kb-pending",
	}
	failed := &types.EvaluationRun{
		RunID:                "run-failed",
		TaskID:               "task-failed",
		TenantID:             1,
		Status:               types.EvaluationRunStatusFailed,
		CleanupStatus:        types.CleanupStatusCreated,
		TemporaryResourceKey: "resource-failed",
		TemporaryKBID:        "kb-failed",
	}

	runRepo := &fakeReconcileRunRepo{running: []*types.EvaluationRun{pending, failed}}
	kbSvc := &fakeReconcileKBService{}

	svc := &EvaluationService{
		runRepository:        runRepo,
		temporaryKBFinder:    &fakeReconcileKBFinder{},
		knowledgeBaseService: kbSvc,
		tenantService:        &fakeReconcileTenantService{tenant: &types.Tenant{ID: 1}},
	}

	n, err := svc.ReconcileInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 reconciled, got %d", n)
	}

	// Both KBs (already known by id) must be deleted.
	if len(kbSvc.deleted) != 2 {
		t.Fatalf("expected 2 KBs deleted, got %v", kbSvc.deleted)
	}

	// PENDING becomes INTERRUPTED; FAILED stays FAILED.
	var pendingGot, failedGot *types.EvaluationRun
	for _, u := range runRepo.updated {
		switch u.RunID {
		case "run-pending":
			pendingGot = u
		case "run-failed":
			failedGot = u
		}
	}
	if pendingGot == nil || pendingGot.Status != types.EvaluationRunStatusInterrupted {
		t.Fatalf("pending run must become INTERRUPTED, got %+v", pendingGot)
	}
	if failedGot == nil || failedGot.Status != types.EvaluationRunStatusFailed {
		t.Fatalf("failed run must stay FAILED, got %+v", failedGot)
	}
}

// TestReconcileInterruptedRunsPreservesTerminalStates covers terminal states
// with incomplete cleanup: COMPLETED/INTERRUPTED/FAILED must keep their status
// (not be rewritten) while reconciliation retries their temporary-resource
// cleanup.
func TestReconcileInterruptedRunsPreservesTerminalStates(t *testing.T) {
	completed := &types.EvaluationRun{
		RunID:                "run-completed",
		TaskID:               "task-completed",
		TenantID:             1,
		Status:               types.EvaluationRunStatusCompleted,
		CleanupStatus:        types.CleanupStatusCreated,
		TemporaryResourceKey: "resource-completed",
		TemporaryKBID:        "kb-completed",
	}
	interrupted := &types.EvaluationRun{
		RunID:                "run-interrupted",
		TaskID:               "task-interrupted",
		TenantID:             1,
		Status:               types.EvaluationRunStatusInterrupted,
		CleanupStatus:        types.CleanupStatusCreated,
		TemporaryResourceKey: "resource-interrupted",
		TemporaryKBID:        "kb-interrupted",
	}

	runRepo := &fakeReconcileRunRepo{running: []*types.EvaluationRun{completed, interrupted}}
	kbSvc := &fakeReconcileKBService{}

	svc := &EvaluationService{
		runRepository:        runRepo,
		temporaryKBFinder:    &fakeReconcileKBFinder{},
		knowledgeBaseService: kbSvc,
		tenantService:        &fakeReconcileTenantService{tenant: &types.Tenant{ID: 1}},
	}

	n, err := svc.ReconcileInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 reconciled, got %d", n)
	}

	for _, u := range runRepo.updated {
		if u.RunID == "run-completed" && u.Status != types.EvaluationRunStatusCompleted {
			t.Fatalf("COMPLETED must stay COMPLETED, got %s", u.Status)
		}
		if u.RunID == "run-interrupted" && u.Status != types.EvaluationRunStatusInterrupted {
			t.Fatalf("INTERRUPTED must stay INTERRUPTED, got %s", u.Status)
		}
		if u.TemporaryKBID != "" {
			t.Fatalf("temporary_kb_id must be cleared after successful delete, got %q", u.TemporaryKBID)
		}
	}
	if len(kbSvc.deleted) != 2 {
		t.Fatalf("expected 2 KBs deleted, got %v", kbSvc.deleted)
	}
}

// TestReconcileTemporaryResourceKeepsKBIDOnDeleteFailure asserts that a failed
// delete does NOT clear temporary_kb_id, so a later reconciliation can retry.
func TestReconcileTemporaryResourceKeepsKBIDOnDeleteFailure(t *testing.T) {
	run := &types.EvaluationRun{
		RunID:                "run-faildel",
		TaskID:               "task-faildel",
		TenantID:             1,
		Status:               types.EvaluationRunStatusFailed,
		CleanupStatus:        types.CleanupStatusFailed,
		TemporaryResourceKey: "resource-faildel",
		TemporaryKBID:        "kb-faildel",
	}

	runRepo := &fakeReconcileRunRepo{running: []*types.EvaluationRun{run}}
	kbSvc := &fakeReconcileKBService{fail: true}

	svc := &EvaluationService{
		runRepository:        runRepo,
		temporaryKBFinder:    &fakeReconcileKBFinder{},
		knowledgeBaseService: kbSvc,
		tenantService:        &fakeReconcileTenantService{tenant: &types.Tenant{ID: 1}},
	}

	n, err := svc.ReconcileInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reconciled, got %d", n)
	}
	if len(kbSvc.deleted) != 1 {
		t.Fatalf("expected 1 delete attempt, got %v", kbSvc.deleted)
	}
	if len(runRepo.updated) == 0 {
		t.Fatalf("expected run to be updated")
	}
	got := runRepo.updated[len(runRepo.updated)-1]
	if got.TemporaryKBID != "kb-faildel" {
		t.Fatalf("temporary_kb_id must be retained on delete failure, got %q", got.TemporaryKBID)
	}
	if got.CleanupStatus != types.CleanupStatusFailed {
		t.Fatalf("cleanup_status must be FAILED, got %q", got.CleanupStatus)
	}
}

// TestReconcileTemporaryResourceTransientFinderErrorDoesNotMarkDone asserts that a
// transient finder failure (e.g. DB timeout) must NOT be misread as "KB does not
// exist": the run must keep its cleanup_status and temporary_kb_id and stay a
// reconciliation candidate, rather than being falsely marked DONE (which would
// strand a real KB as a permanent orphan).
func TestReconcileTemporaryResourceTransientFinderErrorDoesNotMarkDone(t *testing.T) {
	run := &types.EvaluationRun{
		RunID:                "run-transient",
		TaskID:               "task-transient",
		TenantID:             1,
		Status:               types.EvaluationRunStatusFailed,
		CleanupStatus:        types.CleanupStatusCreated,
		TemporaryResourceKey: "resource-transient",
		TemporaryKBID:        "", // id not yet persisted → must be discovered
	}

	runRepo := &fakeReconcileRunRepo{running: []*types.EvaluationRun{run}}
	svc := &EvaluationService{
		runRepository:        runRepo,
		temporaryKBFinder:    &fakeReconcileKBFinder{err: context.DeadlineExceeded},
		knowledgeBaseService: &fakeReconcileKBService{},
		tenantService:        &fakeReconcileTenantService{tenant: &types.Tenant{ID: 1}},
	}

	n, err := svc.ReconcileInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reconciled, got %d", n)
	}
	// No durable write may happen on a transient finder error: the run must not
	// be marked DONE (or anything else) based on an inconclusive lookup.
	if len(runRepo.updated) != 0 {
		t.Fatalf("expected no update on transient finder error, got %+v", runRepo.updated)
	}
	if run.CleanupStatus != types.CleanupStatusCreated {
		t.Fatalf("cleanup_status must be preserved, got %q", run.CleanupStatus)
	}
	if run.TemporaryKBID != "" {
		t.Fatalf("temporary_kb_id must be preserved, got %q", run.TemporaryKBID)
	}
}

// TestReconcileTemporaryResourceFinderNotFoundMarksDone asserts that a definitive
// finder "not found" is the only case that may mark DONE: with no discoverable KB
// the run converges to DONE and (per the convergence fix) resets persistence_status
// to PERSISTED so it stops being a candidate.
func TestReconcileTemporaryResourceFinderNotFoundMarksDone(t *testing.T) {
	run := &types.EvaluationRun{
		RunID:                "run-notfound",
		TaskID:               "task-notfound",
		TenantID:             1,
		Status:               types.EvaluationRunStatusFailed,
		CleanupStatus:        types.CleanupStatusCreated,
		TemporaryResourceKey: "resource-notfound",
		TemporaryKBID:        "",
		PersistenceStatus:    types.PersistenceStatusPersistFailed,
	}

	runRepo := &fakeReconcileRunRepo{running: []*types.EvaluationRun{run}}
	kbSvc := &fakeReconcileKBService{}
	svc := &EvaluationService{
		runRepository:        runRepo,
		temporaryKBFinder:    &fakeReconcileKBFinder{err: repository.ErrKnowledgeBaseNotFound},
		knowledgeBaseService: kbSvc,
		tenantService:        &fakeReconcileTenantService{tenant: &types.Tenant{ID: 1}},
	}

	n, err := svc.ReconcileInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reconciled, got %d", n)
	}
	if len(kbSvc.deleted) != 0 {
		t.Fatalf("expected no delete attempt, got %v", kbSvc.deleted)
	}
	if len(runRepo.updated) == 0 {
		t.Fatalf("expected run to be updated")
	}
	got := runRepo.updated[len(runRepo.updated)-1]
	if got.CleanupStatus != types.CleanupStatusDone {
		t.Fatalf("expected DONE, got %q", got.CleanupStatus)
	}
	if got.PersistenceStatus != types.PersistenceStatusPersisted {
		t.Fatalf("expected PERSISTED after successful write, got %q", got.PersistenceStatus)
	}
}

// TestReconcileTemporaryResourceAlreadySoftDeletedKBConverges asserts that a delete
// returning ErrKnowledgeBaseNotFound (the KB row is already soft-deleted because a
// prior delete's logical phase completed before this run's cleanup write landed) is
// treated as "logical delete already done" — DELETE_REQUESTED + cleared temporary_kb_id
// — rather than a permanent FAILED.
func TestReconcileTemporaryResourceAlreadySoftDeletedKBConverges(t *testing.T) {
	run := &types.EvaluationRun{
		RunID:                "run-softdeleted",
		TaskID:               "task-softdeleted",
		TenantID:             1,
		Status:               types.EvaluationRunStatusCompleted,
		CleanupStatus:        types.CleanupStatusFailed,
		TemporaryResourceKey: "resource-softdeleted",
		TemporaryKBID:        "kb-already-gone",
		PersistenceStatus:    types.PersistenceStatusPersistFailed,
	}

	runRepo := &fakeReconcileRunRepo{running: []*types.EvaluationRun{run}}
	kbSvc := &fakeReconcileKBService{err: repository.ErrKnowledgeBaseNotFound}
	svc := &EvaluationService{
		runRepository:        runRepo,
		temporaryKBFinder:    &fakeReconcileKBFinder{},
		knowledgeBaseService: kbSvc,
		tenantService:        &fakeReconcileTenantService{tenant: &types.Tenant{ID: 1}},
	}

	n, err := svc.ReconcileInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reconciled, got %d", n)
	}
	if len(kbSvc.deleted) != 1 {
		t.Fatalf("expected 1 delete attempt, got %v", kbSvc.deleted)
	}
	if len(runRepo.updated) == 0 {
		t.Fatalf("expected run to be updated")
	}
	got := runRepo.updated[len(runRepo.updated)-1]
	if got.CleanupStatus != types.CleanupStatusDeleteRequested {
		t.Fatalf("expected DELETE_REQUESTED for already-soft-deleted KB, got %q", got.CleanupStatus)
	}
	if got.TemporaryKBID != "" {
		t.Fatalf("temporary_kb_id must be cleared, got %q", got.TemporaryKBID)
	}
	if got.PersistenceStatus != types.PersistenceStatusPersisted {
		t.Fatalf("expected PERSISTED after successful write, got %q", got.PersistenceStatus)
	}
}

// TestReconcileConvergesPersistFailedRun asserts the P2 convergence: a run in
// COMPLETED + DELETE_REQUESTED + PERSIST_FAILED (the state that previously made it a
// permanent candidate) is reconciled and its persistence_status reset to PERSISTED,
// so a second reconciliation would list no candidates.
func TestReconcileConvergesPersistFailedRun(t *testing.T) {
	run := &types.EvaluationRun{
		RunID:                "run-converge",
		TaskID:               "task-converge",
		TenantID:             1,
		Status:               types.EvaluationRunStatusCompleted,
		CleanupStatus:        types.CleanupStatusDeleteRequested,
		TemporaryResourceKey: "resource-converge",
		TemporaryKBID:        "",
		PersistenceStatus:    types.PersistenceStatusPersistFailed,
	}

	runRepo := &fakeReconcileRunRepo{running: []*types.EvaluationRun{run}}
	svc := &EvaluationService{
		runRepository:        runRepo,
		temporaryKBFinder:    &fakeReconcileKBFinder{err: repository.ErrKnowledgeBaseNotFound},
		knowledgeBaseService: &fakeReconcileKBService{},
		tenantService:        &fakeReconcileTenantService{tenant: &types.Tenant{ID: 1}},
	}

	n, err := svc.ReconcileInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reconciled, got %d", n)
	}
	if len(runRepo.updated) == 0 {
		t.Fatalf("expected run to be updated")
	}
	got := runRepo.updated[len(runRepo.updated)-1]
	if got.PersistenceStatus != types.PersistenceStatusPersisted {
		t.Fatalf("persistence_status must be reset to PERSISTED, got %q", got.PersistenceStatus)
	}
	if got.CleanupStatus != types.CleanupStatusDone {
		t.Fatalf("cleanup_status must converge to DONE, got %q", got.CleanupStatus)
	}
}

// TestReconcileTemporaryResourceTenantLoadFailureDoesNotPanic asserts that when
// the tenant cannot be resolved (e.g. it was deleted while a temp KB cleanup was
// pending), reconciliation must NOT panic — the historical nil-*Tenant dereference
// in DeleteKnowledgeBase.GetEffectiveEngines would crash the whole process during
// startup. Instead it leaves cleanup_status/temporary_kb_id untouched so the run
// stays a candidate, and performs no delete.
func TestReconcileTemporaryResourceTenantLoadFailureDoesNotPanic(t *testing.T) {
	run := &types.EvaluationRun{
		RunID:                "run-tenant-gone",
		TaskID:               "task-tenant-gone",
		TenantID:             1,
		Status:               types.EvaluationRunStatusRunning,
		CleanupStatus:        types.CleanupStatusCreated,
		TemporaryResourceKey: "resource-tenant-gone",
		TemporaryKBID:        "kb-tenant-gone",
	}

	runRepo := &fakeReconcileRunRepo{running: []*types.EvaluationRun{run}}
	kbSvc := &fakeReconcileKBService{}

	svc := &EvaluationService{
		runRepository:        runRepo,
		temporaryKBFinder:    &fakeReconcileKBFinder{},
		knowledgeBaseService: kbSvc,
		tenantService:        &fakeReconcileTenantService{tenant: nil, err: context.DeadlineExceeded},
	}

	// Must not panic.
	n, err := svc.ReconcileInterruptedRuns(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reconciled, got %d", n)
	}
	if len(kbSvc.deleted) != 0 {
		t.Fatalf("expected no delete when tenant cannot be resolved, got %v", kbSvc.deleted)
	}
	if len(runRepo.updated) == 0 {
		t.Fatalf("expected the RUNNING→INTERRUPTED marking to be persisted")
	}
	got := runRepo.updated[len(runRepo.updated)-1]
	if got.Status != types.EvaluationRunStatusInterrupted {
		t.Fatalf("expected INTERRUPTED, got %s", got.Status)
	}
	if got.CleanupStatus != types.CleanupStatusCreated {
		t.Fatalf("cleanup_status must stay CREATED on tenant-load failure, got %q", got.CleanupStatus)
	}
	if got.TemporaryKBID != "kb-tenant-gone" {
		t.Fatalf("temporary_kb_id must be retained on tenant-load failure, got %q", got.TemporaryKBID)
	}
}
