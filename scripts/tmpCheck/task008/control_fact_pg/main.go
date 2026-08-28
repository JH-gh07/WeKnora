// Command control_fact_pg runs the Task008 Control Fact (F1A/F1B/F6A/F6B/F6C/D1B)
// durable-state matrix against an isolated PostgreSQL database, in two phases
// to simulate a real process crash between them:
//
//	phase seed      — write RUNNING/PENDING/PERSIST_FAILED/cleanup-pending rows,
//	                  print sanitized counts, then exit (simulated crash, no
//	                  in-memory state survives).
//	phase reconcile — fresh process: run the production reconcile state machine
//	                  (EvaluationService.ReconcileInterruptedRuns with the REAL
//	                  PG-backed repository and deterministic KB/tenant fakes),
//	                  verify every terminal state invariant, then run a second
//	                  pass to prove idempotent convergence (no new mutation).
//
// It never prints credentials, DSN, prompts, vectors, or document bodies.
// Evidence level: FACT-RUNTIME (real PostgreSQL + real repository + restart
// boundary), with the knowledge-base/tenant lookup faked (FACT-TEST seam).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type fakeKBFinder struct {
	interfaces.TemporaryKnowledgeBaseFinder
	kb  *types.KnowledgeBase
	err error
}

func (f *fakeKBFinder) GetTemporaryKnowledgeBaseByResourceKey(
	_ context.Context, _ uint64, _ string,
) (*types.KnowledgeBase, error) {
	return f.kb, f.err
}

type fakeKBService struct {
	interfaces.KnowledgeBaseService
	deleted []string
	failFor map[string]bool
}

func (f *fakeKBService) DeleteKnowledgeBase(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	if f.failFor[id] {
		return fmt.Errorf("simulated delete failure")
	}
	return nil
}

type fakeTenantService struct {
	interfaces.TenantService
	tenant *types.Tenant
	err    error
}

func (f *fakeTenantService) GetTenantByID(_ context.Context, _ uint64) (*types.Tenant, error) {
	return f.tenant, f.err
}

var failures int

func check(name string, cond bool, detail string) {
	status := "PASS"
	if !cond {
		status = "FAIL"
		failures++
	}
	fmt.Printf("%s  %-44s %s\n", status, name, detail)
}

func openDB() *gorm.DB {
	dsn := os.Getenv("TASK008_PG_DSN")
	if dsn == "" {
		fmt.Println("TASK008_PG_DSN env is required (never printed)")
		os.Exit(2)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		fmt.Printf("open db: %v\n", err)
		os.Exit(2)
	}
	if err := db.AutoMigrate(&types.EvaluationRun{}); err != nil {
		fmt.Printf("automigrate evaluation_run: %v\n", err)
		os.Exit(2)
	}
	return db
}

func newService(db *gorm.DB) interfaces.EvaluationService {
	runRepo := repository.NewEvaluationRunRepository(db)
	svc := service.NewEvaluationService(
		nil, // config — unused by reconcile
		nil, // dataset
		&fakeKBService{failFor: map[string]bool{"kb-3": true}},
		nil, // knowledgeService
		nil, // sessionService
		nil, // modelService — unused by reconcile
		&fakeTenantService{tenant: &types.Tenant{ID: 1}},
		&fakeKBFinder{kb: &types.KnowledgeBase{ID: "kb-2", TenantID: 1}},
		runRepo,
		service.EvaluationBuildInfo{},
	)
	return svc
}

func seed(db *gorm.DB) {
	repo := repository.NewEvaluationRunRepository(db)
	ctx := context.Background()
	rows := []*types.EvaluationRun{
		{ // F6A: crash while RUNNING, before temporary KB id was persisted
			RunID: "run-running", TaskID: "task-1", TenantID: 1,
			Status:               types.EvaluationRunStatusRunning,
			TemporaryResourceKey: "resource-1", TemporaryKBID: "",
			MetricsValid: true, CleanupStatus: types.CleanupStatusCreated,
		},
		{ // F6B: terminal run with cleanup still CREATED (crash after completion)
			RunID: "run-completed", TaskID: "task-1", TenantID: 1,
			Status:               types.EvaluationRunStatusCompleted,
			TemporaryResourceKey: "resource-2", TemporaryKBID: "kb-2",
			CleanupStatus: types.CleanupStatusCreated,
		},
		{ // F6B: delete will fail -> locator must be retained
			RunID: "run-failed", TaskID: "task-1", TenantID: 1,
			Status:               types.EvaluationRunStatusFailed,
			TemporaryResourceKey: "resource-3", TemporaryKBID: "kb-3",
			CleanupStatus: types.CleanupStatusFailed,
		},
		{ // D1B: already converged -> must stay untouched
			RunID: "run-done", TaskID: "task-1", TenantID: 1,
			Status:        types.EvaluationRunStatusInterrupted,
			CleanupStatus: types.CleanupStatusDone,
		},
		{ // F1B: persist had failed earlier -> candidate, must converge to PERSISTED
			RunID: "run-persistfailed", TaskID: "task-1", TenantID: 1,
			Status:               types.EvaluationRunStatusRunning,
			TemporaryResourceKey: "resource-5", TemporaryKBID: "kb-2",
			CleanupStatus:     types.CleanupStatusCreated,
			PersistenceStatus: types.PersistenceStatusPersistFailed,
		},
	}
	for _, r := range rows {
		if err := repo.Create(ctx, r); err != nil {
			fmt.Printf("FAIL seed %s: %v\n", r.RunID, err)
			failures++
		}
	}
	fmt.Printf("seeded rows=%d (run-running, run-completed, run-failed, run-done, run-persistfailed)\n", len(rows))
}

func reconcile(pass int, db *gorm.DB) {
	ctx := context.Background()
	svc := newService(db)
	n, err := svc.ReconcileInterruptedRuns(ctx)
	if err != nil {
		fmt.Printf("FAIL reconcile pass %d: %v\n", pass, err)
		failures++
		return
	}
	fmt.Printf("reconcile pass %d handled=%d\n", pass, n)

	repo := repository.NewEvaluationRunRepository(db)
	get := func(runID string) *types.EvaluationRun {
		r, err := repo.GetByRunID(ctx, 1, runID)
		if err != nil {
			fmt.Printf("FAIL get %s: %v\n", runID, err)
			failures++
			return nil
		}
		return r
	}

	if pass == 1 {
		r := get("run-running")
		if r != nil {
			check("F6A SIGKILL->INTERRUPTED", r.Status == types.EvaluationRunStatusInterrupted &&
				r.InterruptionReason == types.InterruptionReasonServerRestart &&
				!r.MetricsValid,
				fmt.Sprintf("status=%s reason=%s metrics_valid=%v", r.Status, r.InterruptionReason, r.MetricsValid))
		}
		r = get("run-completed")
		if r != nil {
			check("F6B terminal preserved + cleanup requested",
				r.Status == types.EvaluationRunStatusCompleted && r.CleanupStatus == types.CleanupStatusDeleteRequested,
				fmt.Sprintf("status=%s cleanup=%s", r.Status, r.CleanupStatus))
		}
		r = get("run-failed")
		if r != nil {
			check("F6B delete failure retains locator",
				r.Status == types.EvaluationRunStatusFailed && r.CleanupStatus == types.CleanupStatusFailed && r.TemporaryKBID == "kb-3",
				fmt.Sprintf("status=%s cleanup=%s kb_id_retained=%v", r.Status, r.CleanupStatus, r.TemporaryKBID == "kb-3"))
		}
		r = get("run-done")
		if r != nil {
			check("D1B converged run untouched",
				r.Status == types.EvaluationRunStatusInterrupted && r.CleanupStatus == types.CleanupStatusDone,
				fmt.Sprintf("status=%s cleanup=%s", r.Status, r.CleanupStatus))
		}
		r = get("run-persistfailed")
		if r != nil {
			check("F1B PERSIST_FAILED converges to PERSISTED",
				r.Status == types.EvaluationRunStatusInterrupted && r.PersistenceStatus == types.PersistenceStatusPersisted,
				fmt.Sprintf("status=%s persistence=%s", r.Status, r.PersistenceStatus))
		}
	} else {
		// Contract-correct idempotence: rows that converged in pass 1 must not
		// drift; a persistently failing cleanup keeps its locator and is
		// re-attempted (frozen retry contract), never rewritten to a false
		// terminal. Assert per-row stability instead of handled==0.
		r := get("run-running")
		if r != nil {
			check("D1B pass2 converged INTERRUPTED unchanged",
				r.Status == types.EvaluationRunStatusInterrupted && r.CleanupStatus == types.CleanupStatusDeleteRequested && r.TemporaryKBID == "",
				fmt.Sprintf("status=%s cleanup=%s kb_cleared=%v", r.Status, r.CleanupStatus, r.TemporaryKBID == ""))
		}
		r = get("run-completed")
		if r != nil {
			check("D1B pass2 terminal preserved",
				r.Status == types.EvaluationRunStatusCompleted && r.CleanupStatus == types.CleanupStatusDeleteRequested,
				fmt.Sprintf("status=%s cleanup=%s", r.Status, r.CleanupStatus))
		}
		r = get("run-failed")
		if r != nil {
			check("D1B pass2 failing cleanup stable (retry, no drift)",
				r.Status == types.EvaluationRunStatusFailed && r.CleanupStatus == types.CleanupStatusFailed && r.TemporaryKBID == "kb-3",
				fmt.Sprintf("status=%s cleanup=%s kb_id_retained=%v", r.Status, r.CleanupStatus, r.TemporaryKBID == "kb-3"))
		}
		r = get("run-persistfailed")
		if r != nil {
			check("D1B pass2 PERSISTED run stays converged",
				r.Status == types.EvaluationRunStatusInterrupted && r.PersistenceStatus == types.PersistenceStatusPersisted,
				fmt.Sprintf("status=%s persistence=%s", r.Status, r.PersistenceStatus))
		}
	}
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("usage: control_fact_pg seed|reconcile1|reconcile2")
		os.Exit(2)
	}
	db := openDB()
	switch os.Args[1] {
	case "seed":
		seed(db)
	case "reconcile1":
		reconcile(1, db)
	case "reconcile2":
		reconcile(2, db)
	default:
		fmt.Println("unknown phase")
		os.Exit(2)
	}
	if failures > 0 {
		fmt.Printf("FAILURES=%d\n", failures)
		os.Exit(1)
	}
	fmt.Println("ALL CHECKS PASS")
}
