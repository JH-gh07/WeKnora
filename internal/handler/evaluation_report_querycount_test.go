package handler

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
)

// countQueries registers a gorm SELECT/row/raw counter so the test can assert
// the report read model performs a FIXED number of queries regardless of the
// number of model_calls rows (no N+1, no per-row fan-out).
func countQueries(db *gorm.DB) *atomic.Int64 {
	counter := &atomic.Int64{}
	inc := func(*gorm.DB) { counter.Add(1) }
	db.Callback().Query().After("gorm:query").Register("task010:count:query", inc)
	db.Callback().Raw().After("gorm:raw").Register("task010:count:raw", inc)
	db.Callback().Row().After("gorm:row").Register("task010:count:row", inc)
	return counter
}

func newQueryCountEnv(t *testing.T) (*gorm.DB, *reportIntegrationEnv) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "qc.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.EvaluationRun{}, &types.ModelCall{}, &types.EmbeddingCacheObservation{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS model_metering_health (
		id VARCHAR(36) PRIMARY KEY,
		tenant_id INTEGER NOT NULL,
		run_id VARCHAR(36),
		item_id VARCHAR(36),
		logical_call_id VARCHAR(36) NOT NULL DEFAULT '',
		attempted_at DATETIME NOT NULL,
		persisted BOOLEAN NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create health table: %v", err)
	}
	runRepo := repository.NewEvaluationRunRepository(db)
	callRepo := repository.NewModelCallRepository(db)
	cacheRepo := repository.NewEmbeddingCacheRepository(db)
	usageSvc := service.NewModelUsageService(callRepo, cacheRepo, nil)
	reportSvc := service.NewEvaluationReportService(runRepo, usageSvc)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	env := &reportIntegrationEnv{db: db, runRepo: runRepo, tenant: 7}
	r.Use(func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), env.tenant)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.TenantIDContextKey, env.tenant))
		c.Next()
	})
	h := NewEvaluationHandler(nil, reportSvc)
	r.GET("/evaluation/runs/:run_id/report", h.GetEvaluationRunReport)
	env.engine = r
	return db, env
}

func TestReportQueryCountIsFixed(t *testing.T) {
	db, env := newQueryCountEnv(t)
	runID := "00000000-0000-0000-0000-00000000cccc"
	env.seedRun(t, completedRun(runID, true, validMetricsJSON()))

	// Seed 10k model_calls for the same run. The report read model must NOT
	// issue one query per row.
	const callCount = 10000
	now := time.Now().UTC()
	for i := 0; i < callCount; i++ {
		call := &types.ModelCall{
			ID:                   uuid.NewString(),
			TenantID:             env.tenant,
			RunID:                strPtr2(runID),
			Operation:            types.ModelOperationChat,
			ModelID:              "chat-1",
			ModelName:            "chat",
			Provider:             "fixture",
			UsageFinality:        types.UsageFinalityUnavailable,
			CacheStatus:          types.PromptCacheStatusUnreported,
			Success:              true,
			AttemptObservability: types.AttemptObservabilityUnobservable,
			CreatedAt:            now,
		}
		if err := db.Create(call).Error; err != nil {
			t.Fatalf("seed call %d: %v", i, err)
		}
	}

	counter := countQueries(db)
	code, rep := env.getReport(t, runID)
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	if rep.Usage.LogicalCallCount != callCount {
		t.Fatalf("logical calls = %d, want %d", rep.Usage.LogicalCallCount, callCount)
	}

	// Fixed query count (5, constant regardless of call volume):
	//   1. EvaluationRun lookup (GetByRunID)
	//   2. model_calls aggregate (AggregateModelCalls)
	//   3. pricing UNKNOWN reason distribution (one GROUP BY, never per-row)
	//   4. tenant-window health embedded in the shared usage aggregate
	//      (part of the G2B ModelUsageService.Aggregate contract, all-time window)
	//   5. run-window tenant health (report-specific observation window)
	// Local embedding cache is DISABLED here, so no extra observation query.
	// No per-row fan-out: the count must NOT grow with the 10k seeded calls.
	const want = 5
	if got := counter.Load(); got != want {
		t.Fatalf("query count = %d, want %d (N+1 or unbounded fan-out detected)", got, want)
	}
}

func TestReportQueryCountIndependentOfVolume(t *testing.T) {
	// 1 call and 10k calls must produce the same number of queries.
	db, env := newQueryCountEnv(t)
	runID := "00000000-0000-0000-0000-00000000dddd"
	env.seedRun(t, completedRun(runID, true, validMetricsJSON()))
	env.seedCall(t, env.tenant, runID, nil)
	counter := countQueries(db)
	code, _ := env.getReport(t, runID)
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	// The single-call path must equal the fixed budget as well.
	if got := counter.Load(); got != 5 {
		t.Fatalf("single-call query count = %d, want 5", got)
	}
}
