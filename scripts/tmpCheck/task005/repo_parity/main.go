// Command repo_parity runs the Task005 embedding-cache repository/failure
// behaviour matrix against PostgreSQL using the FORMAL versioned migrations
// (applied by run_postgres_experiment.sh). It covers the behaviours the SQLite
// repository tests already assert — restart, corruption, lookup/write failure,
// concurrent unique conflict, tenant/model purge — so PostgreSQL has complete
// behaviour parity rather than only the main OFF/COLD/WARM experiment.
//
// It never prints raw text preimages, full vectors, or credentials.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const dimension = 3

var failures int

func run(name string, fn func() error) {
	if err := fn(); err != nil {
		failures++
		fmt.Printf("FAIL  %-42s %v\n", name, err)
		return
	}
	fmt.Printf("PASS  %-42s\n", name)
}

// detProvider is a deterministic in-process embedding provider.
type detProvider struct{ calls int }

func vectorFor(text string) []float32 {
	sum := sha256.Sum256([]byte(text))
	return []float32{float32(len(text)), float32(sum[0]) / 255.0, float32(sum[1]) / 255.0}
}
func (d *detProvider) GetModelName() string { return "deterministic-fixture" }
func (d *detProvider) GetModelID() string   { return "fixture-embedding-model" }
func (d *detProvider) GetDimensions() int   { return dimension }
func (d *detProvider) Embed(_ context.Context, text string) ([]float32, error) {
	d.calls++
	return vectorFor(text), nil
}
func (d *detProvider) BatchEmbed(_ context.Context, texts []string) ([][]float32, error) {
	d.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = vectorFor(t)
	}
	return out, nil
}
func (d *detProvider) BatchEmbedWithPool(ctx context.Context, _ embedding.Embedder, texts []string) ([][]float32, error) {
	return d.BatchEmbed(ctx, texts)
}

func cfg() embedding.Config {
	return embedding.Config{
		Source:                    types.ModelSourceRemote,
		BaseURL:                   "https://fixture.invalid/v1",
		ModelName:                 "fixture-embedding-model",
		Dimensions:                dimension,
		SupportsDimensionOverride: true,
		ModelID:                   "fixture-embedding-model",
		Provider:                  "fixture",
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

func mustPut(repo interfaces.EmbeddingCacheRepository, tenantID uint64, key string, vector []float32, id types.EmbeddingCacheIdentity) error {
	payload := "["
	for i, x := range vector {
		if i > 0 {
			payload += ","
		}
		payload += fmt.Sprintf("%g", x)
	}
	payload += "]"
	entry := &types.EmbeddingCacheEntry{
		TenantID:               tenantID,
		CacheKey:               key,
		ModelID:                id.ModelID,
		ProviderIdentity:       id.ProviderIdentity,
		ModelConfigFingerprint: id.ModelConfigFingerprint,
		CacheSchemaVersion:     id.SchemaVersion,
		Dimensions:             len(vector),
		VectorPayload:          payload,
	}
	return repo.PutValidatedEntry(context.Background(), entry)
}

func mustObservation(db *gorm.DB, tenantID uint64, modelID string) error {
	return db.Create(&types.EmbeddingCacheObservation{
		ID:                        fmt.Sprintf("purge-%d-%s", tenantID, modelID),
		TenantID:                  tenantID,
		ModelID:                   modelID,
		Operation:                 "embedding",
		CacheMode:                 types.EmbeddingCacheModeOn,
		LogicalEmbeddingItemCount: 1,
		PersistenceStatus:         types.EmbeddingCachePersistencePersisted,
	}).Error
}

// lookupFailRepo injects a persistent read failure while delegating writes and
// observation lifecycle, modelling a store whose lookup path is down.
type lookupFailRepo struct {
	interfaces.EmbeddingCacheRepository
}

func (r *lookupFailRepo) GetValidEntry(context.Context, uint64, string, types.EmbeddingCacheIdentity) (types.EmbeddingCacheLookup, error) {
	return types.EmbeddingCacheLookup{}, errors.New("lookup down")
}

// writeFailRepo injects a persistent write failure, modelling a full disk.
type writeFailRepo struct {
	interfaces.EmbeddingCacheRepository
}

func (r *writeFailRepo) PutValidatedEntry(context.Context, *types.EmbeddingCacheEntry) error {
	return errors.New("write down")
}

func main() {
	dsn := flag.String("dsn", "", "postgres DSN (formal migrations already applied)")
	flag.Parse()
	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "-dsn is required")
		os.Exit(2)
	}

	db, err := gorm.Open(postgres.Open(*dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fmt.Fprintf(os.Stderr, "open postgres: %v\n", err)
		os.Exit(1)
	}
	repo := repository.NewEmbeddingCacheRepository(db)
	ctx := context.Background()

	tenantSeq := uint64(900000)
	nextTenant := func() uint64 { tenantSeq++; return tenantSeq }

	run("restart_warm_hit", func() error {
		t := nextTenant()
		id := testIdentity()
		if err := mustPut(repo, t, "k1", []float32{1, 2, 3}, id); err != nil {
			return err
		}
		// Fresh repository over a fresh connection = process restart.
		db2, err := gorm.Open(postgres.Open(*dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			return err
		}
		repo2 := repository.NewEmbeddingCacheRepository(db2)
		got, err := repo2.GetValidEntry(ctx, t, "k1", id)
		if err != nil {
			return err
		}
		if got.Entry == nil || got.Corrupt {
			return fmt.Errorf("warm hit lost after restart")
		}
		if len(got.Vector) != 3 || got.Vector[0] != 1 || got.Vector[1] != 2 || got.Vector[2] != 3 {
			return fmt.Errorf("restart returned wrong vector: %v", got.Vector)
		}
		return nil
	})

	run("corruption_dimension_mismatch", func() error {
		t := nextTenant()
		id := testIdentity()
		if err := db.Exec(`INSERT INTO embedding_cache_entries (id, tenant_id, cache_key, model_id, provider_identity, model_config_fingerprint, vector_payload, dimensions, cache_schema_version) VALUES ('e1', ?, 'k1', 'model-a', 'remote|openai|https://api.openai.com/v1', 'fp-1', '[1,2,3,4]', 3, 1)`, t).Error; err != nil {
			return err
		}
		got, err := repo.GetValidEntry(ctx, t, "k1", id)
		if err != nil {
			return err
		}
		if !got.Corrupt || got.Entry != nil {
			return fmt.Errorf("expected dimension mismatch corrupt, got %+v", got)
		}
		var n int64
		if err := db.Model(&types.EmbeddingCacheEntry{}).Where("tenant_id = ? AND cache_key = ?", t, "k1").Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("corrupt row not best-effort deleted, count=%d", n)
		}
		return nil
	})

	run("corruption_identity_mismatch", func() error {
		t := nextTenant()
		id := testIdentity()
		if err := mustPut(repo, t, "k1", []float32{1, 2, 3}, id); err != nil {
			return err
		}
		other := id
		other.ModelConfigFingerprint = "fp-2"
		got, err := repo.GetValidEntry(ctx, t, "k1", other)
		if err != nil {
			return err
		}
		if !got.Corrupt || got.Entry != nil {
			return fmt.Errorf("expected identity mismatch corrupt, got %+v", got)
		}
		return nil
	})

	run("corruption_malformed_payload", func() error {
		t := nextTenant()
		id := testIdentity()
		if err := db.Exec(`INSERT INTO embedding_cache_entries (id, tenant_id, cache_key, model_id, provider_identity, model_config_fingerprint, vector_payload, dimensions, cache_schema_version) VALUES ('e1', ?, 'k1', 'model-a', 'remote|openai|https://api.openai.com/v1', 'fp-1', 'not-json', 3, 1)`, t).Error; err != nil {
			return err
		}
		got, err := repo.GetValidEntry(ctx, t, "k1", id)
		if err != nil {
			return err
		}
		if !got.Corrupt || got.Entry != nil {
			return fmt.Errorf("malformed payload must be rejected, got %+v", got)
		}
		return nil
	})

	run("duplicate_key_single_entry", func() error {
		t := nextTenant()
		id := testIdentity()
		if err := mustPut(repo, t, "k1", []float32{1, 2, 3}, id); err != nil {
			return err
		}
		if err := mustPut(repo, t, "k1", []float32{9, 9, 9}, id); err != nil {
			return err
		}
		var n int64
		if err := db.Model(&types.EmbeddingCacheEntry{}).Where("tenant_id = ? AND cache_key = ?", t, "k1").Count(&n).Error; err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("expected 1 entry, got %d", n)
		}
		return nil
	})

	run("tenant_isolation", func() error {
		a, b := nextTenant(), nextTenant()
		id := testIdentity()
		if err := mustPut(repo, a, "k1", []float32{1, 2, 3}, id); err != nil {
			return err
		}
		got, err := repo.GetValidEntry(ctx, b, "k1", id)
		if err != nil {
			return err
		}
		if got.Entry != nil || got.Corrupt {
			return fmt.Errorf("cross-tenant read returned entry")
		}
		if err := repo.DeleteByTenant(ctx, b); err != nil {
			return err
		}
		got, err = repo.GetValidEntry(ctx, a, "k1", id)
		if err != nil {
			return err
		}
		if got.Entry == nil {
			return fmt.Errorf("tenant A entry deleted by tenant B purge")
		}
		return nil
	})

	run("observation_aggregate_complete", func() error {
		t := nextTenant()
		finalize := func(obsID string, f types.EmbeddingCacheObservationFinalize) error {
			return repo.FinalizeObservation(ctx, t, obsID, f)
		}
		o1 := &types.EmbeddingCacheObservation{TenantID: t, ModelID: "model-a", Operation: "embedding", CacheMode: types.EmbeddingCacheModeOn, LogicalEmbeddingItemCount: 4}
		if err := repo.BeginObservation(ctx, o1); err != nil {
			return err
		}
		if err := finalize(o1.ID, types.EmbeddingCacheObservationFinalize{LocalEmbeddingHitCount: 3, LocalEmbeddingMissCount: 1, ProviderBoundModelCallCount: 1}); err != nil {
			return err
		}
		o2 := &types.EmbeddingCacheObservation{TenantID: t, ModelID: "model-a", Operation: "embedding", CacheMode: types.EmbeddingCacheModeOn, LogicalEmbeddingItemCount: 4}
		if err := repo.BeginObservation(ctx, o2); err != nil {
			return err
		}
		if err := finalize(o2.ID, types.EmbeddingCacheObservationFinalize{LocalEmbeddingHitCount: 4, LocalEmbeddingMissCount: 0, ProviderBoundModelCallCount: 0}); err != nil {
			return err
		}
		// Other tenant must not pollute.
		other := nextTenant()
		o3 := &types.EmbeddingCacheObservation{TenantID: other, ModelID: "model-a", Operation: "embedding", CacheMode: types.EmbeddingCacheModeOn, LogicalEmbeddingItemCount: 4}
		if err := repo.BeginObservation(ctx, o3); err != nil {
			return err
		}
		if err := repo.FinalizeObservation(ctx, other, o3.ID, types.EmbeddingCacheObservationFinalize{LocalEmbeddingHitCount: 1, LocalEmbeddingMissCount: 3, ProviderBoundModelCallCount: 1}); err != nil {
			return err
		}
		agg, err := repo.AggregateObservations(ctx, types.EmbeddingCacheObservationFilter{TenantID: t})
		if err != nil {
			return err
		}
		if agg.BatchInvocationCount != 2 || agg.LogicalItemCount != 8 || agg.HitCount != 7 || agg.MissCount != 1 || agg.ProviderBoundModelCallCount != 1 {
			return fmt.Errorf("aggregate counts wrong: %+v", agg)
		}
		if agg.MeasurementStatus != types.MeasurementHealthComplete {
			return fmt.Errorf("expected COMPLETE, got %s", agg.MeasurementStatus)
		}
		return nil
	})

	run("unfinalized_partial", func() error {
		t := nextTenant()
		obs := &types.EmbeddingCacheObservation{TenantID: t, ModelID: "model-a", Operation: "embedding", CacheMode: types.EmbeddingCacheModeOn, LogicalEmbeddingItemCount: 2}
		if err := repo.BeginObservation(ctx, obs); err != nil {
			return err
		}
		agg, err := repo.AggregateObservations(ctx, types.EmbeddingCacheObservationFilter{TenantID: t})
		if err != nil {
			return err
		}
		if agg.MeasurementStatus != types.MeasurementHealthPartial {
			return fmt.Errorf("expected PARTIAL for unfinalized, got %s", agg.MeasurementStatus)
		}
		return nil
	})

	run("concurrent_put_single_winner", func() error {
		t := nextTenant()
		id := testIdentity()
		const n = 20
		var wg sync.WaitGroup
		var mu sync.Mutex
		var firstErr error
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(seed int) {
				defer wg.Done()
				if err := mustPut(repo, t, "k1", []float32{float32(seed), 2, 3}, id); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}(i)
		}
		wg.Wait()
		if firstErr != nil {
			return firstErr
		}
		var count int64
		if err := db.Model(&types.EmbeddingCacheEntry{}).Where("tenant_id = ? AND cache_key = ?", t, "k1").Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("expected single winner, got %d", count)
		}
		return nil
	})

	run("purge_matrix_tenant", func() error {
		a, b := nextTenant(), nextTenant()
		id := testIdentity()
		if err := mustPut(repo, a, "k1", []float32{1, 2, 3}, id); err != nil {
			return err
		}
		if err := mustPut(repo, b, "k1", []float32{1, 2, 3}, id); err != nil {
			return err
		}
		if err := mustObservation(db, a, "model-a"); err != nil {
			return err
		}
		if err := mustObservation(db, b, "model-a"); err != nil {
			return err
		}
		if err := repo.DeleteByTenant(ctx, a); err != nil {
			return err
		}
		var na, nb, oa, ob int64
		if err := db.Model(&types.EmbeddingCacheEntry{}).Where("tenant_id = ?", a).Count(&na).Error; err != nil {
			return err
		}
		if err := db.Model(&types.EmbeddingCacheEntry{}).Where("tenant_id = ?", b).Count(&nb).Error; err != nil {
			return err
		}
		if err := db.Model(&types.EmbeddingCacheObservation{}).Where("tenant_id = ?", a).Count(&oa).Error; err != nil {
			return err
		}
		if err := db.Model(&types.EmbeddingCacheObservation{}).Where("tenant_id = ?", b).Count(&ob).Error; err != nil {
			return err
		}
		if na != 0 || nb != 1 || oa != 0 || ob != 1 {
			return fmt.Errorf("tenant purge scoping wrong: entries(A=%d B=%d) observations(A=%d B=%d)", na, nb, oa, ob)
		}
		return nil
	})

	run("purge_matrix_model", func() error {
		t := nextTenant()
		ida := testIdentity()
		idb := ida
		idb.ModelID = "model-b"
		if err := mustPut(repo, t, "ka", []float32{1, 2, 3}, ida); err != nil {
			return err
		}
		if err := mustPut(repo, t, "kb", []float32{1, 2, 3}, idb); err != nil {
			return err
		}
		if err := mustObservation(db, t, "model-a"); err != nil {
			return err
		}
		if err := mustObservation(db, t, "model-b"); err != nil {
			return err
		}
		if err := repo.DeleteByModel(ctx, t, "model-a"); err != nil {
			return err
		}
		var nA, nB, oA, oB int64
		if err := db.Model(&types.EmbeddingCacheEntry{}).Where("tenant_id = ? AND model_id = ?", t, "model-a").Count(&nA).Error; err != nil {
			return err
		}
		if err := db.Model(&types.EmbeddingCacheEntry{}).Where("tenant_id = ? AND model_id = ?", t, "model-b").Count(&nB).Error; err != nil {
			return err
		}
		if err := db.Model(&types.EmbeddingCacheObservation{}).Where("tenant_id = ? AND model_id = ?", t, "model-a").Count(&oA).Error; err != nil {
			return err
		}
		if err := db.Model(&types.EmbeddingCacheObservation{}).Where("tenant_id = ? AND model_id = ?", t, "model-b").Count(&oB).Error; err != nil {
			return err
		}
		if nA != 0 || nB != 1 || oA != 0 || oB != 1 {
			return fmt.Errorf("model purge scoping wrong: entries(a=%d b=%d) observations(a=%d b=%d)", nA, nB, oA, oB)
		}
		return nil
	})

	run("lookup_failure_recorded", func() error {
		t := nextTenant()
		bad := &lookupFailRepo{EmbeddingCacheRepository: repo}
		provider := &detProvider{}
		emb := embedding.WrapEmbeddingCache(provider, embedding.CacheOptions{Enabled: true, TenantID: t, Store: bad, Observer: bad, Config: cfg()})
		vec, err := emb.Embed(ctx, "lookup-failure-probe")
		if err != nil {
			return fmt.Errorf("business result should not fail on lookup error: %v", err)
		}
		if len(vec) == 0 {
			return fmt.Errorf("empty vector returned")
		}
		agg, err := repo.AggregateObservations(ctx, types.EmbeddingCacheObservationFilter{TenantID: t})
		if err != nil {
			return err
		}
		if agg.LookupFailedCount != 1 {
			return fmt.Errorf("expected lookup_failed=1, got %d", agg.LookupFailedCount)
		}
		return nil
	})

	run("write_failure_recorded", func() error {
		t := nextTenant()
		bad := &writeFailRepo{EmbeddingCacheRepository: repo}
		provider := &detProvider{}
		emb := embedding.WrapEmbeddingCache(provider, embedding.CacheOptions{Enabled: true, TenantID: t, Store: bad, Observer: bad, Config: cfg()})
		vec, err := emb.Embed(ctx, "write-failure-probe")
		if err != nil {
			return fmt.Errorf("business result should not fail on write error: %v", err)
		}
		if len(vec) == 0 {
			return fmt.Errorf("empty vector returned")
		}
		agg, err := repo.AggregateObservations(ctx, types.EmbeddingCacheObservationFilter{TenantID: t})
		if err != nil {
			return err
		}
		if agg.WriteFailedCount != 1 {
			return fmt.Errorf("expected write_failed=1, got %d", agg.WriteFailedCount)
		}
		return nil
	})

	fmt.Printf("\n%d checks, %d failures\n", 13, failures)
	if failures > 0 {
		os.Exit(1)
	}
}
