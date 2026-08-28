package repository

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrEmbeddingCacheCorrupt = errors.New("embedding cache entry corrupt")
var ErrEmbeddingCacheObservationNotFound = errors.New("embedding cache observation not found")

type embeddingCacheRepository struct{ db *gorm.DB }

func NewEmbeddingCacheRepository(db *gorm.DB) interfaces.EmbeddingCacheRepository {
	return &embeddingCacheRepository{db: db}
}

// GetValidEntry returns the entry for (tenant, cache_key) only when it decodes
// and validates against the expected computation identity. A row that fails
// validation is best-effort deleted and reported as corrupt; absence is not an
// error.
func (r *embeddingCacheRepository) GetValidEntry(
	ctx context.Context, tenantID uint64, cacheKey string, expected types.EmbeddingCacheIdentity,
) (types.EmbeddingCacheLookup, error) {
	var entry types.EmbeddingCacheEntry
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND cache_key = ?", tenantID, cacheKey).
		First(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return types.EmbeddingCacheLookup{}, nil
	}
	if err != nil {
		return types.EmbeddingCacheLookup{}, err
	}
	vector, err := decodeVector(entry.VectorPayload)
	if err != nil || !vectorValid(vector, entry.Dimensions) ||
		entry.CacheSchemaVersion != expected.SchemaVersion ||
		entry.ModelConfigFingerprint != expected.ModelConfigFingerprint ||
		entry.ProviderIdentity != expected.ProviderIdentity ||
		entry.ModelID != expected.ModelID {
		// Reject the row and best-effort remove it so it cannot poison future hits.
		_ = r.db.WithContext(ctx).
			Where("tenant_id = ? AND cache_key = ?", tenantID, cacheKey).
			Delete(&types.EmbeddingCacheEntry{}).Error
		return types.EmbeddingCacheLookup{Corrupt: true}, nil
	}
	return types.EmbeddingCacheLookup{Entry: &entry, Vector: vector}, nil
}

// PutValidatedEntry writes an entry idempotently. A unique conflict keeps the
// existing winner; callers always return their own provider-computed vector, so
// the loser's vector correctness is unaffected.
func (r *embeddingCacheRepository) PutValidatedEntry(ctx context.Context, entry *types.EmbeddingCacheEntry) error {
	if entry == nil || entry.TenantID == 0 || entry.CacheKey == "" {
		return errors.New("embedding cache entry requires tenant and cache key")
	}
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "cache_key"}},
		DoNothing: true,
	}).Create(entry).Error
}

// BeginObservation writes a STARTED row before the lookup/provider round-trip so
// a crash or finalize failure leaves a not-persisted fact the window can count.
func (r *embeddingCacheRepository) BeginObservation(ctx context.Context, obs *types.EmbeddingCacheObservation) error {
	if obs == nil || obs.TenantID == 0 {
		return errors.New("embedding cache observation requires tenant")
	}
	if obs.ID == "" {
		obs.ID = uuid.NewString()
	}
	if obs.CreatedAt.IsZero() {
		obs.CreatedAt = time.Now().UTC()
	}
	obs.PersistenceStatus = types.EmbeddingCachePersistenceStarted
	return r.db.WithContext(ctx).Create(obs).Error
}

// FinalizeObservation transitions a STARTED row to PERSISTED with the resolved
// counts. It is idempotent and fail-closed: a missing/already-finalized row does
// not create a fake fact, and a zero-row update is reported as an error so the
// caller cannot silently claim a finalize that did not happen.
func (r *embeddingCacheRepository) FinalizeObservation(
	ctx context.Context, tenantID uint64, obsID string, final types.EmbeddingCacheObservationFinalize,
) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).
		Model(&types.EmbeddingCacheObservation{}).
		Where("id = ? AND tenant_id = ? AND persistence_status = ?", obsID, tenantID, types.EmbeddingCachePersistenceStarted).
		Updates(map[string]interface{}{
			"local_embedding_hit_count":       final.LocalEmbeddingHitCount,
			"local_embedding_miss_count":      final.LocalEmbeddingMissCount,
			"local_embedding_bypass_count":    final.LocalEmbeddingBypassCount,
			"lookup_failure_count":            final.LookupFailureCount,
			"corruption_count":                final.CorruptionCount,
			"write_failure_count":             final.WriteFailureCount,
			"provider_bound_model_call_count": final.ProviderBoundModelCallCount,
			"request_elapsed_ms":              final.RequestElapsedMS,
			"persistence_status":              types.EmbeddingCachePersistencePersisted,
			"finalized_at":                    now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrEmbeddingCacheObservationNotFound
	}
	return nil
}

// RecordFailedObservation writes a FAILED observation row directly. It is used
// when BeginObservation failed (so there is no STARTED row to finalize) but the
// business invocation still ran: recording a FAILED fact keeps the measurement
// window PARTIAL instead of silently COMPLETE.
//
// This fallback is best-effort: it writes to the same store that just rejected
// BeginObservation. If the store is persistently unavailable, this write also
// fails and the invocation cannot be recorded at all. That residual window is a
// frozen best-effort boundary (see cacheEmbedder.begin) — it is not a claim
// that a total outage is detectable.
func (r *embeddingCacheRepository) RecordFailedObservation(
	ctx context.Context, obs *types.EmbeddingCacheObservation, final types.EmbeddingCacheObservationFinalize,
) error {
	if obs == nil || obs.TenantID == 0 {
		return errors.New("embedding cache observation requires tenant")
	}
	if obs.ID == "" {
		obs.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if obs.CreatedAt.IsZero() {
		obs.CreatedAt = now
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
	obs.FinalizedAt = &now
	return r.db.WithContext(ctx).Create(obs).Error
}

// AggregateObservations sums tenant/model/run/time-scoped observation facts.
// attempted/persisted/failed and COMPLETE/PARTIAL/UNKNOWN mirror the ModelCall
// measurement-health contract.
//
// Known limitation (frozen best-effort boundary): an invocation whose
// BeginObservation AND RecordFailedObservation both fail (persistent store
// outage) leaves no row, so it is absent from attempted and cannot downgrade a
// mixed window from COMPLETE. Consumers must not read COMPLETE as an absolute
// guarantee under total storage outage.
func (r *embeddingCacheRepository) AggregateObservations(
	ctx context.Context, filter types.EmbeddingCacheObservationFilter,
) (*types.EmbeddingCacheAggregate, error) {
	q := r.db.WithContext(ctx).Model(&types.EmbeddingCacheObservation{}).Where("tenant_id = ?", filter.TenantID)
	if filter.RunID != nil {
		q = q.Where("run_id = ?", *filter.RunID)
	}
	if filter.ModelID != "" {
		q = q.Where("model_id = ?", filter.ModelID)
	}
	if filter.From != nil {
		q = q.Where("created_at >= ?", *filter.From)
	}
	if filter.To != nil {
		q = q.Where("created_at < ?", *filter.To)
	}
	var row struct {
		Attempted, Persisted, Failed                                                     int64
		LogicalItems, Hits, Misses, Bypass, LookupFailed, Corruption, WriteFailed, Calls int64
	}
	err := q.Select(`COUNT(*) AS attempted,
		COALESCE(SUM(CASE WHEN persistence_status = 'PERSISTED' THEN 1 ELSE 0 END),0) AS persisted,
		COALESCE(SUM(CASE WHEN persistence_status IN ('STARTED','FAILED') THEN 1 ELSE 0 END),0) AS failed,
		COALESCE(SUM(logical_embedding_item_count),0) AS logical_items,
		COALESCE(SUM(local_embedding_hit_count),0) AS hits,
		COALESCE(SUM(local_embedding_miss_count),0) AS misses,
		COALESCE(SUM(local_embedding_bypass_count),0) AS bypass,
		COALESCE(SUM(lookup_failure_count),0) AS lookup_failed,
		COALESCE(SUM(corruption_count),0) AS corruption,
		COALESCE(SUM(write_failure_count),0) AS write_failed,
		COALESCE(SUM(provider_bound_model_call_count),0) AS calls`).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	out := &types.EmbeddingCacheAggregate{
		BatchInvocationCount:        row.Attempted,
		LogicalItemCount:            row.LogicalItems,
		HitCount:                    row.Hits,
		MissCount:                   row.Misses,
		BypassCount:                 row.Bypass,
		LookupFailedCount:           row.LookupFailed,
		CorruptionCount:             row.Corruption,
		WriteFailedCount:            row.WriteFailed,
		ProviderBoundModelCallCount: row.Calls,
		AttemptedCount:              row.Attempted,
		PersistedCount:              row.Persisted,
		FailedCount:                 row.Failed,
	}
	out.MeasurementStatus = types.MeasurementHealthComplete
	if row.Failed > 0 {
		out.MeasurementStatus = types.MeasurementHealthPartial
	}
	if row.Attempted == 0 {
		out.MeasurementStatus = types.MeasurementHealthUnknown
	}
	return out, nil
}

func (r *embeddingCacheRepository) DeleteByTenant(ctx context.Context, tenantID uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ?", tenantID).Delete(&types.EmbeddingCacheObservation{}).Error; err != nil {
			return err
		}
		return tx.Where("tenant_id = ?", tenantID).Delete(&types.EmbeddingCacheEntry{}).Error
	})
}

func (r *embeddingCacheRepository) DeleteByModel(ctx context.Context, tenantID uint64, modelID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND model_id = ?", tenantID, modelID).
			Delete(&types.EmbeddingCacheObservation{}).Error; err != nil {
			return err
		}
		return tx.Where("tenant_id = ? AND model_id = ?", tenantID, modelID).
			Delete(&types.EmbeddingCacheEntry{}).Error
	})
}

func encodeVector(v []float32) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeVector(s string) ([]float32, error) {
	var v []float32
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return v, nil
}

func vectorValid(v []float32, expectedDim int) bool {
	if expectedDim <= 0 || len(v) != expectedDim {
		return false
	}
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return false
		}
	}
	return true
}
