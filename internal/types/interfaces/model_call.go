package interfaces

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
	"time"
)

type ModelCallRepository interface {
	types.ModelCallRecorder
	CreateModelCall(ctx context.Context, call *types.ModelCall) error
	GetModelCall(ctx context.Context, tenantID uint64, id string) (*types.ModelCall, error)
	AggregateModelCalls(ctx context.Context, filter types.ModelCallFilter) (*types.ModelUsageAggregate, error)
	RecordMeteringAttempt(ctx context.Context, tenantID uint64, at time.Time, persisted bool) error
	GetMeasurementHealth(ctx context.Context, tenantID uint64, from, to time.Time) (*types.MeasurementHealth, error)
}

type ModelUsageService interface {
	Aggregate(ctx context.Context, filter types.ModelCallFilter) (*types.ModelUsageAggregate, error)
	Health(ctx context.Context, tenantID uint64, from, to time.Time) (*types.MeasurementHealth, error)
}
