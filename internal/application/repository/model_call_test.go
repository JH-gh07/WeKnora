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

func newModelCallTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "model-calls.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.ModelCall{}); err != nil {
		t.Fatalf("migrate calls: %v", err)
	}
	if err := db.Exec(`CREATE TABLE model_metering_health (id VARCHAR(36) PRIMARY KEY, tenant_id INTEGER NOT NULL, attempted_at DATETIME NOT NULL, persisted BOOLEAN NOT NULL)`).Error; err != nil {
		t.Fatalf("create health: %v", err)
	}
	return db
}

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }
func stringPtr(v string) *string  { return &v }

func TestModelCallRepositoryNullSafeAggregateAndTenantIsolation(t *testing.T) {
	db := newModelCallTestDB(t)
	repo := NewModelCallRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	runID := "run-a"
	calls := []*types.ModelCall{
		{ID: "known", TenantID: 1, RunID: &runID, ModelID: "chat-a", ModelName: "chat", Provider: "fixture", Operation: types.ModelOperationChat, InputTokens: intPtr(10), OutputTokens: intPtr(5), CacheReadTokens: intPtr(2), CacheReportedInputTokens: intPtr(10), UsageFinality: types.UsageFinalityReported, CacheStatus: types.PromptCacheStatusHit, Success: true, EstimatedCost: floatPtr(0.125), Currency: "USD", PricingVersion: "fixture-v1", PricingSource: "test", PricingEffectiveAt: &now, AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: now},
		{ID: "unknown", TenantID: 1, ModelID: "rerank-a", ModelName: "rerank", Provider: "fixture", Operation: types.ModelOperationRerank, UsageFinality: types.UsageFinalityUnavailable, CacheStatus: types.PromptCacheStatusUnsupported, Success: false, ErrorType: "provider_error", AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: now.Add(time.Second)},
		{ID: "other-tenant", TenantID: 2, ModelID: "chat-a", Operation: types.ModelOperationChat, UsageFinality: types.UsageFinalityUnavailable, CacheStatus: types.PromptCacheStatusUnreported, Success: true, AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: now},
	}
	for _, call := range calls {
		if err := repo.CreateModelCall(ctx, call); err != nil {
			t.Fatalf("create %s: %v", call.ID, err)
		}
	}

	if _, err := repo.GetModelCall(ctx, 2, "known"); !errors.Is(err, ErrModelCallNotFound) {
		t.Fatalf("cross-tenant read: %v", err)
	}
	unknown, err := repo.GetModelCall(ctx, 1, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if unknown.RunID != nil || unknown.InputTokens != nil || unknown.EstimatedCost != nil {
		t.Fatalf("unknown values changed: %+v", unknown)
	}

	from, to := now.Add(-time.Minute), now.Add(time.Minute)
	if err := repo.RecordMeteringAttempt(ctx, 1, now, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordMeteringAttempt(ctx, 1, now, true); err != nil {
		t.Fatal(err)
	}
	agg, err := repo.AggregateModelCalls(ctx, types.ModelCallFilter{TenantID: 1, From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	if agg.LogicalCallCount != 2 || agg.SuccessCount != 1 || agg.FailureCount != 1 {
		t.Fatalf("counts: %+v", agg)
	}
	if agg.InputTokens == nil || *agg.InputTokens != 10 || agg.OutputTokens == nil || *agg.OutputTokens != 5 {
		t.Fatalf("usage: %+v", agg)
	}
	if agg.KnownCostTotal == nil || *agg.KnownCostTotal != 0.125 || agg.UnknownCostCallCount != 1 {
		t.Fatalf("cost: %+v", agg)
	}
	if agg.Currency == nil || *agg.Currency != "USD" || agg.MixedCurrency {
		t.Fatalf("currency identity: %+v", agg)
	}
	if agg.CacheReportedInputTokens == nil || *agg.CacheReportedInputTokens != 10 {
		t.Fatalf("reported input denominator: %+v", agg)
	}
	if agg.CacheReportedCount != 1 || agg.CacheUnsupportedCount != 1 {
		t.Fatalf("cache: %+v", agg)
	}
	if agg.MeasurementStatus != types.MeasurementHealthComplete || agg.MeteringPersistedCount != 2 {
		t.Fatalf("health: %+v", agg)
	}

	runAgg, err := repo.AggregateModelCalls(ctx, types.ModelCallFilter{TenantID: 1, RunID: &runID, From: &from, To: &to})
	if err != nil || runAgg.LogicalCallCount != 1 {
		t.Fatalf("run aggregate: %+v err=%v", runAgg, err)
	}
	otherAgg, err := repo.AggregateModelCalls(ctx, types.ModelCallFilter{TenantID: 2, From: &from, To: &to})
	if err != nil || otherAgg.LogicalCallCount != 1 {
		t.Fatalf("tenant aggregate: %+v err=%v", otherAgg, err)
	}
}

func TestModelCallRepositoryAggregateFailsClosedForMixedCurrency(t *testing.T) {
	db := newModelCallTestDB(t)
	repo := NewModelCallRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	for _, call := range []*types.ModelCall{
		{ID: "usd", TenantID: 11, ModelID: "m", Operation: types.ModelOperationChat, EstimatedCost: floatPtr(1), Currency: "USD", CacheStatus: types.PromptCacheStatusUnsupported, AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: now},
		{ID: "eur", TenantID: 11, ModelID: "m", Operation: types.ModelOperationChat, EstimatedCost: floatPtr(2), Currency: "EUR", CacheStatus: types.PromptCacheStatusUnsupported, AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: now},
	} {
		if err := repo.CreateModelCall(context.Background(), call); err != nil {
			t.Fatal(err)
		}
	}
	from, to := now.Add(-time.Minute), now.Add(time.Minute)
	agg, err := repo.AggregateModelCalls(context.Background(), types.ModelCallFilter{TenantID: 11, From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	if agg.KnownCostTotal != nil || agg.Currency != nil || !agg.MixedCurrency {
		t.Fatalf("mixed currency must fail closed: %+v", agg)
	}
}

func TestModelCallRepositoryAggregateFailsClosedForMissingCurrency(t *testing.T) {
	db := newModelCallTestDB(t)
	repo := NewModelCallRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	call := &types.ModelCall{ID: "missing-currency", TenantID: 12, ModelID: "m", Operation: types.ModelOperationChat, EstimatedCost: floatPtr(1), CacheStatus: types.PromptCacheStatusUnsupported, AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: now}
	if err := repo.CreateModelCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	from, to := now.Add(-time.Minute), now.Add(time.Minute)
	agg, err := repo.AggregateModelCalls(context.Background(), types.ModelCallFilter{TenantID: 12, From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	if agg.KnownCostTotal != nil || agg.Currency != nil || agg.MixedCurrency {
		t.Fatalf("missing currency must fail closed: %+v", agg)
	}
}

func TestModelCallRepositoryAllUnknownStaysNullAndHealthDegrades(t *testing.T) {
	repo := NewModelCallRepository(newModelCallTestDB(t))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	call := &types.ModelCall{ID: "unknown-only", TenantID: 7, ModelID: "embed", Operation: types.ModelOperationEmbedding, UsageFinality: types.UsageFinalityUnavailable, CacheStatus: types.PromptCacheStatusUnreported, Success: true, AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: now}
	if err := repo.CreateModelCall(ctx, call); err != nil {
		t.Fatal(err)
	}
	from, to := now.Add(-time.Minute), now.Add(time.Minute)
	agg, err := repo.AggregateModelCalls(ctx, types.ModelCallFilter{TenantID: 7, From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	if agg.InputTokens != nil || agg.OutputTokens != nil || agg.KnownCostTotal != nil {
		t.Fatalf("unknown aggregate became zero: %+v", agg)
	}
	if agg.MeasurementStatus != types.MeasurementHealthUnknown {
		t.Fatalf("no health fact must be UNKNOWN: %+v", agg)
	}
	if err := repo.RecordMeteringAttempt(ctx, 7, now, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordMeteringAttempt(ctx, 7, now, false); err != nil {
		t.Fatal(err)
	}
	health, err := repo.GetMeasurementHealth(ctx, 7, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != types.MeasurementHealthPartial || health.MeteringAttemptedCount != 2 || health.MeteringPersistedCount != 1 || health.MeteringFailedCount != 1 {
		t.Fatalf("partial health: %+v", health)
	}
}

func minimalModelCall(tenant uint64, at time.Time) *types.ModelCall {
	return &types.ModelCall{TenantID: tenant, ModelID: "chat-a", ModelName: "chat", Provider: "fixture", Operation: types.ModelOperationChat, UsageFinality: types.UsageFinalityUnavailable, CacheStatus: types.PromptCacheStatusUnreported, Success: true, AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: at}
}

// TestModelCallRepositoryDurableStartedLifecycle verifies the STARTED ->
// PERSISTED transition: BeginModelCall writes a not-persisted marker (window
// reads PARTIAL until finalize), and RecordModelCall finalizes it to persisted
// in the same transaction that inserts the call.
func TestModelCallRepositoryDurableStartedLifecycle(t *testing.T) {
	db := newModelCallTestDB(t)
	repo := NewModelCallRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Minute), now.Add(time.Minute)

	healthID, err := repo.BeginModelCall(ctx, 5, now)
	if err != nil || healthID == "" {
		t.Fatalf("begin: id=%q err=%v", healthID, err)
	}
	before, err := repo.GetMeasurementHealth(ctx, 5, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != types.MeasurementHealthPartial || before.MeteringAttemptedCount != 1 || before.MeteringPersistedCount != 0 || before.MeteringFailedCount != 1 {
		t.Fatalf("started marker must read PARTIAL: %+v", before)
	}

	call := minimalModelCall(5, now)
	if err := repo.RecordModelCall(ctx, healthID, call); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetModelCall(ctx, 5, call.ID); err != nil {
		t.Fatalf("call not persisted: %v", err)
	}
	after, err := repo.GetMeasurementHealth(ctx, 5, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != types.MeasurementHealthComplete || after.MeteringPersistedCount != 1 || after.MeteringFailedCount != 0 {
		t.Fatalf("finalized marker must read COMPLETE: %+v", after)
	}
}

// TestModelCallRepositoryFinalizeFailClosed ensures a missing marker rolls back
// the call insert and leaves the not-persisted marker behind, so the window
// degrades to PARTIAL instead of silently COMPLETE.
func TestModelCallRepositoryFinalizeFailClosed(t *testing.T) {
	db := newModelCallTestDB(t)
	repo := NewModelCallRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Minute), now.Add(time.Minute)

	healthID, err := repo.BeginModelCall(ctx, 5, now)
	if err != nil {
		t.Fatal(err)
	}
	call := minimalModelCall(5, now)
	if err := repo.RecordModelCall(ctx, "bogus-health-id", call); !errors.Is(err, ErrModelCallHealthNotFound) {
		t.Fatalf("expected health-not-found, got %v", err)
	}
	if _, err := repo.GetModelCall(ctx, 5, call.ID); !errors.Is(err, ErrModelCallNotFound) {
		t.Fatalf("call should have rolled back, got %v", err)
	}
	health, err := repo.GetMeasurementHealth(ctx, 5, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != types.MeasurementHealthPartial || health.MeteringFailedCount != 1 {
		t.Fatalf("orphaned marker must read PARTIAL: %+v", health)
	}
	_ = healthID
}

// TestModelCallRepositoryCancelledContextStillPersists verifies that a
// cancelled business context cannot drop the closing metering write: the
// detached context keeps values and drops cancellation.
func TestModelCallRepositoryCancelledContextStillPersists(t *testing.T) {
	db := newModelCallTestDB(t)
	repo := NewModelCallRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Minute), now.Add(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())

	healthID, err := repo.BeginModelCall(ctx, 6, now)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	call := minimalModelCall(6, now)
	if err := repo.RecordModelCall(ctx, healthID, call); err != nil {
		t.Fatalf("cancelled context dropped metering write: %v", err)
	}
	verifyCtx := context.Background()
	if _, err := repo.GetModelCall(verifyCtx, 6, call.ID); err != nil {
		t.Fatalf("call not persisted after cancel: %v", err)
	}
	health, err := repo.GetMeasurementHealth(verifyCtx, 6, from, to)
	if err != nil || health.Status != types.MeasurementHealthComplete {
		t.Fatalf("health after cancel: %+v err=%v", health, err)
	}
}

// TestModelCallRepositoryRecordWithoutMarkerFallsBack covers the DB-was-down-at-
// start path: RecordModelCall with an empty marker persists the call and a
// persisted health fact in a single best-effort transaction.
func TestModelCallRepositoryRecordWithoutMarkerFallsBack(t *testing.T) {
	repo := NewModelCallRepository(newModelCallTestDB(t))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Minute), now.Add(time.Minute)

	call := minimalModelCall(9, now)
	if err := repo.RecordModelCall(ctx, "", call); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetModelCall(ctx, 9, call.ID); err != nil {
		t.Fatalf("fallback call not persisted: %v", err)
	}
	health, err := repo.GetMeasurementHealth(ctx, 9, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != types.MeasurementHealthComplete || health.MeteringAttemptedCount != 1 || health.MeteringPersistedCount != 1 {
		t.Fatalf("fallback health: %+v", health)
	}
}

func TestModelCallHistoricalPricingIdentityIsImmutable(t *testing.T) {
	db := newModelCallTestDB(t)
	repo := NewModelCallRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	call := &types.ModelCall{ID: "priced", TenantID: 3, ModelID: "m", Operation: types.ModelOperationChat, UsageFinality: types.UsageFinalityReported, CacheStatus: types.PromptCacheStatusUnsupported, Success: true, EstimatedCost: floatPtr(1.25), Currency: "USD", PricingVersion: "2026-01", PricingSource: "fixture-a", PricingEffectiveAt: &now, AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: now}
	if err := repo.CreateModelCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	// A hypothetical current-price table/config change cannot affect the copied
	// fields because reads and aggregates only use the immutable call row.
	got, err := repo.GetModelCall(context.Background(), 3, "priced")
	if err != nil {
		t.Fatal(err)
	}
	if got.PricingVersion != "2026-01" || got.PricingSource != "fixture-a" || got.EstimatedCost == nil || *got.EstimatedCost != 1.25 {
		t.Fatalf("pricing identity drifted: %+v", got)
	}
}
