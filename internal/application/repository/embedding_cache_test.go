package repository

import (
	"context"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newEmbeddingCacheTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "embedding-cache.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&types.EmbeddingCacheEntry{}, &types.EmbeddingCacheObservation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_embedding_cache_entries_tenant_key ON embedding_cache_entries (tenant_id, cache_key)`).Error; err != nil {
		t.Fatalf("unique index: %v", err)
	}
	return db
}

func mustPut(t *testing.T, repo *embeddingCacheRepository, tenantID uint64, key string, vector []float32, identity types.EmbeddingCacheIdentity) {
	t.Helper()
	payload, err := encodeVector(vector)
	if err != nil {
		t.Fatal(err)
	}
	entry := &types.EmbeddingCacheEntry{
		TenantID:               tenantID,
		CacheKey:               key,
		ModelID:                identity.ModelID,
		ProviderIdentity:       identity.ProviderIdentity,
		ModelConfigFingerprint: identity.ModelConfigFingerprint,
		CacheSchemaVersion:     identity.SchemaVersion,
		Dimensions:             len(vector),
		VectorPayload:          payload,
	}
	if err := repo.PutValidatedEntry(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
}

func testIdentity() types.EmbeddingCacheIdentity {
	return types.EmbeddingCacheIdentity{
		ModelID:                "model-a",
		ProviderIdentity:       "remote|openai|https://api.openai.com/v1",
		ModelConfigFingerprint: "fp-1",
		SchemaVersion:          1,
	}
}

func TestEmbeddingCacheRepositoryTenantIsolation(t *testing.T) {
	db := newEmbeddingCacheTestDB(t)
	repo := NewEmbeddingCacheRepository(db).(*embeddingCacheRepository)
	id := testIdentity()
	mustPut(t, repo, 1, "k1", []float32{1, 2, 3}, id)

	// tenant 2 cannot read tenant 1's entry.
	got, err := repo.GetValidEntry(context.Background(), 2, "k1", id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry != nil || got.Corrupt {
		t.Fatalf("cross-tenant read returned entry: %+v", got)
	}
	// tenant 1 can read its own entry.
	got, err = repo.GetValidEntry(context.Background(), 1, "k1", id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry == nil {
		t.Fatal("own entry not found")
	}

	// DeleteByTenant(2) does not affect tenant 1.
	if err := repo.DeleteByTenant(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetValidEntry(context.Background(), 1, "k1", id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry == nil {
		t.Fatal("tenant 1 entry deleted by tenant 2 purge")
	}
}

func TestEmbeddingCacheRepositoryDuplicateKeySingleEntry(t *testing.T) {
	db := newEmbeddingCacheTestDB(t)
	repo := NewEmbeddingCacheRepository(db).(*embeddingCacheRepository)
	id := testIdentity()
	mustPut(t, repo, 1, "k1", []float32{1, 2, 3}, id)
	// Idempotent second write must not error and must keep one row.
	mustPut(t, repo, 1, "k1", []float32{9, 9, 9}, id)
	var count int64
	if err := db.Model(&types.EmbeddingCacheEntry{}).Where("tenant_id = ? AND cache_key = ?", 1, "k1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 entry, got %d", count)
	}
}

func TestEmbeddingCacheRepositoryDimensionMismatchRejected(t *testing.T) {
	db := newEmbeddingCacheTestDB(t)
	repo := NewEmbeddingCacheRepository(db).(*embeddingCacheRepository)
	id := testIdentity()
	// Insert a row whose stored dimensions disagree with its payload length.
	if err := db.Exec(`INSERT INTO embedding_cache_entries (id, tenant_id, cache_key, model_id, provider_identity, model_config_fingerprint, vector_payload, dimensions, cache_schema_version) VALUES ('e1', 1, 'k1', 'model-a', 'remote|openai|https://api.openai.com/v1', 'fp-1', '[1,2,3,4]', 3, 1)`).Error; err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetValidEntry(context.Background(), 1, "k1", id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Corrupt || got.Entry != nil {
		t.Fatalf("expected dimension-mismatch corrupt, got %+v", got)
	}
	// Corrupt row is best-effort deleted.
	var count int64
	if err := db.Model(&types.EmbeddingCacheEntry{}).Where("tenant_id = ? AND cache_key = ?", 1, "k1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("corrupt entry not deleted, count=%d", count)
	}
}

func TestEmbeddingCacheRepositoryIdentityMismatchRejected(t *testing.T) {
	db := newEmbeddingCacheTestDB(t)
	repo := NewEmbeddingCacheRepository(db).(*embeddingCacheRepository)
	id := testIdentity()
	mustPut(t, repo, 1, "k1", []float32{1, 2, 3}, id)
	other := id
	other.ModelConfigFingerprint = "fp-2"
	got, err := repo.GetValidEntry(context.Background(), 1, "k1", other)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Corrupt || got.Entry != nil {
		t.Fatalf("expected identity mismatch corrupt, got %+v", got)
	}
}

func TestEmbeddingCacheRepositoryFloat32RoundTrip(t *testing.T) {
	db := newEmbeddingCacheTestDB(t)
	repo := NewEmbeddingCacheRepository(db).(*embeddingCacheRepository)
	id := testIdentity()
	original := []float32{0.1, -2.5, 1.0 / 3.0}
	mustPut(t, repo, 1, "k1", original, id)
	got, err := repo.GetValidEntry(context.Background(), 1, "k1", id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry == nil {
		t.Fatal("entry missing")
	}
	decoded, err := decodeVector(got.Entry.VectorPayload)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("dimension mismatch: %d vs %d", len(decoded), len(original))
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Fatalf("float32 round-trip mismatch at %d: %v vs %v", i, decoded[i], original[i])
		}
	}
}

func TestEmbeddingCacheRepositoryNonFiniteEncodeRejected(t *testing.T) {
	// JSON cannot represent NaN/Inf, so encoding a non-finite vector must fail
	// rather than persist a corrupt payload.
	if _, err := encodeVector([]float32{1, float32(math.NaN()), 3}); err == nil {
		t.Fatal("expected NaN vector encoding to fail")
	}
	if _, err := encodeVector([]float32{1, float32(math.Inf(1)), 3}); err == nil {
		t.Fatal("expected Inf vector encoding to fail")
	}
}

func TestEmbeddingCacheRepositoryMalformedPayloadRejected(t *testing.T) {
	db := newEmbeddingCacheTestDB(t)
	repo := NewEmbeddingCacheRepository(db).(*embeddingCacheRepository)
	id := testIdentity()
	// Insert a malformed payload directly (bypassing encodeVector) to prove the
	// read-side validation rejects it and best-effort deletes it.
	if err := db.Exec(`INSERT INTO embedding_cache_entries (id, tenant_id, cache_key, model_id, provider_identity, model_config_fingerprint, vector_payload, dimensions, cache_schema_version) VALUES ('e1', 1, 'k1', 'model-a', 'remote|openai|https://api.openai.com/v1', 'fp-1', 'not-json', 3, 1)`).Error; err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetValidEntry(context.Background(), 1, "k1", id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Corrupt || got.Entry != nil {
		t.Fatalf("malformed payload must be rejected, got %+v", got)
	}
}

func TestEmbeddingCacheRepositoryObservationAggregate(t *testing.T) {
	db := newEmbeddingCacheTestDB(t)
	repo := NewEmbeddingCacheRepository(db).(*embeddingCacheRepository)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	begin := func(tenant uint64, model string) *types.EmbeddingCacheObservation {
		obs := &types.EmbeddingCacheObservation{
			TenantID:                  tenant,
			ModelID:                   model,
			Operation:                 "embedding",
			CacheMode:                 types.EmbeddingCacheModeOn,
			LogicalEmbeddingItemCount: 4,
			CreatedAt:                 now,
		}
		if err := repo.BeginObservation(ctx, obs); err != nil {
			t.Fatal(err)
		}
		return obs
	}
	finalize := func(tenant uint64, obsID string, f types.EmbeddingCacheObservationFinalize) {
		if err := repo.FinalizeObservation(ctx, tenant, obsID, f); err != nil {
			t.Fatal(err)
		}
	}

	o1 := begin(1, "model-a")
	finalize(1, o1.ID, types.EmbeddingCacheObservationFinalize{
		LocalEmbeddingHitCount: 3, LocalEmbeddingMissCount: 1, ProviderBoundModelCallCount: 1, RequestElapsedMS: 5,
	})
	o2 := begin(1, "model-a")
	finalize(1, o2.ID, types.EmbeddingCacheObservationFinalize{
		LocalEmbeddingHitCount: 4, LocalEmbeddingMissCount: 0, ProviderBoundModelCallCount: 0, RequestElapsedMS: 2,
	})
	// tenant 2 observation must not pollute tenant 1 aggregate.
	o3 := begin(2, "model-a")
	finalize(2, o3.ID, types.EmbeddingCacheObservationFinalize{LocalEmbeddingHitCount: 1, LocalEmbeddingMissCount: 3, ProviderBoundModelCallCount: 1})

	agg, err := repo.AggregateObservations(ctx, types.EmbeddingCacheObservationFilter{TenantID: 1, From: &now, To: timePtr(now.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	if agg.BatchInvocationCount != 2 || agg.LogicalItemCount != 8 || agg.HitCount != 7 || agg.MissCount != 1 {
		t.Fatalf("aggregate counts wrong: %+v", agg)
	}
	if agg.ProviderBoundModelCallCount != 1 {
		t.Fatalf("provider-bound calls wrong: %+v", agg)
	}
	if agg.MeasurementStatus != types.MeasurementHealthComplete {
		t.Fatalf("expected COMPLETE, got %s", agg.MeasurementStatus)
	}
}

func TestEmbeddingCacheRepositoryUnfinalizedIsPartial(t *testing.T) {
	db := newEmbeddingCacheTestDB(t)
	repo := NewEmbeddingCacheRepository(db).(*embeddingCacheRepository)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	// A STARTED row that is never finalized must make the window PARTIAL.
	if err := repo.BeginObservation(ctx, &types.EmbeddingCacheObservation{
		TenantID: 1, ModelID: "model-a", Operation: "embedding", CacheMode: types.EmbeddingCacheModeOn,
		LogicalEmbeddingItemCount: 2, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	agg, err := repo.AggregateObservations(ctx, types.EmbeddingCacheObservationFilter{TenantID: 1, From: &now, To: timePtr(now.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	if agg.MeasurementStatus != types.MeasurementHealthPartial {
		t.Fatalf("expected PARTIAL for unfinalized, got %s", agg.MeasurementStatus)
	}
}

func TestEmbeddingCacheRepositoryConcurrentPutSingleWinner(t *testing.T) {
	db := newEmbeddingCacheTestDB(t)
	repo := NewEmbeddingCacheRepository(db).(*embeddingCacheRepository)
	id := testIdentity()
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			mustPut(t, repo, 1, "k1", []float32{float32(seed), 2, 3}, id)
		}(i)
	}
	wg.Wait()
	var count int64
	if err := db.Model(&types.EmbeddingCacheEntry{}).Where("tenant_id = ? AND cache_key = ?", 1, "k1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected single winner, got %d", count)
	}
	got, err := repo.GetValidEntry(context.Background(), 1, "k1", id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry == nil || got.Corrupt {
		t.Fatalf("winner must be a valid entry, got %+v", got)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

// TestEmbeddingCacheRepositoryRestartWarmHit proves persistence survives a
// "restart": a brand-new repository instance over the same DB file still reads
// the previously persisted entry (no in-process map).
func TestEmbeddingCacheRepositoryRestartWarmHit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "embedding-cache-restart.db")
	openRepo := func() *embeddingCacheRepository {
		db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		if err := db.AutoMigrate(&types.EmbeddingCacheEntry{}, &types.EmbeddingCacheObservation{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_embedding_cache_entries_tenant_key ON embedding_cache_entries (tenant_id, cache_key)`).Error; err != nil {
			t.Fatalf("unique index: %v", err)
		}
		return NewEmbeddingCacheRepository(db).(*embeddingCacheRepository)
	}

	repo := openRepo()
	id := testIdentity()
	mustPut(t, repo, 1, "k1", []float32{1, 2, 3}, id)

	// Simulate restart: fresh repo over the same file.
	repo2 := openRepo()
	got, err := repo2.GetValidEntry(context.Background(), 1, "k1", id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry == nil || got.Corrupt {
		t.Fatalf("warm hit after restart failed: %+v", got)
	}
	if len(got.Vector) != 3 || got.Vector[0] != 1 || got.Vector[1] != 2 || got.Vector[2] != 3 {
		t.Fatalf("restart returned wrong vector: %v", got.Vector)
	}
}
