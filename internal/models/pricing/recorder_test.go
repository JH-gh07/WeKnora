package pricing

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

type mockRepo struct {
	began    int
	recorded *types.ModelCall
}

func (m *mockRepo) BeginModelCall(ctx context.Context, tenantID uint64, at time.Time) (string, error) {
	m.began++
	return "health-1", nil
}
func (m *mockRepo) RecordModelCall(ctx context.Context, healthID string, call *types.ModelCall) error {
	m.recorded = call
	return nil
}
func (m *mockRepo) CreateModelCall(ctx context.Context, call *types.ModelCall) error { return nil }
func (m *mockRepo) GetModelCall(ctx context.Context, tenantID uint64, id string) (*types.ModelCall, error) {
	return nil, nil
}
func (m *mockRepo) AggregateModelCalls(ctx context.Context, filter types.ModelCallFilter) (*types.ModelUsageAggregate, error) {
	return nil, nil
}
func (m *mockRepo) RecordMeteringAttempt(ctx context.Context, tenantID uint64, at time.Time, persisted bool) error {
	return nil
}
func (m *mockRepo) GetMeasurementHealth(ctx context.Context, tenantID uint64, from, to time.Time) (*types.MeasurementHealth, error) {
	return nil, nil
}
func (m *mockRepo) GetRunMeasurementHealth(ctx context.Context, tenantID uint64, runID string) (*types.MeasurementHealth, error) {
	return nil, nil
}

func TestRecorderSelectedModelPriced(t *testing.T) {
	repo := &mockRepo{}
	rec := NewPricingRecorder(repo, defaultEstimator(t))
	call := chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(100), intp(50))
	if err := rec.RecordModelCall(context.Background(), "health-1", call); err != nil {
		t.Fatal(err)
	}
	if repo.recorded != call {
		t.Fatal("recorder must delegate the same call")
	}
	if call.PricingStatus != types.PricingStatusPriced {
		t.Fatalf("expected PRICED, got %s/%s", call.PricingStatus, call.PricingReason)
	}
	if call.EstimatedCostNanos == nil || *call.EstimatedCostNanos != 150_000 {
		t.Fatalf("expected 150000 nanos, got %v", call.EstimatedCostNanos)
	}
	if call.EstimatedCost == nil || *call.EstimatedCost != 0.00015 {
		t.Fatalf("compat cost must equal nanos/1e9, got %v", call.EstimatedCost)
	}
	if call.Currency != "CNY" || call.PricingRuleID == "" || call.PricingCatalogHash == "" || call.PricingVersion == "" || call.PricingSource == "" || call.PricingUnit == "" {
		t.Fatalf("priced call missing pricing snapshot: %+v", call)
	}
	if call.InputUnitPriceNanos == nil || *call.InputUnitPriceNanos != 500_000_000 {
		t.Fatalf("missing input unit price: %+v", call)
	}
}

func TestRecorderOtherModelUnknown(t *testing.T) {
	repo := &mockRepo{}
	rec := NewPricingRecorder(repo, defaultEstimator(t))
	call := chatCall("Qwen/Qwen3-8B", "siliconflow", true, types.UsageFinalityReported, intp(1), intp(1))
	if err := rec.RecordModelCall(context.Background(), "health-1", call); err != nil {
		t.Fatal(err)
	}
	if call.PricingStatus != types.PricingStatusUnknown || call.PricingReason != types.PricingReasonNoExactRule {
		t.Fatalf("expected UNKNOWN/NO_EXACT_RULE, got %s/%s", call.PricingStatus, call.PricingReason)
	}
	if call.EstimatedCost != nil || call.EstimatedCostNanos != nil || call.Currency != "" {
		t.Fatal("unknown call must not fabricate amounts")
	}
}

func TestRecorderProviderFailureUnknown(t *testing.T) {
	repo := &mockRepo{}
	rec := NewPricingRecorder(repo, defaultEstimator(t))
	call := chatCall("Qwen/Qwen3-14B", "siliconflow", false, types.UsageFinalityUnavailable, nil, nil)
	if err := rec.RecordModelCall(context.Background(), "health-1", call); err != nil {
		t.Fatal(err)
	}
	if call.Success {
		t.Fatal("recorder must not change provider business outcome")
	}
	if call.PricingStatus != types.PricingStatusUnknown || call.PricingReason != types.PricingReasonUsageUnavailable {
		t.Fatalf("expected UNKNOWN/USAGE_UNAVAILABLE, got %s/%s", call.PricingStatus, call.PricingReason)
	}
}

func TestRecorderEstimatorNil(t *testing.T) {
	repo := &mockRepo{}
	rec := NewPricingRecorder(repo, nil)
	call := chatCall("Qwen/Qwen3-14B", "siliconflow", true, types.UsageFinalityReported, intp(1), intp(1))
	if err := rec.RecordModelCall(context.Background(), "health-1", call); err != nil {
		t.Fatal(err)
	}
	if call.PricingStatus != types.PricingStatusUnknown || call.PricingReason != types.PricingReasonCatalogUnavailable {
		t.Fatalf("expected UNKNOWN/CATALOG_UNAVAILABLE, got %s/%s", call.PricingStatus, call.PricingReason)
	}
}

func TestRecorderBeginDelegates(t *testing.T) {
	repo := &mockRepo{}
	rec := NewPricingRecorder(repo, defaultEstimator(t))
	id, err := rec.BeginModelCall(context.Background(), 42, time.Now())
	if err != nil || id != "health-1" || repo.began != 1 {
		t.Fatalf("begin must delegate: id=%q err=%v began=%d", id, err, repo.began)
	}
}
