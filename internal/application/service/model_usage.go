package service

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type modelUsageService struct {
	repo                  interfaces.ModelCallRepository
	embeddingCache        interfaces.EmbeddingCacheRepository
	embeddingCacheEnabled bool
}

func NewModelUsageService(repo interfaces.ModelCallRepository,
	embeddingCache interfaces.EmbeddingCacheRepository,
	cfg *config.Config,
) interfaces.ModelUsageService {
	return &modelUsageService{
		repo:                  repo,
		embeddingCache:        embeddingCache,
		embeddingCacheEnabled: cfg != nil && cfg.EmbeddingCache != nil && cfg.EmbeddingCache.Enabled,
	}
}

func (s *modelUsageService) Aggregate(ctx context.Context, filter types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
	agg, err := s.repo.AggregateModelCalls(ctx, filter)
	if err != nil {
		return nil, err
	}
	agg.LocalEmbeddingCache = s.localEmbeddingCache(ctx, filter)
	return agg, nil
}

func (s *modelUsageService) Health(ctx context.Context, tenantID uint64, from, to time.Time) (*types.MeasurementHealth, error) {
	return s.repo.GetMeasurementHealth(ctx, tenantID, from, to)
}

// localEmbeddingCache reports the additive local-cache fact alongside (never
// merged into) the Prompt Cache fields. When the rollout switch is off it is
// DISABLED — a real "implemented but off" state — never NOT_IMPLEMENTED, and
// never a fabricated zero hit rate.
func (s *modelUsageService) localEmbeddingCache(ctx context.Context, filter types.ModelCallFilter) *types.EmbeddingCacheAggregate {
	disabled := &types.EmbeddingCacheAggregate{
		ImplementationStatus: types.EmbeddingCacheImplementationDisabled,
		MeasurementStatus:    types.MeasurementHealthUnknown,
	}
	if !s.embeddingCacheEnabled || s.embeddingCache == nil {
		return disabled
	}
	local, err := s.embeddingCache.AggregateObservations(ctx, types.EmbeddingCacheObservationFilter{
		TenantID: filter.TenantID,
		RunID:    filter.RunID,
		ModelID:  filter.ModelID,
		From:     filter.From,
		To:       filter.To,
	})
	if err != nil {
		return &types.EmbeddingCacheAggregate{
			ImplementationStatus: types.EmbeddingCacheImplementationEnabled,
			MeasurementStatus:    types.MeasurementHealthUnknown,
		}
	}
	local.ImplementationStatus = types.EmbeddingCacheImplementationEnabled
	return local
}
