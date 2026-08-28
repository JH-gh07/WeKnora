package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/provider"
	"github.com/Tencent/WeKnora/internal/types"
)

// embeddingCacheSchemaVersion bumps invalidate every previously persisted
// entry: an old-version row can never be re-interpreted as a current hit.
const embeddingCacheSchemaVersion = 1

// CacheOptions carries the frozen computation-identity inputs and the narrow
// store/observer dependencies needed to wrap an embedder with the local
// embedding cache. It is deliberately separate from Config so the decorator
// depends only on types.EmbeddingCacheStore / types.EmbeddingCacheObserver
// (never on a concrete repository, which would create an import cycle).
type CacheOptions struct {
	Enabled  bool
	TenantID uint64 // owner tenant (source tenant for cross-tenant sharing)
	Store    types.EmbeddingCacheStore
	Observer types.EmbeddingCacheObserver
	Config   Config // frozen identity source (allowlisted fields only)
}

// cacheEmbedder sits OUTSIDE the metered embedder (Decision 005-1-B): a HIT
// never reaches the inner provider-bound boundary, so it produces zero
// provider-bound ModelCall; a MISS delegates exactly once, so it produces
// exactly one. It is the outermost decorator installed by the model service.
type cacheEmbedder struct {
	inner            Embedder
	store            types.EmbeddingCacheStore
	observer         types.EmbeddingCacheObserver
	tenantID         uint64
	modelID          string
	providerIdentity string
	fingerprint      string
	dimension        int
}

// WrapEmbeddingCache returns e unchanged when the cache is disabled or any
// dependency is absent, preserving the original metered provider path. A zero
// tenant id (no tenant context) is also rejected: caching without an owning
// tenant would collapse every tenant's vectors into one shared identity.
func WrapEmbeddingCache(e Embedder, opts CacheOptions) Embedder {
	if e == nil || !opts.Enabled || opts.Store == nil || opts.Observer == nil || opts.TenantID == 0 {
		return e
	}
	return &cacheEmbedder{
		inner:            e,
		store:            opts.Store,
		observer:         opts.Observer,
		tenantID:         opts.TenantID,
		modelID:          opts.Config.ModelID,
		providerIdentity: ComputeProviderIdentity(opts.Config),
		fingerprint:      ComputeModelConfigFingerprint(opts.Config),
		dimension:        opts.Config.Dimensions,
	}
}

func (c *cacheEmbedder) GetModelName() string { return c.inner.GetModelName() }
func (c *cacheEmbedder) GetModelID() string   { return c.inner.GetModelID() }
func (c *cacheEmbedder) GetDimensions() int   { return c.inner.GetDimensions() }

// ComputeProviderIdentity returns the source + provider + normalized base URL
// that identify which remote endpoint produced a vector. It mirrors the
// provider routing in NewEmbedder (empty provider => detect from base URL) and
// never includes credentials.
func ComputeProviderIdentity(config Config) string {
	providerName := string(provider.ProviderName(config.Provider))
	if providerName == "" {
		providerName = string(provider.DetectProvider(config.BaseURL))
	}
	return string(config.Source) + "|" + providerName + "|" + normalizeBaseURL(config.BaseURL)
}

// normalizeBaseURL strips userinfo and fragment from a base URL before it
// becomes part of a persisted identity. Query values are never persisted, but
// a digest of their canonical form remains part of the identity: gateways may
// use a query parameter to select a deployment or API version, so dropping the
// query altogether could incorrectly share vectors across computation routes.
//
// Bare hosts (no scheme) such as "api.example.com/v1?token=secret" are given a
// default https scheme first, so url.Parse can identify the host and strip the
// query/userinfo instead of falling back to a verbatim value that would leak
// the token.
func normalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// Never persist any portion of an invalid endpoint: url.Parse could not
		// reliably identify userinfo/query boundaries. A digest keeps malformed
		// configurations distinct without leaking credentials or route values.
		sum := sha256.Sum256([]byte(raw))
		return "invalid_url_sha256=" + hex.EncodeToString(sum[:])
	}
	u.User = nil
	query := u.Query().Encode()
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return appendQueryDigest(strings.TrimRight(u.String(), "/"), query)
}

func appendQueryDigest(base, query string) string {
	if query == "" {
		return base
	}
	sum := sha256.Sum256([]byte(query))
	return base + "|query_sha256=" + hex.EncodeToString(sum[:])
}

// ComputeModelConfigFingerprint returns a SHA-256 over the allowlisted
// embedding.Config fields that actually change vector computation. Credentials
// (APIKey/AppSecret/AppID/CustomHeaders), MaxConcurrency and Recorder are
// excluded. ExtraConfig keys are limited to {api_version, remote_model_name}.
func ComputeModelConfigFingerprint(config Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "model_name=%s\n", config.ModelName)
	fmt.Fprintf(&b, "truncate_prompt_tokens=%d\n", config.TruncatePromptTokens)
	fmt.Fprintf(&b, "dimensions=%d\n", config.Dimensions)
	fmt.Fprintf(&b, "supports_dimension_override=%t\n", config.SupportsDimensionOverride)
	keys := make([]string, 0, len(config.ExtraConfig))
	for k := range config.ExtraConfig {
		if k == "api_version" || k == "remote_model_name" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "extra.%s=%s\n", k, config.ExtraConfig[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// computeCacheKey builds the SHA-256 computation identity for one exact text
// input. It never persists the preimage nor the raw text.
func (c *cacheEmbedder) computeCacheKey(ctx context.Context, text string) string {
	isQuery, _ := ctx.Value(types.EmbedQueryContextKey).(bool)
	queryMode := "document"
	if isQuery {
		queryMode = "query"
	}
	textDigest := sha256.Sum256([]byte(text))
	preimage := strings.Join([]string{
		strconv.Itoa(embeddingCacheSchemaVersion),
		strconv.FormatUint(c.tenantID, 10),
		hex.EncodeToString(textDigest[:]),
		c.providerIdentity,
		c.modelID,
		c.fingerprint,
		strconv.Itoa(c.dimension),
		queryMode,
	}, "|")
	sum := sha256.Sum256([]byte(preimage))
	return hex.EncodeToString(sum[:])
}

func (c *cacheEmbedder) identity() types.EmbeddingCacheIdentity {
	return types.EmbeddingCacheIdentity{
		ModelID:                c.modelID,
		ProviderIdentity:       c.providerIdentity,
		ModelConfigFingerprint: c.fingerprint,
		SchemaVersion:          embeddingCacheSchemaVersion,
	}
}

// Embed decorates a single-text embedding call.
func (c *cacheEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	obs := c.begin(ctx, 1)
	started := time.Now()
	counts := observationCounts{}

	if text == "" {
		// Empty input follows the provider path and is never cached.
		counts.bypass = 1
		vec, err := c.inner.Embed(ctx, text)
		if err != nil {
			c.finalize(ctx, obs, counts, 1, started)
			return nil, err
		}
		c.finalize(ctx, obs, counts, 1, started)
		return vec, nil
	}

	key := c.computeCacheKey(ctx, text)
	lookup, err := c.store.GetValidEntry(ctx, c.tenantID, key, c.identity())
	if err != nil {
		counts.lookupFailed = 1
		vec, perr := c.inner.Embed(ctx, text)
		if perr != nil {
			c.finalize(ctx, obs, counts, 1, started)
			return nil, perr
		}
		if !finiteVector(vec) {
			c.finalize(ctx, obs, counts, 1, started)
			return nil, fmt.Errorf("provider returned invalid embedding vector")
		}
		c.finalize(ctx, obs, counts, 1, started)
		return vec, nil
	}
	if lookup.Corrupt {
		counts.corruption = 1
		vec, perr := c.inner.Embed(ctx, text)
		if perr != nil {
			c.finalize(ctx, obs, counts, 1, started)
			return nil, perr
		}
		if !finiteVector(vec) {
			c.finalize(ctx, obs, counts, 1, started)
			return nil, fmt.Errorf("provider returned invalid embedding vector")
		}
		c.finalize(ctx, obs, counts, 1, started)
		return vec, nil
	}
	if lookup.Entry != nil {
		counts.hit = 1
		c.finalize(ctx, obs, counts, 0, started)
		return lookup.Vector, nil
	}

	counts.miss = 1
	vec, perr := c.inner.Embed(ctx, text)
	if perr != nil {
		c.finalize(ctx, obs, counts, 1, started)
		return nil, perr
	}
	if !finiteVector(vec) {
		c.finalize(ctx, obs, counts, 1, started)
		return nil, fmt.Errorf("provider returned invalid embedding vector")
	}
	if c.write(ctx, key, vec) {
		counts.writeFailed = 1
	}
	c.finalize(ctx, obs, counts, 1, started)
	return vec, nil
}

// BatchEmbed decorates a batch embedding call with per-item HIT/MISS recovery
// that preserves original order and duplicate positions.
func (c *cacheEmbedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embedBatch(ctx, texts, false)
}

// BatchEmbedWithPool decorates a pooled batch embedding call. On a partial
// HIT it delegates only the miss subset to the inner pooler so the single
// logical invocation still produces exactly one provider-bound ModelCall.
func (c *cacheEmbedder) BatchEmbedWithPool(ctx context.Context, _ Embedder, texts []string) ([][]float32, error) {
	return c.embedBatch(ctx, texts, true)
}

// embedBatch is the shared batch path. usePool selects whether the miss subset
// is embedded via the inner pooler (preserving pool concurrency) or the plain
// BatchEmbed entry.
func (c *cacheEmbedder) embedBatch(ctx context.Context, texts []string, usePool bool) ([][]float32, error) {
	obs := c.begin(ctx, len(texts))
	started := time.Now()
	counts := observationCounts{}

	results := make([][]float32, len(texts))
	missIdx := make([]int, 0, len(texts))

	// Phase 1: per-item lookup, classify HIT / MISS / CORRUPT / LOOKUP_FAILED
	// / BYPASS. Only a clean MISS increments the miss count.
	for i, text := range texts {
		if text == "" {
			counts.bypass++
			missIdx = append(missIdx, i)
			continue
		}
		key := c.computeCacheKey(ctx, text)
		lookup, err := c.store.GetValidEntry(ctx, c.tenantID, key, c.identity())
		if err != nil {
			counts.lookupFailed++
			missIdx = append(missIdx, i)
			continue
		}
		if lookup.Corrupt {
			counts.corruption++
			missIdx = append(missIdx, i)
			continue
		}
		if lookup.Entry != nil {
			results[i] = lookup.Vector
			counts.hit++
			continue
		}
		counts.miss++
		missIdx = append(missIdx, i)
	}

	// Phase 2: provider for the recompute subset. Any provider error or count
	// mismatch fails the whole call without persisting any partial entry.
	providerCalls := int64(0)
	if len(missIdx) > 0 {
		providerCalls = 1
		missTexts := make([]string, len(missIdx))
		for j, idx := range missIdx {
			missTexts[j] = texts[idx]
		}
		var vectors [][]float32
		var err error
		if usePool {
			vectors, err = c.inner.BatchEmbedWithPool(ctx, c.inner, missTexts)
		} else {
			vectors, err = c.inner.BatchEmbed(ctx, missTexts)
		}
		if err != nil {
			c.finalize(ctx, obs, counts, providerCalls, started)
			return nil, err
		}
		if len(vectors) != len(missTexts) {
			c.finalize(ctx, obs, counts, providerCalls, started)
			return nil, fmt.Errorf("embedding model returned %d embeddings for %d inputs", len(vectors), len(missTexts))
		}
		for j, idx := range missIdx {
			vec := vectors[j]
			if !finiteVector(vec) {
				c.finalize(ctx, obs, counts, providerCalls, started)
				return nil, fmt.Errorf("embedding model returned invalid vector at index %d", j)
			}
			results[idx] = vec
			// Only cacheable items (non-bypass) are persisted.
			if texts[idx] != "" {
				key := c.computeCacheKey(ctx, texts[idx])
				if c.write(ctx, key, vec) {
					counts.writeFailed++
				}
			}
		}
	}

	c.finalize(ctx, obs, counts, providerCalls, started)
	return results, nil
}

// write best-effort persists a validated vector. A write failure never changes
// the business result; it only increments write_failed for the observation.
func (c *cacheEmbedder) write(ctx context.Context, key string, vec []float32) bool {
	payload, err := json.Marshal(vec)
	if err != nil {
		return true
	}
	entry := &types.EmbeddingCacheEntry{
		TenantID:               c.tenantID,
		CacheKey:               key,
		ModelID:                c.modelID,
		ProviderIdentity:       c.providerIdentity,
		ModelConfigFingerprint: c.fingerprint,
		CacheSchemaVersion:     embeddingCacheSchemaVersion,
		Dimensions:             len(vec),
		VectorPayload:          string(payload),
	}
	return c.store.PutValidatedEntry(ctx, entry) != nil
}

type observationCounts struct {
	hit          int64
	miss         int64
	bypass       int64
	lookupFailed int64
	corruption   int64
	writeFailed  int64
}

// cacheObservation carries the observation row plus whether its STARTED write
// succeeded, so finalize can degrade to a FAILED record instead of silently
// dropping the invocation when begin failed.
type cacheObservation struct {
	obs     *types.EmbeddingCacheObservation
	beginOK bool
}

// begin writes a STARTED observation before the lookup/provider round-trip. On
// failure it logs and returns a handle marked beginOK=false, so the business
// path continues and finalize records a best-effort FAILED fact (PARTIAL) rather
// than fabricating a zero-hit COMPLETE window.
//
// Fail-closed guarantee is scoped honestly: when begin succeeds, any subsequent
// finalize failure or crash leaves the STARTED row, so the window is PARTIAL.
// When begin itself fails, the FAILED fallback in finalize is best-effort — if
// the store is persistently unavailable for BOTH writes, that invocation cannot
// be recorded at all and the window is not guaranteed to be downgraded from
// COMPLETE. That residual window is a frozen best-effort boundary, not a
// silently-fabricated COMPLETE claim.
func (c *cacheEmbedder) begin(ctx context.Context, logicalItems int) *cacheObservation {
	runID, taskID, traceID := types.LLMCallScopeFromContext(ctx)
	obs := &types.EmbeddingCacheObservation{
		TenantID:                  c.tenantID,
		RunID:                     embeddingOptionalString(runID),
		TaskID:                    embeddingOptionalString(taskID),
		TraceID:                   traceID,
		ModelID:                   c.modelID,
		Operation:                 "embedding",
		CacheMode:                 types.EmbeddingCacheModeOn,
		LogicalEmbeddingItemCount: int64(logicalItems),
	}
	if err := c.observer.BeginObservation(ctx, obs); err != nil {
		logger.Errorf(ctx, "embedding cache observation begin failed: %v", err)
		return &cacheObservation{obs: obs, beginOK: false}
	}
	return &cacheObservation{obs: obs, beginOK: true}
}

// finalize resolves an observation STARTED -> PERSISTED. When begin failed it
// instead records a best-effort FAILED fact; a finalize failure leaves the
// STARTED row so the window cannot be reported COMPLETE. Neither ever changes
// the business result.
func (c *cacheEmbedder) finalize(ctx context.Context, h *cacheObservation, counts observationCounts, providerCalls int64, started time.Time) {
	if h == nil {
		return
	}
	final := types.EmbeddingCacheObservationFinalize{
		LocalEmbeddingHitCount:      counts.hit,
		LocalEmbeddingMissCount:     counts.miss,
		LocalEmbeddingBypassCount:   counts.bypass,
		LookupFailureCount:          counts.lookupFailed,
		CorruptionCount:             counts.corruption,
		WriteFailureCount:           counts.writeFailed,
		ProviderBoundModelCallCount: providerCalls,
		RequestElapsedMS:            int64(time.Since(started).Milliseconds()),
	}
	if !h.beginOK {
		if err := c.observer.RecordFailedObservation(ctx, h.obs, final); err != nil {
			logger.Errorf(ctx, "embedding cache observation failed-record failed: %v", err)
		}
		return
	}
	if err := c.observer.FinalizeObservation(ctx, h.obs.TenantID, h.obs.ID, final); err != nil {
		logger.Errorf(ctx, "embedding cache observation finalize failed: %v", err)
	}
}

func finiteVector(v []float32) bool {
	if len(v) == 0 {
		return false
	}
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return false
		}
	}
	return true
}
