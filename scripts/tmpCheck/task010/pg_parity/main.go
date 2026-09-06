// Command pg_parity runs the Task010 run-level report read model against a
// fixed fixture on either SQLite or PostgreSQL and prints the normalized report
// JSON for a set of canonical terminal scenarios. The shell driver runs it
// twice (once per driver) and diffs the output to prove parity (D02).
//
// It prints NO prompt/document/model-response bodies and NO credentials.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	driver = flag.String("driver", "sqlite", "sqlite|postgres")
	dsn    = flag.String("dsn", "", "gorm DSN (postgres) or sqlite file path (sqlite)")
)

const tenantID uint64 = 42

var (
	t0    = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	t1    = t0.Add(10 * time.Second)
	metri = types.JSON(`{"retrieval_metrics":{"precision":0.85,"recall":0.92,"ndcg3":0.88,"ndcg10":0.86,"mrr":0.95,"map":0.87},"generation_metrics":{"bleu1":0.72,"bleu2":0.65,"bleu4":0.58,"rouge1":0.78,"rouge2":0.71,"rougel":0.75}}`)
)

func openDB() (*gorm.DB, error) {
	// Silent logger: the report JSON on stdout must be byte-clean and free of
	// gorm SQL/SLOW-SQL log lines (which would otherwise pollute the diff).
	cfg := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	if *driver == "postgres" {
		return gorm.Open(postgres.Open(*dsn), cfg)
	}
	if *dsn == "" {
		*dsn = filepath.Join(os.TempDir(), "task010_pg_parity.db")
	}
	return gorm.Open(sqlite.Open(*dsn), cfg)
}

func migrate(db *gorm.DB) error {
	if *driver == "postgres" {
		// Versioned migrations are applied by the shell driver.
		return nil
	}
	if err := db.AutoMigrate(&types.EvaluationRun{}, &types.ModelCall{}, &types.EmbeddingCacheObservation{}); err != nil {
		return err
	}
	return db.Exec(`CREATE TABLE IF NOT EXISTS model_metering_health (id VARCHAR(36) PRIMARY KEY, tenant_id INTEGER NOT NULL, attempted_at DATETIME NOT NULL, persisted BOOLEAN NOT NULL)`).Error
}

func completedRun(runID string) *types.EvaluationRun {
	return &types.EvaluationRun{
		RunID:             runID,
		TenantID:          tenantID,
		TaskID:            "evaluation-42-default",
		ProtocolHash:      "hash-" + runID,
		GitCommit:         "deadbeef",
		AppVersion:        "0.8.0",
		Status:            types.EvaluationRunStatusCompleted,
		StartedAt:         &t0,
		EndedAt:           &t1,
		MetricsJSON:       metri,
		MetricsValid:      true,
		PersistenceStatus: types.PersistenceStatusPersisted,
		CleanupStatus:     types.CleanupStatusDeleteRequested,
		MeasurementStatus: types.MeasurementStatusUnknown,
	}
}

func call(runID string, cost *float64, currency string, cacheStatus types.PromptCacheStatus) *types.ModelCall {
	return &types.ModelCall{
		ID:                   uuid.NewString(),
		TenantID:             tenantID,
		RunID:                &runID,
		Operation:            types.ModelOperationChat,
		ModelID:              "chat-1",
		ModelName:            "chat",
		Provider:             "fixture",
		UsageFinality:        types.UsageFinalityUnavailable,
		CacheStatus:          cacheStatus,
		Success:              true,
		AttemptObservability: types.AttemptObservabilityUnobservable,
		CreatedAt:            t0.Add(1 * time.Second),
		EstimatedCost:        cost,
		Currency:             currency,
	}
}

func reportFor(db *gorm.DB, runID string) (types.EvaluationRunReport, error) {
	runRepo := repository.NewEvaluationRunRepository(db)
	callRepo := repository.NewModelCallRepository(db)
	cacheRepo := repository.NewEmbeddingCacheRepository(db)
	usageSvc := service.NewModelUsageService(callRepo, cacheRepo, nil) // nil config → local cache DISABLED
	reportSvc := service.NewEvaluationReportService(runRepo, usageSvc)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
	r, err := reportSvc.GetRunReport(ctx, runID)
	if err != nil {
		return types.EvaluationRunReport{}, err
	}
	return *r, nil
}

func main() {
	flag.Parse()
	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(2)
	}
	if err := migrate(db); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(2)
	}

	// Scenario A: completed, valid metrics, known USD cost, cache hit.
	runA := "00000000-0000-0000-0000-0000000000a1"
	costA := 0.5
	db.Create(completedRun(runA))
	db.Create(call(runA, &costA, "USD", types.PromptCacheStatusHit))

	// Scenario B: completed, valid metrics, all-unknown cost.
	runB := "00000000-0000-0000-0000-0000000000b2"
	db.Create(completedRun(runB))
	db.Create(call(runB, nil, "", types.PromptCacheStatusMiss))

	// Scenario C: completed, valid metrics, mixed currency.
	runC := "00000000-0000-0000-0000-0000000000c3"
	costC1 := 1.0
	costC2 := 7.0
	db.Create(completedRun(runC))
	db.Create(call(runC, &costC1, "USD", types.PromptCacheStatusHit))
	db.Create(call(runC, &costC2, "CNY", types.PromptCacheStatusMiss))

	out := map[string]types.EvaluationRunReport{}
	for _, r := range []string{runA, runB, runC} {
		rep, err := reportFor(db, r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "report %s: %v\n", r, err)
			os.Exit(2)
		}
		out[r] = rep
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
		os.Exit(2)
	}
}
