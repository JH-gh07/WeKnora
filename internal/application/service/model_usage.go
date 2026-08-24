package service

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type modelUsageService struct {
	repo interfaces.ModelCallRepository
}

func NewModelUsageService(repo interfaces.ModelCallRepository) interfaces.ModelUsageService {
	return &modelUsageService{repo: repo}
}
func (s *modelUsageService) Aggregate(ctx context.Context, filter types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
	return s.repo.AggregateModelCalls(ctx, filter)
}
func (s *modelUsageService) Health(ctx context.Context, tenantID uint64, from, to time.Time) (*types.MeasurementHealth, error) {
	return s.repo.GetMeasurementHealth(ctx, tenantID, from, to)
}
