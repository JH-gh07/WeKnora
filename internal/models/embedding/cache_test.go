package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

// memCacheStore is a minimal in-memory implementation of both
// types.EmbeddingCacheStore and types.EmbeddingCacheObserver that mirrors the
// repository validation contract (identity match, dimension/finite check,
// idempotent put).
type memCacheStore struct {
	mu              sync.Mutex
	entries         map[uint64]map[string]*types.EmbeddingCacheEntry
	observations    map[string]*types.EmbeddingCacheObservation
	lookupErr       error
	putErr          error
	beginErr        error
	recordFailedErr error
}

func newMemCacheStore() *memCacheStore {
	return &memCacheStore{
		entries:      map[uint64]map[string]*types.EmbeddingCacheEntry{},
		observations: map[string]*types.EmbeddingCacheObservation{},
	}
}

func (m *memCacheStore) GetValidEntry(_ context.Context, tenantID uint64, cacheKey string, expected types.EmbeddingCacheIdentity) (types.EmbeddingCacheLookup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lookupErr != nil {
		return types.EmbeddingCacheLookup{}, m.lookupErr
	}
	byTenant := m.entries[tenantID]
	if byTenant == nil {
		return types.EmbeddingCacheLookup{}, nil
	}
	e, ok := byTenant[cacheKey]
	if !ok {
		return types.EmbeddingCacheLookup{}, nil
	}
	if e.ModelID != expected.ModelID ||
		e.ProviderIdentity != expected.ProviderIdentity ||
		e.ModelConfigFingerprint != expected.ModelConfigFingerprint ||
		e.CacheSchemaVersion != expected.SchemaVersion {
		return types.EmbeddingCacheLookup{Corrupt: true}, nil
	}
	var vec []float32
	if err := json.Unmarshal([]byte(e.VectorPayload), &vec); err != nil {
		return types.EmbeddingCacheLookup{Corrupt: true}, nil
	}
	if len(vec) != e.Dimensions || !finiteVector(vec) {
		return types.EmbeddingCacheLookup{Corrupt: true}, nil
	}
	return types.EmbeddingCacheLookup{Entry: e, Vector: vec}, nil
}

func (m *memCacheStore) PutValidatedEntry(_ context.Context, entry *types.EmbeddingCacheEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putErr != nil {
		return m.putErr
	}
	if m.entries[entry.TenantID] == nil {
		m.entries[entry.TenantID] = map[string]*types.EmbeddingCacheEntry{}
	}
	if _, exists := m.entries[entry.TenantID][entry.CacheKey]; exists {
		return nil // idempotent winner
	}
	cp := *entry
	m.entries[entry.TenantID][entry.CacheKey] = &cp
	return nil
}

func (m *memCacheStore) BeginObservation(_ context.Context, obs *types.EmbeddingCacheObservation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.beginErr != nil {
		return m.beginErr
	}
	m.observations[obs.ID] = obs
	return nil
}

func (m *memCacheStore) FinalizeObservation(_ context.Context, _ uint64, obsID string, final types.EmbeddingCacheObservationFinalize) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	obs := m.observations[obsID]
	if obs == nil {
		return errors.New("missing observation")
	}
	obs.LocalEmbeddingHitCount = final.LocalEmbeddingHitCount
	obs.LocalEmbeddingMissCount = final.LocalEmbeddingMissCount
	obs.LocalEmbeddingBypassCount = final.LocalEmbeddingBypassCount
	obs.LookupFailureCount = final.LookupFailureCount
	obs.CorruptionCount = final.CorruptionCount
	obs.WriteFailureCount = final.WriteFailureCount
	obs.ProviderBoundModelCallCount = final.ProviderBoundModelCallCount
	obs.RequestElapsedMS = final.RequestElapsedMS
	obs.PersistenceStatus = types.EmbeddingCachePersistencePersisted
	return nil
}

func (m *memCacheStore) RecordFailedObservation(_ context.Context, obs *types.EmbeddingCacheObservation, final types.EmbeddingCacheObservationFinalize) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recordFailedErr != nil {
		return m.recordFailedErr
	}
	obs.PersistenceStatus = types.EmbeddingCachePersistenceFailed
	obs.LocalEmbeddingHitCount = final.LocalEmbeddingHitCount
	obs.LocalEmbeddingMissCount = final.LocalEmbeddingMissCount
	obs.LocalEmbeddingBypassCount = final.LocalEmbeddingBypassCount
	obs.LookupFailureCount = final.LookupFailureCount
	obs.CorruptionCount = final.CorruptionCount
	obs.WriteFailureCount = final.WriteFailureCount
	obs.ProviderBoundModelCallCount = final.ProviderBoundModelCallCount
	obs.RequestElapsedMS = final.RequestElapsedMS
	m.observations[obs.ID] = obs
	return nil
}

func (m *memCacheStore) lastObservation() *types.EmbeddingCacheObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	var last *types.EmbeddingCacheObservation
	for _, o := range m.observations {
		if last == nil || o.CreatedAt.After(last.CreatedAt) {
			last = o
		}
	}
	return last
}

func (m *memCacheStore) entryCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, byTenant := range m.entries {
		n += len(byTenant)
	}
	return n
}

func (m *memCacheStore) observationCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.observations)
}

// deterministicEmbedder returns a fixed vector per text and counts provider
// invocations.
type deterministicEmbedder struct {
	mu            sync.Mutex
	embedCalls    int
	batchCalls    int
	poolCalls     int
	failBatch     error
	countMismatch bool
	nanVector     bool
}

func vectorFor(text string) []float32 {
	sum := sha256.Sum256([]byte(text))
	return []float32{float32(len(text)), float32(sum[0]) / 255.0, float32(sum[1]) / 255.0}
}

func (d *deterministicEmbedder) GetModelName() string { return "det" }
func (d *deterministicEmbedder) GetModelID() string   { return "model-a" }
func (d *deterministicEmbedder) GetDimensions() int   { return 3 }

func (d *deterministicEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	d.mu.Lock()
	d.embedCalls++
	d.mu.Unlock()
	return vectorFor(text), nil
}

func (d *deterministicEmbedder) batch(_ context.Context, texts []string) ([][]float32, error) {
	if d.failBatch != nil {
		return nil, d.failBatch
	}
	if d.countMismatch {
		return [][]float32{vectorFor("wrong-count")}, nil
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = vectorFor(t)
		if d.nanVector {
			out[i] = []float32{float32(len(t)), 0, float32(1e20)} // non-finite substitute handled below
		}
	}
	return out, nil
}

func (d *deterministicEmbedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	d.mu.Lock()
	d.batchCalls++
	d.mu.Unlock()
	return d.batch(ctx, texts)
}

func (d *deterministicEmbedder) BatchEmbedWithPool(ctx context.Context, _ Embedder, texts []string) ([][]float32, error) {
	d.mu.Lock()
	d.poolCalls++
	d.mu.Unlock()
	return d.batch(ctx, texts)
}

func testCacheConfig() Config {
	return Config{
		Source:                    types.ModelSourceRemote,
		BaseURL:                   "https://api.openai.com/v1",
		ModelName:                 "text-embedding-3-small",
		Dimensions:                3,
		SupportsDimensionOverride: true,
		ModelID:                   "model-a",
		Provider:                  "openai",
	}
}

func newCachedEmbedder(t *testing.T, inner Embedder, store types.EmbeddingCacheStore, observer types.EmbeddingCacheObserver, tenantID uint64, cfg Config) Embedder {
	t.Helper()
	return WrapEmbeddingCache(inner, CacheOptions{
		Enabled:  true,
		TenantID: tenantID,
		Store:    store,
		Observer: observer,
		Config:   cfg,
	})
}

func TestEmbeddingCacheSameIdentityHit(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	v1, err := emb.Embed(ctx, "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if inner.embedCalls != 1 {
		t.Fatalf("first call should miss and hit provider once, got %d", inner.embedCalls)
	}
	if store.entryCount() != 1 {
		t.Fatalf("expected 1 entry, got %d", store.entryCount())
	}

	v2, err := emb.Embed(ctx, "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if inner.embedCalls != 1 {
		t.Fatalf("second call should hit cache, provider calls = %d", inner.embedCalls)
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("hit vector mismatch at %d: %v vs %v", i, v1[i], v2[i])
		}
	}
	obs := store.lastObservation()
	if obs == nil || obs.LocalEmbeddingHitCount != 1 || obs.ProviderBoundModelCallCount != 0 {
		t.Fatalf("hit observation wrong: %+v", obs)
	}
}

func TestEmbeddingCacheIdentityChangeMiss(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	if _, err := emb.Embed(ctx, "hello world"); err != nil {
		t.Fatal(err)
	}

	// Different tenant must miss.
	emb2 := newCachedEmbedder(t, inner, store, store, 2, testCacheConfig())
	if _, err := emb2.Embed(ctx, "hello world"); err != nil {
		t.Fatal(err)
	}
	if inner.embedCalls != 2 {
		t.Fatalf("cross-tenant must miss, provider calls=%d", inner.embedCalls)
	}

	// Different model config fingerprint must miss.
	cfg := testCacheConfig()
	cfg.ModelName = "text-embedding-3-large"
	emb3 := newCachedEmbedder(t, inner, store, store, 1, cfg)
	if _, err := emb3.Embed(ctx, "hello world"); err != nil {
		t.Fatal(err)
	}
	if inner.embedCalls != 3 {
		t.Fatalf("config change must miss, provider calls=%d", inner.embedCalls)
	}
}

func TestEmbeddingCacheBatchMixedOrderAndDuplicates(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	texts := []string{"a", "b", "c"}
	if _, err := emb.BatchEmbed(ctx, texts); err != nil {
		t.Fatal(err)
	}
	if inner.batchCalls != 1 {
		t.Fatalf("first batch should miss and call provider once, got %d", inner.batchCalls)
	}

	// Second batch: b is cached, a/c are missing (a and c are not cached).
	texts2 := []string{"b", "x", "b"}
	got, err := emb.BatchEmbed(ctx, texts2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}
	// Order and duplicate positions preserved: [vectorFor(b), vectorFor(x), vectorFor(b)].
	expB := vectorFor("b")
	expX := vectorFor("x")
	for i := range expB {
		if got[0][i] != expB[i] || got[2][i] != expB[i] {
			t.Fatalf("duplicate hit positions wrong at %d", i)
		}
	}
	for i := range expX {
		if got[1][i] != expX[i] {
			t.Fatalf("miss position wrong at %d", i)
		}
	}
	if inner.batchCalls != 2 {
		t.Fatalf("mixed batch should call provider once for misses, provider calls=%d", inner.batchCalls)
	}
	obs := store.lastObservation()
	if obs == nil || obs.LocalEmbeddingHitCount != 2 || obs.LocalEmbeddingMissCount != 1 {
		t.Fatalf("mixed counts wrong: %+v", obs)
	}
}

func TestEmbeddingCacheAllHitZeroProviderCalls(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	texts := []string{"a", "b", "c"}
	if _, err := emb.BatchEmbed(ctx, texts); err != nil {
		t.Fatal(err)
	}
	if _, err := emb.BatchEmbed(ctx, texts); err != nil {
		t.Fatal(err)
	}
	if inner.batchCalls != 1 {
		t.Fatalf("all-hit batch must not call provider again, provider calls=%d", inner.batchCalls)
	}
	obs := store.lastObservation()
	if obs == nil || obs.LocalEmbeddingHitCount != 3 || obs.ProviderBoundModelCallCount != 0 {
		t.Fatalf("all-hit observation wrong: %+v", obs)
	}
}

func TestEmbeddingCacheAllMissOneProviderCall(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	if _, err := emb.BatchEmbed(ctx, []string{"a", "b", "c"}); err != nil {
		t.Fatal(err)
	}
	if inner.batchCalls != 1 {
		t.Fatalf("all-miss batch should call provider once, got %d", inner.batchCalls)
	}
	obs := store.lastObservation()
	if obs == nil || obs.LocalEmbeddingMissCount != 3 || obs.ProviderBoundModelCallCount != 1 {
		t.Fatalf("all-miss observation wrong: %+v", obs)
	}
}

func TestEmbeddingCachePoolPreservesSingleProviderCall(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	// First pool call populates.
	if _, err := emb.BatchEmbedWithPool(ctx, emb, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if inner.poolCalls != 1 {
		t.Fatalf("cold pool should call provider once, got %d", inner.poolCalls)
	}
	// Warm pool call hits both.
	if _, err := emb.BatchEmbedWithPool(ctx, emb, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if inner.poolCalls != 1 {
		t.Fatalf("warm pool must not call provider again, got %d", inner.poolCalls)
	}
}

func TestEmbeddingCacheProviderCountMismatchFailsNoWrite(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{countMismatch: true}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	if _, err := emb.BatchEmbed(ctx, []string{"a", "b"}); err == nil {
		t.Fatal("expected count mismatch error")
	}
	if store.entryCount() != 0 {
		t.Fatalf("count mismatch must not persist partial entries, got %d", store.entryCount())
	}
}

func TestEmbeddingCacheProviderErrorNoEntry(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{failBatch: errors.New("provider down")}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	if _, err := emb.BatchEmbed(ctx, []string{"a"}); err == nil {
		t.Fatal("expected provider error")
	}
	if store.entryCount() != 0 {
		t.Fatalf("provider error must not persist entries, got %d", store.entryCount())
	}
}

func TestEmbeddingCacheCorruptEntryRejectedAndRecomputed(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	// Populate a valid entry.
	if _, err := emb.Embed(ctx, "corrupt me"); err != nil {
		t.Fatal(err)
	}
	// Tamper the stored payload directly under the SAME cache key so its
	// dimensions disagree with its stored Dimensions (a corrupt row).
	ce := emb.(*cacheEmbedder)
	key := ce.computeCacheKey(ctx, "corrupt me")
	store.mu.Lock()
	store.entries[1][key].VectorPayload = "[1,2]"
	store.mu.Unlock()

	if _, err := emb.Embed(ctx, "corrupt me"); err != nil {
		t.Fatal(err)
	}
	obs := store.lastObservation()
	if obs == nil || obs.CorruptionCount != 1 {
		t.Fatalf("corrupt entry should be counted as corruption, got %+v", obs)
	}
	// It still recomputed via provider.
	if inner.embedCalls != 2 {
		t.Fatalf("corrupt entry should recompute, provider calls=%d", inner.embedCalls)
	}
}

func TestEmbeddingCacheLookupFailureFallback(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	store.lookupErr = errors.New("db down")
	got, err := emb.Embed(ctx, "still works")
	if err != nil {
		t.Fatalf("lookup failure must fall back to provider, got err=%v", err)
	}
	if !equalVector(got, vectorFor("still works")) {
		t.Fatalf("fallback returned wrong vector")
	}
	obs := store.lastObservation()
	if obs == nil || obs.LookupFailureCount != 1 {
		t.Fatalf("lookup failure must be counted, got %+v", obs)
	}
}

func TestEmbeddingCacheWriteFailureReturnsProviderResult(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	store.putErr = errors.New("write down")
	got, err := emb.Embed(ctx, "write fail")
	if err != nil {
		t.Fatalf("write failure must not fail business result, got err=%v", err)
	}
	if !equalVector(got, vectorFor("write fail")) {
		t.Fatalf("write failure returned wrong vector")
	}
	obs := store.lastObservation()
	if obs == nil || obs.WriteFailureCount != 1 {
		t.Fatalf("write failure must be counted, got %+v", obs)
	}
}

func TestEmbeddingCacheConcurrentSameKeySingleEntry(t *testing.T) {
	store := newMemCacheStore()
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := emb.Embed(ctx, "same-key"); err != nil {
				t.Errorf("concurrent embed failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if store.entryCount() != 1 {
		t.Fatalf("concurrent same key must persist a single entry, got %d", store.entryCount())
	}
}

func TestComputeModelConfigFingerprintExcludesCredentials(t *testing.T) {
	base := testCacheConfig()
	base.APIKey = "secret-1"
	base.AppSecret = "app-secret-1"
	base.CustomHeaders = map[string]string{"Authorization": "Bearer x"}
	fp1 := ComputeModelConfigFingerprint(base)

	changed := base
	changed.APIKey = "secret-2"
	changed.AppSecret = "app-secret-2"
	changed.CustomHeaders = map[string]string{"Authorization": "Bearer y"}
	if ComputeModelConfigFingerprint(changed) != fp1 {
		t.Fatal("credential change must not change fingerprint")
	}

	changedModel := base
	changedModel.ModelName = "other-model"
	if ComputeModelConfigFingerprint(changedModel) == fp1 {
		t.Fatal("model name change must change fingerprint")
	}

	changedExtra := base
	changedExtra.ExtraConfig = map[string]string{"api_version": "2024-06-01"}
	if ComputeModelConfigFingerprint(changedExtra) == fp1 {
		t.Fatal("allowlisted extra_config change must change fingerprint")
	}

	changedIgnored := base
	changedIgnored.ExtraConfig = map[string]string{"region": "us-east-1"}
	if ComputeModelConfigFingerprint(changedIgnored) != fp1 {
		t.Fatal("non-allowlisted extra_config change must not change fingerprint")
	}
}

func TestComputeProviderIdentityNormalizesTrailingSlash(t *testing.T) {
	cfg := testCacheConfig()
	cfg.BaseURL = "https://api.openai.com/v1/"
	id1 := ComputeProviderIdentity(cfg)
	cfg.BaseURL = "https://api.openai.com/v1"
	id2 := ComputeProviderIdentity(cfg)
	if id1 != id2 {
		t.Fatalf("trailing slash must not change identity: %q vs %q", id1, id2)
	}
}

func TestWrapEmbeddingCacheRejectsZeroTenant(t *testing.T) {
	inner := &deterministicEmbedder{}
	store := newMemCacheStore()
	emb := WrapEmbeddingCache(inner, CacheOptions{
		Enabled:  true,
		TenantID: 0,
		Store:    store,
		Observer: store,
		Config:   testCacheConfig(),
	})
	if emb != inner {
		t.Fatal("zero tenant must disable the cache and return the inner embedder unchanged")
	}
}

func TestEmbeddingCacheBeginFailureRecordsFailedObservation(t *testing.T) {
	store := newMemCacheStore()
	store.beginErr = errors.New("db down")
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	got, err := emb.Embed(ctx, "still runs")
	if err != nil {
		t.Fatalf("begin failure must not fail the business result, got err=%v", err)
	}
	if !equalVector(got, vectorFor("still runs")) {
		t.Fatal("begin failure returned wrong vector")
	}
	obs := store.lastObservation()
	if obs == nil || obs.PersistenceStatus != types.EmbeddingCachePersistenceFailed {
		t.Fatalf("begin failure must record a FAILED observation, got %+v", obs)
	}
}

// TestEmbeddingCachePersistentObservationOutageIsBestEffort documents the frozen
// best-effort boundary: when BOTH the STARTED and the FAILED writes fail (a
// persistent store outage), the invocation cannot be recorded at all. The
// business result is unaffected, but that window's measurement is not
// guaranteed to downgrade from COMPLETE — this is an acknowledged limitation,
// not a silently fabricated COMPLETE.
func TestEmbeddingCachePersistentObservationOutageIsBestEffort(t *testing.T) {
	store := newMemCacheStore()
	store.beginErr = errors.New("db down")
	store.recordFailedErr = errors.New("db still down")
	inner := &deterministicEmbedder{}
	emb := newCachedEmbedder(t, inner, store, store, 1, testCacheConfig())
	ctx := context.Background()

	got, err := emb.Embed(ctx, "still runs")
	if err != nil {
		t.Fatalf("persistent observation outage must not fail the business result, got err=%v", err)
	}
	if !equalVector(got, vectorFor("still runs")) {
		t.Fatal("business result wrong under persistent outage")
	}
	if n := store.observationCount(); n != 0 {
		t.Fatalf("persistent outage should leave no observation, got %d", n)
	}
}

func TestComputeProviderIdentityStripsUserinfoAndHashesQuery(t *testing.T) {
	cfg := testCacheConfig()
	cfg.BaseURL = "https://user:secret@api.openai.com/v1?api-version=2024&key=leak"
	id1 := ComputeProviderIdentity(cfg)
	cfg.BaseURL = "https://api.openai.com/v1"
	id2 := ComputeProviderIdentity(cfg)
	if id1 == id2 {
		t.Fatalf("a routing query must change identity: %q", id1)
	}
	if strings.Contains(id1, "secret") || strings.Contains(id1, "leak") || strings.Contains(id1, "api-version") {
		t.Fatalf("identity must not persist credentials: %q", id1)
	}
}

func TestComputeProviderIdentityCanonicalizesQueryBeforeHashing(t *testing.T) {
	cfg := testCacheConfig()
	cfg.BaseURL = "https://api.example.com/v1?b=2&a=1"
	id1 := ComputeProviderIdentity(cfg)
	cfg.BaseURL = "https://api.example.com/v1?a=1&b=2"
	id2 := ComputeProviderIdentity(cfg)
	if id1 != id2 {
		t.Fatalf("equivalent query ordering must have one identity: %q vs %q", id1, id2)
	}
}

func TestComputeProviderIdentityBareHostQueryHasSafeDigest(t *testing.T) {
	cfg := testCacheConfig()
	cfg.BaseURL = "api.example.com/v1?token=secret"
	id1 := ComputeProviderIdentity(cfg)
	cfg.BaseURL = "api.example.com/v1"
	id2 := ComputeProviderIdentity(cfg)
	if id1 == id2 {
		t.Fatalf("bare-host routing query must change identity: %q", id1)
	}
	if strings.Contains(id1, "token") || strings.Contains(id1, "secret") {
		t.Fatalf("identity must not persist a query token: %q", id1)
	}
}

func TestComputeProviderIdentityInvalidURLPersistsDigestOnly(t *testing.T) {
	cfg := testCacheConfig()
	cfg.BaseURL = "https://user:secret@api.example.com/%zz?token=leak"
	id := ComputeProviderIdentity(cfg)
	if !strings.Contains(id, "invalid_url_sha256=") {
		t.Fatalf("invalid URL must use a safe digest identity: %q", id)
	}
	for _, forbidden := range []string{"user", "secret", "token", "leak", "%zz"} {
		if strings.Contains(id, forbidden) {
			t.Fatalf("invalid URL identity leaked %q: %q", forbidden, id)
		}
	}
}

func TestComputeProviderIdentityBareHostUserinfoStripped(t *testing.T) {
	cfg := testCacheConfig()
	cfg.BaseURL = "user:secret@api.example.com/v1?token=secret"
	id1 := ComputeProviderIdentity(cfg)
	cfg.BaseURL = "api.example.com/v1"
	id2 := ComputeProviderIdentity(cfg)
	if id1 == id2 {
		t.Fatalf("bare-host query must change identity while userinfo stays excluded: %q", id1)
	}
	if strings.Contains(id1, "secret") || strings.Contains(id1, "token") {
		t.Fatalf("identity must not persist credentials/query: %q", id1)
	}
}

func equalVector(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
