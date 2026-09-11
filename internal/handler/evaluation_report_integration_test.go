package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// reportIntegrationEnv wires the real repositories → real services → real
// HTTP handler against a deterministic SQLite database (Task010 Step 6).
// Fixtures are written through the repository layer and read back through the
// actual GET /evaluation/runs/:run_id/report handler, never by hand-feeding
// expected JSON into the UI.
type reportIntegrationEnv struct {
	db      *gorm.DB
	runRepo interfaces.EvaluationRunRepository
	engine  *gin.Engine
	tenant  uint64
}

func newReportIntegrationEnv(t *testing.T) *reportIntegrationEnv {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "report.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.EvaluationRun{}, &types.ModelCall{}, &types.EmbeddingCacheObservation{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	// model_metering_health is created by the versioned migrations in
	// production; here it is created by hand with the same schema so the
	// tenant-window health observation is exercisable.
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
	usageSvc := service.NewModelUsageService(callRepo, cacheRepo, nil) // nil config → local embedding cache DISABLED
	reportSvc := service.NewEvaluationReportService(runRepo, usageSvc)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	env := &reportIntegrationEnv{db: db, runRepo: runRepo, tenant: 7}
	r.Use(func(c *gin.Context) {
		// Mirror middleware.applyAuthSession: set both the gin Keys surface and
		// the request context, because the report service reads the tenant from
		// the request context (types.MustTenantIDFromContext).
		c.Set(types.TenantIDContextKey.String(), env.tenant)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.TenantIDContextKey, env.tenant))
		c.Next()
	})
	h := NewEvaluationHandler(nil, reportSvc)
	r.GET("/evaluation/runs/:run_id/report", h.GetEvaluationRunReport)
	env.engine = r
	return env
}

func (e *reportIntegrationEnv) getReport(t *testing.T, runID string) (int, types.EvaluationRunReport) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/evaluation/runs/"+runID+"/report", nil)
	e.engine.ServeHTTP(w, req)
	var body struct {
		Success bool                      `json:"success"`
		Data    types.EvaluationRunReport `json:"data"`
	}
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode 200 body: %v (body=%s)", err, w.Body.String())
		}
	}
	return w.Code, body.Data
}

func (e *reportIntegrationEnv) seedRun(t *testing.T, run *types.EvaluationRun) {
	t.Helper()
	if run.RunID == "" {
		run.RunID = uuid.NewString()
	}
	if run.TenantID == 0 {
		run.TenantID = e.tenant
	}
	if err := e.runRepo.Create(context.Background(), run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

func (e *reportIntegrationEnv) seedCall(t *testing.T, tenantID uint64, runID string, apply func(*types.ModelCall)) {
	t.Helper()
	call := &types.ModelCall{
		ID:                   uuid.NewString(),
		TenantID:             tenantID,
		RunID:                strPtr2(runID),
		Operation:            types.ModelOperationChat,
		ModelID:              "chat-1",
		ModelName:            "chat",
		Provider:             "fixture",
		UsageFinality:        types.UsageFinalityUnavailable,
		CacheStatus:          types.PromptCacheStatusUnreported,
		Success:              true,
		AttemptObservability: types.AttemptObservabilityUnobservable,
		CreatedAt:            time.Now().UTC(),
	}
	if apply != nil {
		apply(call)
	}
	if err := e.db.Create(call).Error; err != nil {
		t.Fatalf("seed call: %v", err)
	}
}

func strPtr2(s string) *string   { return &s }
func intPtr2(i int) *int         { return &i }
func f64Ptr2(f float64) *float64 { return &f }

func validMetricsJSON() types.JSON {
	return types.JSON(`{"retrieval_metrics":{"legacy_nonstandard_precision":0.85,"recall":0.92,"ndcg3":0.88,"ndcg10":0.86,"mrr":0.95,"legacy_nonstandard_map":0.87},"generation_metrics":{"bleu1":0.72,"bleu2":0.65,"bleu4":0.58,"rouge1":0.78,"rouge2":0.71,"rougel":0.75}}`)
}

func completedRun(runID string, metricsValid bool, metrics types.JSON) *types.EvaluationRun {
	start := time.Now().Add(-10 * time.Second).UTC()
	end := time.Now().UTC()
	return &types.EvaluationRun{
		RunID:             runID,
		TaskID:            "evaluation-7-default",
		ProtocolHash:      "hash-" + runID,
		GitCommit:         "deadbeef",
		AppVersion:        "0.8.0",
		Status:            types.EvaluationRunStatusCompleted,
		StartedAt:         &start,
		EndedAt:           &end,
		MetricsJSON:       metrics,
		MetricsValid:      metricsValid,
		PersistenceStatus: types.PersistenceStatusPersisted,
		CleanupStatus:     types.CleanupStatusDeleteRequested,
		MeasurementStatus: types.MeasurementStatusUnknown,
	}
}

// ---- fixtures -----------------------------------------------------------

func TestReportIntegrationCompletedKnownCost(t *testing.T) {
	env := newReportIntegrationEnv(t)
	env.seedRun(t, completedRun("00000000-0000-0000-0000-000000000001", true, validMetricsJSON()))
	env.seedCall(t, env.tenant, "00000000-0000-0000-0000-000000000001", func(c *types.ModelCall) {
		c.Success = true
		c.InputTokens = intPtr2(100)
		c.OutputTokens = intPtr2(50)
		c.EstimatedCost = f64Ptr2(0.5)
		c.Currency = "USD"
		c.UsageFinality = types.UsageFinalityReported
		c.CacheStatus = types.PromptCacheStatusHit
	})

	code, rep := env.getReport(t, "00000000-0000-0000-0000-000000000001")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if rep.SchemaVersion != "evaluation-run-report/v1" {
		t.Fatalf("schema version = %q", rep.SchemaVersion)
	}
	if rep.Quality.Availability != types.AvailabilityAvailable {
		t.Fatalf("quality availability = %q", rep.Quality.Availability)
	}
	if rep.Quality.Retrieval == nil || rep.Quality.Retrieval.Precision != 0.85 {
		t.Fatalf("retrieval = %+v", rep.Quality.Retrieval)
	}
	if rep.Cost.Availability != types.AvailabilityAvailable {
		t.Fatalf("cost availability = %q (reason %q)", rep.Cost.Availability, rep.Cost.ReasonCode)
	}
	if rep.Cost.KnownCostTotal == nil || *rep.Cost.KnownCostTotal != 0.5 {
		t.Fatalf("known cost = %v", rep.Cost.KnownCostTotal)
	}
	if rep.Cost.Currency == nil || *rep.Cost.Currency != "USD" {
		t.Fatalf("currency = %v", rep.Cost.Currency)
	}
	if !rep.Cost.IsEstimate {
		t.Fatal("cost must be marked estimate")
	}
	if rep.Latency.Availability != types.AvailabilityAvailable {
		t.Fatalf("latency availability = %q", rep.Latency.Availability)
	}
	if rep.Latency.EvaluationWallClockMS == nil || *rep.Latency.EvaluationWallClockMS < 0 {
		t.Fatalf("wall clock = %v", rep.Latency.EvaluationWallClockMS)
	}
	if rep.Warnings == nil {
		t.Fatal("warnings must be an empty array, never null")
	}
}

func TestReportIntegrationAllCostUnknown(t *testing.T) {
	env := newReportIntegrationEnv(t)
	env.seedRun(t, completedRun("00000000-0000-0000-0000-000000000002", true, validMetricsJSON()))
	env.seedCall(t, env.tenant, "00000000-0000-0000-0000-000000000002", func(c *types.ModelCall) {
		c.EstimatedCost = nil // unknown price
	})

	_, rep := env.getReport(t, "00000000-0000-0000-0000-000000000002")
	if rep.Cost.Availability != types.AvailabilityUnknown {
		t.Fatalf("cost availability = %q", rep.Cost.Availability)
	}
	if rep.Cost.KnownCostTotal != nil {
		t.Fatalf("known cost must be nil for all-unknown, got %v", *rep.Cost.KnownCostTotal)
	}
	if rep.Cost.UnknownCostCallCount != 1 {
		t.Fatalf("unknown count = %d", rep.Cost.UnknownCostCallCount)
	}
}

func TestReportIntegrationPartialCost(t *testing.T) {
	env := newReportIntegrationEnv(t)
	env.seedRun(t, completedRun("00000000-0000-0000-0000-000000000003", true, validMetricsJSON()))
	env.seedCall(t, env.tenant, "00000000-0000-0000-0000-000000000003", func(c *types.ModelCall) {
		c.EstimatedCost = f64Ptr2(0.5)
		c.Currency = "USD"
	})
	env.seedCall(t, env.tenant, "00000000-0000-0000-0000-000000000003", func(c *types.ModelCall) {
		c.EstimatedCost = nil
	})

	_, rep := env.getReport(t, "00000000-0000-0000-0000-000000000003")
	if rep.Cost.Availability != types.AvailabilityPartial {
		t.Fatalf("cost availability = %q", rep.Cost.Availability)
	}
	if rep.Cost.KnownCostTotal == nil || *rep.Cost.KnownCostTotal != 0.5 {
		t.Fatalf("known cost = %v", rep.Cost.KnownCostTotal)
	}
	if rep.Cost.UnknownCostCallCount != 1 {
		t.Fatalf("unknown count = %d", rep.Cost.UnknownCostCallCount)
	}
}

func TestReportIntegrationRunning(t *testing.T) {
	env := newReportIntegrationEnv(t)
	env.seedRun(t, &types.EvaluationRun{
		RunID:        "00000000-0000-0000-0000-000000000004",
		TaskID:       "evaluation-7-default",
		Status:       types.EvaluationRunStatusRunning,
		StartedAt:    timePtr(time.Now().Add(-2 * time.Second)),
		ProtocolHash: "hash-run-0004",
	})

	_, rep := env.getReport(t, "00000000-0000-0000-0000-000000000004")
	if rep.Quality.Availability != types.AvailabilityNotFinal {
		t.Fatalf("quality availability = %q", rep.Quality.Availability)
	}
	if rep.Quality.ReasonCode != types.ReasonRunNotTerminal {
		t.Fatalf("quality reason = %q", rep.Quality.ReasonCode)
	}
	if rep.Latency.Availability != types.AvailabilityNotFinal {
		t.Fatalf("latency availability = %q", rep.Latency.Availability)
	}
	if rep.Quality.Retrieval != nil {
		t.Fatal("running run must not fabricate metrics")
	}
}

func TestReportIntegrationInterrupted(t *testing.T) {
	env := newReportIntegrationEnv(t)
	env.seedRun(t, &types.EvaluationRun{
		RunID:              "00000000-0000-0000-0000-000000000005",
		TaskID:             "evaluation-7-default",
		Status:             types.EvaluationRunStatusInterrupted,
		InterruptionReason: types.InterruptionReasonProcessLost,
		StartedAt:          timePtr(time.Now().Add(-5 * time.Second)),
		EndedAt:            timePtr(time.Now()),
		MetricsValid:       false,
		ProtocolHash:       "hash-run-0005",
	})

	_, rep := env.getReport(t, "00000000-0000-0000-0000-000000000005")
	if rep.Quality.Availability != types.AvailabilityUnknown {
		t.Fatalf("quality availability = %q", rep.Quality.Availability)
	}
	if rep.Quality.ReasonCode != types.ReasonMetricsInvalid {
		t.Fatalf("quality reason = %q", rep.Quality.ReasonCode)
	}
	if rep.Run.InterruptionReason != types.InterruptionReasonProcessLost {
		t.Fatalf("interruption reason = %q", rep.Run.InterruptionReason)
	}
}

func TestReportIntegrationMixedCurrency(t *testing.T) {
	env := newReportIntegrationEnv(t)
	env.seedRun(t, completedRun("00000000-0000-0000-0000-000000000007", true, validMetricsJSON()))
	env.seedCall(t, env.tenant, "00000000-0000-0000-0000-000000000007", func(c *types.ModelCall) {
		c.EstimatedCost = f64Ptr2(1.0)
		c.Currency = "USD"
	})
	env.seedCall(t, env.tenant, "00000000-0000-0000-0000-000000000007", func(c *types.ModelCall) {
		c.EstimatedCost = f64Ptr2(7.0)
		c.Currency = "CNY"
	})

	_, rep := env.getReport(t, "00000000-0000-0000-0000-000000000007")
	if rep.Cost.Availability != types.AvailabilityUnknown {
		t.Fatalf("cost availability = %q", rep.Cost.Availability)
	}
	if rep.Cost.ReasonCode != types.ReasonMixedCurrency {
		t.Fatalf("cost reason = %q", rep.Cost.ReasonCode)
	}
	if rep.Cost.KnownCostTotal != nil {
		t.Fatal("mixed currency must not return a displayable total")
	}
	if !rep.Cost.MixedCurrency {
		t.Fatal("mixed_currency must be true")
	}
}

func TestReportIntegrationNoObservedCall(t *testing.T) {
	env := newReportIntegrationEnv(t)
	env.seedRun(t, completedRun("00000000-0000-0000-0000-000000000008", true, validMetricsJSON()))

	_, rep := env.getReport(t, "00000000-0000-0000-0000-000000000008")
	if rep.Usage.Availability != types.AvailabilityUnknown {
		t.Fatalf("usage availability = %q", rep.Usage.Availability)
	}
	if rep.Usage.ReasonCode != types.ReasonNoObservedModelCall {
		t.Fatalf("usage reason = %q", rep.Usage.ReasonCode)
	}
	if rep.Usage.LogicalCallCount != 0 {
		t.Fatalf("logical calls = %d", rep.Usage.LogicalCallCount)
	}
	if rep.Cost.Availability != types.AvailabilityUnknown {
		t.Fatalf("cost availability = %q", rep.Cost.Availability)
	}
}

func TestReportIntegrationCacheStates(t *testing.T) {
	env := newReportIntegrationEnv(t)
	env.seedRun(t, completedRun("00000000-0000-0000-0000-000000000009", true, validMetricsJSON()))
	env.seedCall(t, env.tenant, "00000000-0000-0000-0000-000000000009", func(c *types.ModelCall) {
		c.CacheStatus = types.PromptCacheStatusHit
		c.CacheReadTokens = intPtr2(30)
		c.CacheReportedInputTokens = intPtr2(100)
	})
	env.seedCall(t, env.tenant, "00000000-0000-0000-0000-000000000009", func(c *types.ModelCall) {
		c.CacheStatus = types.PromptCacheStatusMiss
	})
	env.seedCall(t, env.tenant, "00000000-0000-0000-0000-000000000009", func(c *types.ModelCall) {
		c.CacheStatus = types.PromptCacheStatusUnsupported
	})

	_, rep := env.getReport(t, "00000000-0000-0000-0000-000000000009")
	pc := rep.SupportingObservation.PromptCache
	if pc == nil {
		t.Fatal("prompt cache support must be present")
	}
	if pc.EligibleCount != 2 || pc.ReportedCount != 2 || pc.UnsupportedCount != 1 {
		t.Fatalf("cache counts = eligible %d reported %d unsupported %d", pc.EligibleCount, pc.ReportedCount, pc.UnsupportedCount)
	}
	// Local embedding cache is DISABLED because the config was nil; it must be
	// reported as a real DISABLED state, never a fabricated 0% hit rate.
	if rep.SupportingObservation.LocalEmbeddingCache == nil || rep.SupportingObservation.LocalEmbeddingCache.ImplementationStatus != string(types.EmbeddingCacheImplementationDisabled) {
		t.Fatalf("local cache = %+v", rep.SupportingObservation.LocalEmbeddingCache)
	}
}

func TestReportIntegrationCrossTenant404(t *testing.T) {
	env := newReportIntegrationEnv(t)
	env.seedRun(t, completedRun("00000000-0000-0000-0000-000000000010", true, validMetricsJSON()))

	// Query the same run_id from a different tenant: must be 404 and must not
	// leak the existence of tenant A's run to tenant B.
	env.tenant = 8
	code, _ := env.getReport(t, "00000000-0000-0000-0000-000000000010")
	if code != http.StatusNotFound {
		t.Fatalf("cross-tenant expected 404, got %d", code)
	}
}

func TestReportIntegrationRunNotFoundFailClosed(t *testing.T) {
	env := newReportIntegrationEnv(t)
	code, _ := env.getReport(t, "00000000-0000-0000-0000-00000000dead")
	if code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", code)
	}
}

func TestReportIntegrationMalformedRunID400(t *testing.T) {
	env := newReportIntegrationEnv(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/evaluation/runs/not-a-uuid/report", nil)
	env.engine.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func timePtr(t time.Time) *time.Time { return &t }
