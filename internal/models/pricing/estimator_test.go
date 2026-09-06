package pricing

import (
	"math"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

func intp(v int) *int { return &v }

func defaultEstimator(t *testing.T) *Estimator {
	t.Helper()
	c, err := LoadCatalog(mustJSON(t, validWire()))
	if err != nil {
		t.Fatal(err)
	}
	e := NewEstimator(c)
	e.now = func() time.Time { return time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC) }
	return e
}

func customEstimator(t *testing.T, validFrom, validTo, reviewAfter, input, output string, cacheRead, cacheWrite *string) *Estimator {
	t.Helper()
	w := validWire()
	w.Rules[0].ValidFrom = validFrom
	w.Rules[0].ValidTo = validTo
	w.ReviewAfter = reviewAfter
	w.SourceCapturedAt = validFrom
	w.Rules[0].InputPrice = input
	w.Rules[0].OutputPrice = output
	w.Rules[0].CacheReadPrice = cacheRead
	w.Rules[0].CacheWritePrice = cacheWrite
	c, err := LoadCatalog(mustJSON(t, w))
	if err != nil {
		t.Fatal(err)
	}
	e := NewEstimator(c)
	e.now = func() time.Time { return time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC) }
	return e
}

func chatCall(model, provider string, success bool, finality types.UsageFinality, in, out *int) *types.ModelCall {
	return &types.ModelCall{
		ModelName:     model,
		Provider:      provider,
		Operation:     types.ModelOperationChat,
		Success:       success,
		UsageFinality: finality,
		InputTokens:   in,
		OutputTokens:  out,
	}
}

func TestEstimateSimpleInputOutput(t *testing.T) {
	e := defaultEstimator(t)
	res := e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(100), intp(50)))
	if res.Status != types.PricingStatusPriced {
		t.Fatalf("expected PRICED, got %s/%s", res.Status, res.Reason)
	}
	// 100 * 0.5/M + 50 * 2/M = 0.00005 + 0.0001 = 0.00015 CNY = 150000 nanos.
	if res.EstimatedCostNanos == nil || *res.EstimatedCostNanos != 150_000 {
		t.Fatalf("expected 150000 nanos, got %v", res.EstimatedCostNanos)
	}
	if res.Currency != "CNY" || res.PricingRuleID == "" || res.PricingCatalogHash == "" || res.PricingUnit != BillingUnitPer1MTokens {
		t.Fatalf("priced result missing snapshot facts: %+v", res)
	}
	if res.InputUnitPriceNanos != 500_000_000 || res.OutputUnitPriceNanos != 2_000_000_000 {
		t.Fatalf("unexpected unit prices: %d / %d", res.InputUnitPriceNanos, res.OutputUnitPriceNanos)
	}
}

func TestEstimateZeroTokensPricedZero(t *testing.T) {
	e := defaultEstimator(t)
	res := e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(0), intp(0)))
	if res.Status != types.PricingStatusPriced || *res.EstimatedCostNanos != 0 {
		t.Fatalf("expected PRICED zero, got %s/%s nanos=%v", res.Status, res.Reason, res.EstimatedCostNanos)
	}
}

func TestEstimateOneTokenFractional(t *testing.T) {
	e := defaultEstimator(t)
	res := e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(1), intp(0)))
	if res.Status != types.PricingStatusPriced || *res.EstimatedCostNanos != 500 {
		t.Fatalf("expected 500 nanos, got %v", res.EstimatedCostNanos)
	}
}

func TestEstimateNoExactRule(t *testing.T) {
	e := defaultEstimator(t)
	res := e.Estimate(chatCall("Qwen/Qwen3-8B", "siliconflow", true, types.UsageFinalityReported, intp(1), intp(1)))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonNoExactRule {
		t.Fatalf("expected UNKNOWN/NO_EXACT_RULE, got %s/%s", res.Status, res.Reason)
	}
	if res.EstimatedCostNanos != nil || res.Currency != "" {
		t.Fatal("unknown result must not fabricate amounts")
	}
}

func TestEstimateNonProviderPricedModel(t *testing.T) {
	e := defaultEstimator(t)
	res := e.Estimate(chatCall("Qwen/Qwen3-14B", "openai", true, types.UsageFinalityReported, intp(1), intp(1)))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonNonProviderPricedModel {
		t.Fatalf("expected UNKNOWN/NON_PROVIDER_PRICED_MODEL, got %s/%s", res.Status, res.Reason)
	}
	// Embedding operation within siliconflow is outside the chat-only catalog.
	emb := chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(1), intp(1))
	emb.Operation = types.ModelOperationEmbedding
	res = e.Estimate(emb)
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonNonProviderPricedModel {
		t.Fatalf("expected UNKNOWN/NON_PROVIDER_PRICED_MODEL for embedding, got %s/%s", res.Status, res.Reason)
	}
}

func TestEstimateUsageUnavailable(t *testing.T) {
	e := defaultEstimator(t)
	res := e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityUnavailable, nil, nil))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonUsageUnavailable {
		t.Fatalf("expected UNKNOWN/USAGE_UNAVAILABLE, got %s/%s", res.Status, res.Reason)
	}
}

func TestEstimateUsagePartial(t *testing.T) {
	e := defaultEstimator(t)
	res := e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityPartial, intp(1), intp(1)))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonUsagePartial {
		t.Fatalf("expected UNKNOWN/USAGE_PARTIAL, got %s/%s", res.Status, res.Reason)
	}
}

func TestEstimateInvalidUsage(t *testing.T) {
	e := defaultEstimator(t)
	res := e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(-1), intp(1)))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonInvalidUsage {
		t.Fatalf("expected UNKNOWN/INVALID_USAGE (negative), got %s/%s", res.Status, res.Reason)
	}
	res = e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, nil, intp(1)))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonInvalidUsage {
		t.Fatalf("expected UNKNOWN/INVALID_USAGE (missing input), got %s/%s", res.Status, res.Reason)
	}
	res = e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(1), nil))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonInvalidUsage {
		t.Fatalf("expected UNKNOWN/INVALID_USAGE (missing output), got %s/%s", res.Status, res.Reason)
	}
}

func TestEstimateFailedCallNotZero(t *testing.T) {
	e := defaultEstimator(t)
	// A failed call (e.g. abandoned stream) with reported usage must not be
	// priced from an incomplete final-usage snapshot.
	call := chatCall("Qwen/Qwen3-14B", "siliconflow", false, types.UsageFinalityReported, intp(10), intp(5))
	res := e.Estimate(call)
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonUsageUnavailable {
		t.Fatalf("expected UNKNOWN/USAGE_UNAVAILABLE, got %s/%s", res.Status, res.Reason)
	}
	if res.EstimatedCostNanos != nil {
		t.Fatal("failed call must not be priced to 0")
	}
}

func TestEstimateRuleNotYetValidAndExpired(t *testing.T) {
	// Not yet valid.
	e := customEstimator(t, "2026-09-08T00:00:00Z", "2026-10-06T00:00:00Z", "2026-10-06T00:00:00Z", "0.5", "2", nil, nil)
	res := e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(1), intp(1)))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonRuleNotYetValid {
		t.Fatalf("expected UNKNOWN/RULE_NOT_YET_VALID, got %s/%s", res.Status, res.Reason)
	}
	// Expired.
	e2 := customEstimator(t, "2026-09-01T00:00:00Z", "2026-09-06T00:00:00Z", "2026-09-06T00:00:00Z", "0.5", "2", nil, nil)
	res = e2.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(1), intp(1)))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonRuleExpired {
		t.Fatalf("expected UNKNOWN/RULE_EXPIRED, got %s/%s", res.Status, res.Reason)
	}
}

func TestEstimateReviewAfterExpired(t *testing.T) {
	// Rule interval is active at now, but catalog review_after has lapsed.
	e := customEstimator(t, "2026-09-01T00:00:00Z", "2026-10-06T00:00:00Z", "2026-09-07T00:00:00Z", "0.5", "2", nil, nil)
	res := e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(1), intp(1)))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonRuleExpired {
		t.Fatalf("expected UNKNOWN/RULE_EXPIRED (review overdue), got %s/%s", res.Status, res.Reason)
	}
}

func TestEstimateCacheDiscount(t *testing.T) {
	cr := "0.5"
	e := customEstimator(t, "2026-09-06T17:40:34Z", "2026-10-06T00:00:00Z", "2026-10-06T00:00:00Z", "2", "2", &cr, nil)
	call := chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(100), intp(50))
	call.CacheReadTokens = intp(40)
	call.CacheReportedInputTokens = intp(100)
	res := e.Estimate(call)
	if res.Status != types.PricingStatusPriced {
		t.Fatalf("expected PRICED, got %s/%s", res.Status, res.Reason)
	}
	// uncached 60 * 2/M + cached 40 * 0.5/M + out 50 * 2/M
	// = 120 + 20 + 100 = 240 micro-CNY = 240000 nanos.
	if *res.EstimatedCostNanos != 240_000 {
		t.Fatalf("expected 240000 nanos, got %v", res.EstimatedCostNanos)
	}
}

func TestEstimateCacheMissingTelemetry(t *testing.T) {
	cr := "0.5"
	e := customEstimator(t, "2026-09-06T17:40:34Z", "2026-10-06T00:00:00Z", "2026-10-06T00:00:00Z", "2", "2", &cr, nil)
	call := chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(100), intp(50))
	res := e.Estimate(call)
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonUnobservableBillingDimension {
		t.Fatalf("expected UNKNOWN/UNOBSERVABLE_BILLING_DIMENSION, got %s/%s", res.Status, res.Reason)
	}
}

func TestEstimateCacheReadExceedsInput(t *testing.T) {
	cr := "0.5"
	e := customEstimator(t, "2026-09-06T17:40:34Z", "2026-10-06T00:00:00Z", "2026-10-06T00:00:00Z", "2", "2", &cr, nil)
	call := chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(100), intp(50))
	call.CacheReadTokens = intp(101)
	call.CacheReportedInputTokens = intp(100)
	res := e.Estimate(call)
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonInvalidUsage {
		t.Fatalf("expected UNKNOWN/INVALID_USAGE, got %s/%s", res.Status, res.Reason)
	}
}

func TestEstimateOverflow(t *testing.T) {
	e := defaultEstimator(t)
	max := int(math.MaxInt64)
	res := e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, &max, &max))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonCalculationOverflow {
		t.Fatalf("expected UNKNOWN/CALCULATION_OVERFLOW, got %s/%s", res.Status, res.Reason)
	}
}

func TestEstimateNilCatalog(t *testing.T) {
	e := NewEstimator(nil)
	res := e.Estimate(chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(1), intp(1)))
	if res.Status != types.PricingStatusUnknown || res.Reason != types.PricingReasonCatalogUnavailable {
		t.Fatalf("expected UNKNOWN/CATALOG_UNAVAILABLE, got %s/%s", res.Status, res.Reason)
	}
}

func TestEstimateConcurrentIdentical(t *testing.T) {
	e := defaultEstimator(t)
	call := chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(1234), intp(567))
	var want *PricingResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.Estimate(call)
			mu.Lock()
			if want == nil {
				cp := r
				want = &cp
			}
			mu.Unlock()
			if *r.EstimatedCostNanos != *want.EstimatedCostNanos || r.Status != want.Status || r.PricingCatalogHash != want.PricingCatalogHash {
				t.Errorf("concurrent estimate diverged: %+v vs %+v", r, want)
			}
		}()
	}
	wg.Wait()
}

func TestRoundHalfUpDiv(t *testing.T) {
	cases := []struct {
		raw   int64
		div   int64
		want  int64
	}{
		{0, 1_000_000, 0},
		{1_000_000, 1_000_000, 1},
		{500_000, 1_000_000, 1},   // .5 rounds up
		{499_999, 1_000_000, 0},   // below half rounds down
		{1_500_000, 1_000_000, 2}, // 1.5 rounds up
		{2_400_000, 1_000_000, 2},
	}
	for _, c := range cases {
		got, ok := roundHalfUpDiv(big.NewInt(c.raw), c.div)
		if !ok {
			t.Fatalf("roundHalfUpDiv(%d): unexpected overflow", c.raw)
		}
		if got != c.want {
			t.Errorf("roundHalfUpDiv(%d/%d) = %d, want %d", c.raw, c.div, got, c.want)
		}
	}
}
