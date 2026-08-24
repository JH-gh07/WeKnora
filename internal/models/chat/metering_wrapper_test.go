package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

type meteringChatFake struct {
	response *types.ChatResponse
	err      error
	stream   []types.StreamResponse
}

func (f *meteringChatFake) Chat(context.Context, []Message, *ChatOptions) (*types.ChatResponse, error) {
	return f.response, f.err
}
func (f *meteringChatFake) ChatStream(context.Context, []Message, *ChatOptions) (<-chan types.StreamResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan types.StreamResponse, len(f.stream))
	for _, item := range f.stream {
		ch <- item
	}
	close(ch)
	return ch, nil
}
func (*meteringChatFake) GetModelName() string { return "fixture-chat" }
func (*meteringChatFake) GetModelID() string   { return "chat-id" }

type chatRecorderFake struct {
	calls  []*types.ModelCall
	begins []uint64
	err    error
}

func (r *chatRecorderFake) BeginModelCall(_ context.Context, tenantID uint64, _ time.Time) (string, error) {
	r.begins = append(r.begins, tenantID)
	return "health-" + string(rune('0'+len(r.begins))), nil
}

func (r *chatRecorderFake) RecordModelCall(_ context.Context, _ string, call *types.ModelCall) error {
	r.calls = append(r.calls, call)
	return r.err
}

func meteringContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(42))
	ctx = types.WithLLMCallScope(ctx, "run-1", "task-1", "trace-1")
	return types.WithLLMCallMetadata(ctx, "evaluation", "hash-only")
}

func TestMeteredChatCapturesUsageAndScope(t *testing.T) {
	recorder := &chatRecorderFake{}
	usage := types.TokenUsage{PromptTokens: 12, CompletionTokens: 3}
	usage.SetPromptCacheUsage(4, 0, 8, true)
	m := &meteredChat{inner: &meteringChatFake{response: &types.ChatResponse{Content: "secret response", Usage: usage}}, recorder: recorder, provider: "fixture"}
	resp, err := m.Chat(meteringContext(), []Message{{Content: "secret prompt"}}, nil)
	if err != nil || resp.Content != "secret response" {
		t.Fatalf("business result changed: %+v %v", resp, err)
	}
	if len(recorder.calls) != 1 {
		t.Fatalf("calls=%d", len(recorder.calls))
	}
	call := recorder.calls[0]
	if call.TenantID != 42 || call.RunID == nil || *call.RunID != "run-1" || call.InputTokens == nil || *call.InputTokens != 12 || call.CacheStatus != types.PromptCacheStatusHit {
		t.Fatalf("call=%+v", call)
	}
}

func TestMeteredChatPersistenceFailureDoesNotChangeBusinessResult(t *testing.T) {
	recorder := &chatRecorderFake{err: errors.New("injected persistence failure")}
	m := &meteredChat{inner: &meteringChatFake{response: &types.ChatResponse{Content: "ok"}}, recorder: recorder}
	resp, err := m.Chat(meteringContext(), nil, nil)
	if err != nil || resp.Content != "ok" {
		t.Fatalf("metering failure leaked: resp=%+v err=%v", resp, err)
	}
	if recorder.calls[0].UsageFinality != types.UsageFinalityUnavailable || recorder.calls[0].InputTokens != nil {
		t.Fatalf("unavailable usage falsified: %+v", recorder.calls[0])
	}
}

func TestMeteredChatStreamRecordsOnceOnClose(t *testing.T) {
	recorder := &chatRecorderFake{}
	usage := &types.TokenUsage{PromptTokens: 2, CompletionTokens: 1}
	m := &meteredChat{inner: &meteringChatFake{stream: []types.StreamResponse{{Content: "a"}, {Done: true, Usage: usage}}}, recorder: recorder}
	ch, err := m.ChatStream(meteringContext(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if len(recorder.calls) != 1 || recorder.calls[0].InputTokens == nil || *recorder.calls[0].InputTokens != 2 {
		t.Fatalf("stream calls=%+v", recorder.calls)
	}
	if len(recorder.begins) != 1 || recorder.begins[0] != 42 {
		t.Fatalf("begin tenant=%v", recorder.begins)
	}
}

// TestMeteredChatProviderErrorIsRecordedAsFailure covers the non-streaming
// provider-error path: the business error is returned unchanged and the call
// is recorded as a failure with a non-empty error classification.
func TestMeteredChatProviderErrorIsRecordedAsFailure(t *testing.T) {
	recorder := &chatRecorderFake{}
	providerErr := errors.New("provider 500")
	m := &meteredChat{inner: &meteringChatFake{err: providerErr}, recorder: recorder, provider: "fixture"}
	_, err := m.Chat(meteringContext(), nil, nil)
	if !errors.Is(err, providerErr) {
		t.Fatalf("provider error changed: %v", err)
	}
	if len(recorder.calls) != 1 || recorder.calls[0].Success || recorder.calls[0].ErrorType == "" {
		t.Fatalf("provider error not recorded as failure: %+v", recorder.calls)
	}
}

type abandonableChatFake struct{ responses []types.StreamResponse }

func (f *abandonableChatFake) Chat(context.Context, []Message, *ChatOptions) (*types.ChatResponse, error) {
	return nil, errors.New("unused")
}
func (f *abandonableChatFake) ChatStream(_ context.Context, _ []Message, _ *ChatOptions) (<-chan types.StreamResponse, error) {
	ch := make(chan types.StreamResponse)
	go func() {
		defer close(ch)
		for _, r := range f.responses {
			ch <- r // blocks until the wrapper reads; independent of ctx so abandon is exercised
		}
	}()
	return ch, nil
}
func (*abandonableChatFake) GetModelName() string { return "fixture-chat" }
func (*abandonableChatFake) GetModelID() string   { return "chat-id" }

// TestMeteredChatStreamAbandonRecordsExactlyOnce exercises the consumer-abandon
// path: the producer blocks on the send, the consumer cancels, and the wrapper
// drains + finalizes exactly once instead of leaking.
func TestMeteredChatStreamAbandonRecordsExactlyOnce(t *testing.T) {
	recorder := &chatRecorderFake{}
	inner := &abandonableChatFake{responses: []types.StreamResponse{{Content: "a"}, {Content: "b"}}}
	m := &meteredChat{inner: inner, recorder: recorder, provider: "fixture"}
	ctx, cancel := context.WithCancel(meteringContext())
	ch, err := m.ChatStream(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := <-ch; !ok {
		t.Fatal("expected first response")
	}
	cancel()
	for range ch {
	}
	if len(recorder.calls) != 1 {
		t.Fatalf("abandoned stream should record exactly once, got %d", len(recorder.calls))
	}
	if recorder.calls[0].Success {
		t.Fatalf("abandoned stream should be non-success: %+v", recorder.calls[0])
	}
}
