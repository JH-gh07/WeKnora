package rerank

import (
	"context"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

type meteredReranker struct {
	inner    Reranker
	recorder types.ModelCallRecorder
	provider string
}

func (m *meteredReranker) GetModelName() string { return m.inner.GetModelName() }
func (m *meteredReranker) GetModelID() string   { return m.inner.GetModelID() }
func (m *meteredReranker) Rerank(ctx context.Context, query string, documents []string) ([]RankResult, error) {
	started := time.Now()
	healthID := m.begin(ctx, started)
	result, businessErr := m.inner.Rerank(ctx, query, documents)
	m.record(ctx, healthID, started, businessErr)
	return result, businessErr
}
func (m *meteredReranker) begin(ctx context.Context, started time.Time) string {
	if m.recorder == nil {
		return ""
	}
	tenantID, _ := types.TenantIDFromContext(ctx)
	healthID, err := m.recorder.BeginModelCall(ctx, tenantID, started)
	if err != nil {
		logger.Errorf(ctx, "model call metering begin failed: %v", err)
		return ""
	}
	return healthID
}
func (m *meteredReranker) record(ctx context.Context, healthID string, started time.Time, businessErr error) {
	if m.recorder == nil {
		return
	}
	tenantID, _ := types.TenantIDFromContext(ctx)
	runID, taskID, traceID := types.LLMCallScopeFromContext(ctx)
	purpose, _ := types.LLMCallMetadataFromContext(ctx)
	call := &types.ModelCall{TenantID: tenantID, RunID: rerankOptionalString(runID), TaskID: rerankOptionalString(taskID), TraceID: traceID, ModelID: m.inner.GetModelID(), ModelName: m.inner.GetModelName(), Provider: m.provider, Operation: types.ModelOperationRerank, Purpose: purpose, UsageFinality: types.UsageFinalityUnavailable, CacheStatus: types.PromptCacheStatusUnsupported, RequestElapsedMS: rerankElapsedMillis(started), Success: businessErr == nil, AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: started.UTC()}
	if businessErr != nil {
		call.ErrorType = fmt.Sprintf("%T", businessErr)
	}
	if err := m.recorder.RecordModelCall(ctx, healthID, call); err != nil {
		logger.Errorf(ctx, "model call metering failed: %v", err)
	}
}
func wrapRerankerMetering(r Reranker, config *RerankerConfig, err error) (Reranker, error) {
	if err != nil || r == nil || config == nil || config.Recorder == nil {
		return r, err
	}
	return &meteredReranker{inner: r, recorder: config.Recorder, provider: config.Provider}, nil
}
func rerankOptionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
func rerankElapsedMillis(start time.Time) int {
	ms := time.Since(start).Milliseconds()
	if ms < 0 {
		return 0
	}
	return int(ms)
}
