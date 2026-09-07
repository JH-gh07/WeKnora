package pricing

import (
	"math/big"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// PricingResult is the deterministic output of cost estimation. Price/cost
// facts are populated only when Status == PRICED; an UNKNOWN result carries only
// a Reason and leaves every amount/currency field empty.
type PricingResult struct {
	Status types.PricingStatus
	Reason types.PricingReason

	// Populated only when Status == PRICED.
	Currency                 string
	EstimatedCostNanos       *int64
	PricingVersion           string
	PricingSource            string
	PricingEffectiveAt       *time.Time
	PricingRuleID            string
	PricingCatalogHash       string
	PricingUnit              string
	InputUnitPriceNanos      int64
	OutputUnitPriceNanos     int64
	CacheReadUnitPriceNanos  *int64
	CacheWriteUnitPriceNanos *int64
}

// CostEstimator is the narrow dependency used by the pricing recorder. It is a
// pure function over a ModelCall: no network, no database, no model config.
type CostEstimator interface {
	Estimate(call *types.ModelCall) PricingResult
}

// Estimator holds an immutable catalog and produces fixed-point estimates.
type Estimator struct {
	catalog *LoadedCatalog
	now     func() time.Time
}

// NewEstimator builds an estimator over a validated catalog. The estimator is
// safe for concurrent use: the catalog is read-only after load.
func NewEstimator(catalog *LoadedCatalog) *Estimator {
	return &Estimator{catalog: catalog, now: time.Now}
}

// Estimate resolves the exact rule and computes the fixed-point cost. It never
// returns a business error and never fabricates a zero: any non-priceable call
// yields UNKNOWN with a fail-closed reason (Task012 §7).
func (e *Estimator) Estimate(call *types.ModelCall) PricingResult {
	res := PricingResult{Status: types.PricingStatusUnknown}
	if call == nil {
		res.Reason = types.PricingReasonUsageUnavailable
		return res
	}
	if e == nil || e.catalog == nil {
		res.Reason = types.PricingReasonCatalogUnavailable
		return res
	}
	now := e.now().UTC()

	// Pricing domain: only the catalog's exact provider + chat operation.
	if call.Provider != e.catalog.Provider {
		res.Reason = types.PricingReasonNonProviderPricedModel
		return res
	}
	if call.Operation != types.ModelOperationChat {
		res.Reason = types.PricingReasonNonProviderPricedModel
		return res
	}

	rule, notYetValid, expired := e.catalog.resolveExact(call.ModelName, call.Operation, now)
	if rule.ruleID == "" {
		switch {
		case notYetValid:
			res.Reason = types.PricingReasonRuleNotYetValid
		case expired:
			res.Reason = types.PricingReasonRuleExpired
		default:
			res.Reason = types.PricingReasonNoExactRule
		}
		return res
	}
	if !now.Before(e.catalog.ReviewAfter) {
		// Catalog review window lapsed: fail closed instead of reusing a stale price.
		res.Reason = types.PricingReasonRuleExpired
		return res
	}

	// Final usage: only a fully-drained successful call carries trustworthy
	// final usage. Failed/abandoned calls (including partially-streamed ones)
	// must not be priced from an incomplete final usage snapshot.
	if !call.Success {
		res.Reason = types.PricingReasonUsageUnavailable
		return res
	}
	switch call.UsageFinality {
	case types.UsageFinalityPartial:
		res.Reason = types.PricingReasonUsagePartial
		return res
	case types.UsageFinalityUnavailable:
		res.Reason = types.PricingReasonUsageUnavailable
		return res
	case types.UsageFinalityReported:
		// continue
	default:
		res.Reason = types.PricingReasonUsageUnavailable
		return res
	}
	if call.InputTokens == nil || call.OutputTokens == nil {
		res.Reason = types.PricingReasonInvalidUsage
		return res
	}
	if *call.InputTokens < 0 || *call.OutputTokens < 0 {
		res.Reason = types.PricingReasonInvalidUsage
		return res
	}

	// Cache billing dimension: only when the rule declares a cache price.
	if rule.cacheReadNanos != nil || rule.cacheWriteNanos != nil {
		if rule.cacheWriteNanos != nil {
			// Task012 defines no cache-write billing formula; fail closed.
			res.Reason = types.PricingReasonUnobservableBillingDimension
			return res
		}
		if call.CacheReadTokens == nil || call.CacheReportedInputTokens == nil {
			res.Reason = types.PricingReasonUnobservableBillingDimension
			return res
		}
		cr := int64(*call.CacheReadTokens)
		if cr < 0 || cr > int64(*call.InputTokens) {
			res.Reason = types.PricingReasonInvalidUsage
			return res
		}
	}

	nanos, ok := computeNanos(call, rule)
	if !ok {
		res.Reason = types.PricingReasonCalculationOverflow
		return res
	}

	eff := rule.validFrom
	res.Status = types.PricingStatusPriced
	res.Currency = e.catalog.Currency
	res.EstimatedCostNanos = &nanos
	res.PricingVersion = e.catalog.CatalogVersion
	res.PricingSource = e.catalog.SourceURL
	res.PricingEffectiveAt = &eff
	res.PricingRuleID = rule.ruleID
	res.PricingCatalogHash = e.catalog.Hash
	res.PricingUnit = rule.billingUnit
	res.InputUnitPriceNanos = rule.inputNanos
	res.OutputUnitPriceNanos = rule.outputNanos
	res.CacheReadUnitPriceNanos = rule.cacheReadNanos
	res.CacheWriteUnitPriceNanos = rule.cacheWriteNanos
	return res
}

// computeNanos computes the fixed-point nanos with integer math. It uses
// math/big for the multiplication so overflow is detected (via IsInt64) rather
// than silently wrapping. Rounding happens exactly once, half-up, on the final
// division by 1_000_000.
func computeNanos(call *types.ModelCall, r loadedRule) (int64, bool) {
	input := int64(*call.InputTokens)
	output := int64(*call.OutputTokens)
	raw := new(big.Int).Mul(big.NewInt(input), big.NewInt(r.inputNanos))
	raw.Add(raw, new(big.Int).Mul(big.NewInt(output), big.NewInt(r.outputNanos)))
	if r.cacheReadNanos != nil {
		// cache-read discount: uncached input at full price, cached read at discount.
		cacheRead := int64(*call.CacheReadTokens)
		uncached := input - cacheRead
		raw.Set(new(big.Int).Mul(big.NewInt(uncached), big.NewInt(r.inputNanos)))
		raw.Add(raw, new(big.Int).Mul(big.NewInt(cacheRead), big.NewInt(*r.cacheReadNanos)))
		raw.Add(raw, new(big.Int).Mul(big.NewInt(output), big.NewInt(r.outputNanos)))
	}
	return roundHalfUpDiv(raw, 1_000_000)
}

// roundHalfUpDiv returns round_half_up(raw / divisor) and reports whether the
// result fits in int64. It assumes raw is non-negative.
func roundHalfUpDiv(raw *big.Int, divisor int64) (int64, bool) {
	den := big.NewInt(divisor)
	q, rem := new(big.Int).QuoRem(raw, den, new(big.Int))
	twice := new(big.Int).Mul(rem, big.NewInt(2))
	if twice.Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, false
	}
	return q.Int64(), true
}
