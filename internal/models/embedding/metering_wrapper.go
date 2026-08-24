package embedding

import (
	"context"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

type meteredEmbedder struct {
	inner    Embedder
	recorder types.ModelCallRecorder
	provider string
}

func (m *meteredEmbedder) GetModelName() string { return m.inner.GetModelName() }
func (m *meteredEmbedder) GetModelID() string   { return m.inner.GetModelID() }
func (m *meteredEmbedder) GetDimensions() int   { return m.inner.GetDimensions() }
func (m *meteredEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	started := time.Now()
	healthID := m.begin(ctx, started)
	result, err := m.inner.Embed(ctx, text)
	m.record(ctx, healthID, started, err)
	return result, err
}
func (m *meteredEmbedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	started := time.Now()
	healthID := m.begin(ctx, started)
	result, err := m.inner.BatchEmbed(ctx, texts)
	m.record(ctx, healthID, started, err)
	return result, err
}
func (m *meteredEmbedder) BatchEmbedWithPool(ctx context.Context, _ Embedder, texts []string) ([][]float32, error) {
	started := time.Now()
	healthID := m.begin(ctx, started)
	// Pass the inner decorated model so provider sub-batches do not create
	// duplicate ModelCall rows for this one logical pool invocation.
	result, err := m.inner.BatchEmbedWithPool(ctx, m.inner, texts)
	m.record(ctx, healthID, started, err)
	return result, err
}
func (m *meteredEmbedder) begin(ctx context.Context, started time.Time) string {
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
func (m *meteredEmbedder) record(ctx context.Context, healthID string, started time.Time, businessErr error) {
	if m.recorder == nil {
		return
	}
	tenantID, _ := types.TenantIDFromContext(ctx)
	runID, taskID, traceID := types.LLMCallScopeFromContext(ctx)
	purpose, _ := types.LLMCallMetadataFromContext(ctx)
	call := &types.ModelCall{TenantID: tenantID, RunID: embeddingOptionalString(runID), TaskID: embeddingOptionalString(taskID), TraceID: traceID, ModelID: m.inner.GetModelID(), ModelName: m.inner.GetModelName(), Provider: m.provider, Operation: types.ModelOperationEmbedding, Purpose: purpose, UsageFinality: types.UsageFinalityUnavailable, CacheStatus: types.PromptCacheStatusUnsupported, RequestElapsedMS: embeddingElapsedMillis(started), Success: businessErr == nil, AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: started.UTC()}
	if businessErr != nil {
		call.ErrorType = fmt.Sprintf("%T", businessErr)
	}
	if err := m.recorder.RecordModelCall(ctx, healthID, call); err != nil {
		logger.Errorf(ctx, "model call metering failed: %v", err)
	}
}
func wrapEmbeddingMetering(e Embedder, config Config) Embedder {
	if e == nil || config.Recorder == nil {
		return e
	}
	return &meteredEmbedder{inner: e, recorder: config.Recorder, provider: config.Provider}
}
func embeddingOptionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
func embeddingElapsedMillis(start time.Time) int {
	ms := time.Since(start).Milliseconds()
	if ms < 0 {
		return 0
	}
	return int(ms)
}
