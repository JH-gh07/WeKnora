package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type stubModelCallRepo struct {
	interfaces.ModelCallRepository
	agg func(ctx context.Context, filter types.ModelCallFilter) (*types.ModelUsageAggregate, error)
}

func (s *stubModelCallRepo) AggregateModelCalls(ctx context.Context, filter types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
	return s.agg(ctx, filter)
}

type stubEmbeddingCacheRepo struct {
	interfaces.EmbeddingCacheRepository
	aggregate func(ctx context.Context, filter types.EmbeddingCacheObservationFilter) (*types.EmbeddingCacheAggregate, error)
}

func (s *stubEmbeddingCacheRepo) AggregateObservations(ctx context.Context, filter types.EmbeddingCacheObservationFilter) (*types.EmbeddingCacheAggregate, error) {
	return s.aggregate(ctx, filter)
}

func TestModelUsageLocalEmbeddingCacheDisabled(t *testing.T) {
	svc := NewModelUsageService(
		&stubModelCallRepo{agg: func(_ context.Context, _ types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
			return &types.ModelUsageAggregate{}, nil
		}},
		&stubEmbeddingCacheRepo{},
		&config.Config{}, // EmbeddingCache nil => disabled
	).(*modelUsageService)

	agg, err := svc.Aggregate(context.Background(), types.ModelCallFilter{TenantID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if agg.LocalEmbeddingCache == nil {
		t.Fatal("local embedding cache fact must be present")
	}
	if agg.LocalEmbeddingCache.ImplementationStatus != types.EmbeddingCacheImplementationDisabled {
		t.Fatalf("expected DISABLED, got %s", agg.LocalEmbeddingCache.ImplementationStatus)
	}
}

func TestModelUsageLocalEmbeddingCacheEnabledAggregates(t *testing.T) {
	var gotFilter types.EmbeddingCacheObservationFilter
	cacheRepo := &stubEmbeddingCacheRepo{
		aggregate: func(_ context.Context, f types.EmbeddingCacheObservationFilter) (*types.EmbeddingCacheAggregate, error) {
			gotFilter = f
			return &types.EmbeddingCacheAggregate{
				HitCount:             3,
				LogicalItemCount:     4,
				MeasurementStatus:    types.MeasurementHealthComplete,
				BatchInvocationCount: 1,
			}, nil
		},
	}
	svc := NewModelUsageService(
		&stubModelCallRepo{agg: func(_ context.Context, _ types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
			return &types.ModelUsageAggregate{}, nil
		}},
		cacheRepo,
		&config.Config{EmbeddingCache: &config.EmbeddingCacheConfig{Enabled: true}},
	).(*modelUsageService)

	from := time.Now().Add(-time.Hour)
	to := time.Now()
	agg, err := svc.Aggregate(context.Background(), types.ModelCallFilter{TenantID: 7, From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	if agg.LocalEmbeddingCache.ImplementationStatus != types.EmbeddingCacheImplementationEnabled {
		t.Fatalf("expected ENABLED, got %s", agg.LocalEmbeddingCache.ImplementationStatus)
	}
	if agg.LocalEmbeddingCache.HitCount != 3 || agg.LocalEmbeddingCache.LogicalItemCount != 4 {
		t.Fatalf("aggregate not passed through: %+v", agg.LocalEmbeddingCache)
	}
	if gotFilter.TenantID != 7 || gotFilter.From == nil || gotFilter.To == nil {
		t.Fatalf("observation filter not mapped: %+v", gotFilter)
	}
}
