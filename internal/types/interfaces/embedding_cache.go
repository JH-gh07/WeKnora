package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// EmbeddingCacheRepository is the full tenant-scoped persistence surface for the
// local embedding cache. It extends the narrow types.EmbeddingCacheStore and
// types.EmbeddingCacheObserver contracts used by the decorator with lifecycle
// purge and aggregation.
type EmbeddingCacheRepository interface {
	types.EmbeddingCacheStore
	types.EmbeddingCacheObserver
	AggregateObservations(ctx context.Context, filter types.EmbeddingCacheObservationFilter) (*types.EmbeddingCacheAggregate, error)
	DeleteByTenant(ctx context.Context, tenantID uint64) error
	DeleteByModel(ctx context.Context, tenantID uint64, modelID string) error
}
