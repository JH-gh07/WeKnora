package pricing

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// PricingModelCallRecorder decorates a ModelCallRecorder so every persisted call
// passes through one pricing enrichment boundary. It delegates BeginModelCall
// and the actual persistence to the inner recorder, so the durable health-marker
// lifecycle is preserved unchanged (Task012 §4.3, Decision 012-4).
type PricingModelCallRecorder struct {
	inner     types.ModelCallRecorder
	estimator CostEstimator
}

// NewPricingRecorder wires a repository with the immutable estimator. The inner
// dependency is typed as interfaces.ModelCallRepository so the DI container can
// resolve it independently of the returned types.ModelCallRecorder.
func NewPricingRecorder(inner interfaces.ModelCallRepository, estimator *Estimator) types.ModelCallRecorder {
	return &PricingModelCallRecorder{inner: inner, estimator: estimator}
}

func (r *PricingModelCallRecorder) BeginModelCall(ctx context.Context, tenantID uint64, at time.Time) (string, error) {
	if r.inner == nil {
		return "", nil
	}
	return r.inner.BeginModelCall(ctx, tenantID, at)
}

func (r *PricingModelCallRecorder) RecordModelCall(ctx context.Context, healthID string, call *types.ModelCall) error {
	if call != nil {
		r.enrich(call)
	}
	if r.inner == nil {
		return nil
	}
	return r.inner.RecordModelCall(ctx, healthID, call)
}

// enrich fills the pricing snapshot in-place. It is a pure function of the call
// and the estimator; it never returns an error and never changes provider
// business outcome (a pricing failure only leaves the row UNKNOWN).
func (r *PricingModelCallRecorder) enrich(call *types.ModelCall) {
	if r.estimator == nil {
		call.PricingStatus = types.PricingStatusUnknown
		call.PricingReason = types.PricingReasonCatalogUnavailable
		return
	}
	res := r.estimator.Estimate(call)
	call.PricingStatus = res.Status
	call.PricingReason = res.Reason
	if res.Status != types.PricingStatusPriced {
		// UNKNOWN rows keep every amount/currency/unit-price field NULL.
		return
	}
	call.EstimatedCostNanos = res.EstimatedCostNanos
	// estimated_cost is only a compatibility decimal; the nanos field is the
	// fixed-point authority (Task012 §6.2, §8.2).
	compat := float64(*res.EstimatedCostNanos) / 1e9
	call.EstimatedCost = &compat
	call.Currency = res.Currency
	call.PricingVersion = res.PricingVersion
	call.PricingSource = res.PricingSource
	call.PricingEffectiveAt = res.PricingEffectiveAt
	call.PricingRuleID = res.PricingRuleID
	call.PricingCatalogHash = res.PricingCatalogHash
	call.PricingUnit = res.PricingUnit
	inputNanos := res.InputUnitPriceNanos
	outputNanos := res.OutputUnitPriceNanos
	call.InputUnitPriceNanos = &inputNanos
	call.OutputUnitPriceNanos = &outputNanos
	call.CacheReadUnitNanos = res.CacheReadUnitPriceNanos
	call.CacheWriteUnitNanos = res.CacheWriteUnitPriceNanos
}
