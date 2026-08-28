package chat

import (
	"context"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

type meteredChat struct {
	inner    Chat
	recorder types.ModelCallRecorder
	provider string
}

func (m *meteredChat) GetModelName() string { return m.inner.GetModelName() }
func (m *meteredChat) GetModelID() string   { return m.inner.GetModelID() }

func (m *meteredChat) Chat(ctx context.Context, messages []Message, opts *ChatOptions) (*types.ChatResponse, error) {
	started := time.Now()
	healthID := m.begin(ctx, started)
	resp, err := m.inner.Chat(ctx, messages, opts)
	call := m.baseCall(ctx, started, err)
	if resp != nil {
		applyChatUsage(call, &resp.Usage)
	}
	m.record(ctx, healthID, call)
	return resp, err
}

func (m *meteredChat) ChatStream(ctx context.Context, messages []Message, opts *ChatOptions) (<-chan types.StreamResponse, error) {
	started := time.Now()
	healthID := m.begin(ctx, started)
	ch, err := m.inner.ChatStream(ctx, messages, opts)
	if err != nil || ch == nil {
		m.record(ctx, healthID, m.baseCall(ctx, started, err))
		return ch, err
	}
	out := make(chan types.StreamResponse)
	go func() {
		defer close(out)
		var usage *types.TokenUsage
		streamFailed := false
		abandoned := false
		// Finalize exactly once regardless of how the stream ends (full drain,
		// error, or consumer abandonment), so no logical call is ever lost.
		defer func() {
			var streamErr error
			if streamFailed {
				streamErr = fmt.Errorf("stream_error")
			} else if abandoned {
				streamErr = fmt.Errorf("stream_abandoned")
			}
			call := m.baseCall(ctx, started, streamErr)
			applyChatUsage(call, usage)
			m.record(ctx, healthID, call)
		}()
		for resp := range ch {
			if resp.Usage != nil {
				snapshot := *resp.Usage
				usage = &snapshot
			}
			if resp.ResponseType == types.ResponseTypeError {
				streamFailed = true
			}
			select {
			case out <- resp:
			case <-ctx.Done():
				// Consumer abandoned the stream. Drain upstream in the
				// background so the producer can exit, then finalize once.
				abandoned = true
				go func() {
					for range ch {
					}
				}()
				return
			}
		}
	}()
	return out, nil
}

func (m *meteredChat) baseCall(ctx context.Context, started time.Time, businessErr error) *types.ModelCall {
	tenantID, _ := types.TenantIDFromContext(ctx)
	runID, taskID, traceID := types.LLMCallScopeFromContext(ctx)
	purpose, _ := types.LLMCallMetadataFromContext(ctx)
	call := &types.ModelCall{TenantID: tenantID, RunID: optionalString(runID), TaskID: optionalString(taskID), TraceID: traceID, ModelID: m.inner.GetModelID(), ModelName: m.inner.GetModelName(), Provider: m.provider, Operation: types.ModelOperationChat, Purpose: purpose, UsageFinality: types.UsageFinalityUnavailable, CacheStatus: types.PromptCacheStatusUnreported, RequestElapsedMS: elapsedMillis(started), Success: businessErr == nil, AttemptObservability: types.AttemptObservabilityUnobservable, CreatedAt: started.UTC()}
	if businessErr != nil {
		call.ErrorType = fmt.Sprintf("%T", businessErr)
	}
	return call
}

func applyChatUsage(call *types.ModelCall, usage *types.TokenUsage) {
	if usage == nil {
		return
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 && usage.CacheStatus == "" && !usage.CacheReported {
		return
	}
	call.InputTokens = intPointer(usage.PromptTokens)
	call.OutputTokens = intPointer(usage.CompletionTokens)
	call.UsageFinality = types.UsageFinalityReported
	call.CacheStatus = usage.CacheStatus
	if call.CacheStatus == "" {
		call.CacheStatus = types.PromptCacheStatusUnreported
	}
	if usage.CacheReported {
		call.CacheReadTokens = intPointer(usage.CacheReadTokens)
		call.CacheWriteTokens = intPointer(usage.CacheWriteTokens)
		call.CacheMissTokens = intPointer(usage.CacheMissTokens)
		call.CacheReportedInputTokens = intPointer(usage.PromptTokens)
	}
}

func (m *meteredChat) begin(ctx context.Context, started time.Time) string {
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

func (m *meteredChat) record(ctx context.Context, healthID string, call *types.ModelCall) {
	if m.recorder == nil {
		return
	}
	if err := m.recorder.RecordModelCall(ctx, healthID, call); err != nil {
		logger.Errorf(ctx, "model call metering failed: %v", err)
	}
}

func wrapChatMetering(c Chat, config *ChatConfig, err error) (Chat, error) {
	if err != nil || c == nil || config == nil || config.Recorder == nil {
		return c, err
	}
	return &meteredChat{inner: c, recorder: config.Recorder, provider: config.Provider}, nil
}

func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
func intPointer(v int) *int { return &v }
func elapsedMillis(start time.Time) int {
	ms := time.Since(start).Milliseconds()
	if ms < 0 {
		return 0
	}
	return int(ms)
}
