package embedding

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

type meteringEmbedFake struct {
	err        error
	batchCalls int
}

func (f *meteringEmbedFake) Embed(context.Context, string) ([]float32, error) {
	return []float32{1}, f.err
}
func (f *meteringEmbedFake) BatchEmbed(_ context.Context, texts []string) ([][]float32, error) {
	f.batchCalls++
	out := make([][]float32, len(texts))
	return out, f.err
}
func (f *meteringEmbedFake) BatchEmbedWithPool(ctx context.Context, model Embedder, texts []string) ([][]float32, error) {
	return model.BatchEmbed(ctx, texts)
}
func (*meteringEmbedFake) GetModelName() string { return "fixture-embed" }
func (*meteringEmbedFake) GetModelID() string   { return "embed-id" }
func (*meteringEmbedFake) GetDimensions() int   { return 1 }

type embedRecorderFake struct {
	calls  []*types.ModelCall
	begins []uint64
	err    error
}

func (r *embedRecorderFake) BeginModelCall(_ context.Context, tenantID uint64, _ time.Time) (string, error) {
	r.begins = append(r.begins, tenantID)
	return "health-" + string(rune('0'+len(r.begins))), nil
}

func (r *embedRecorderFake) RecordModelCall(_ context.Context, _ string, call *types.ModelCall) error {
	r.calls = append(r.calls, call)
	return r.err
}
func embedMeteringContext() context.Context {
	return context.WithValue(context.Background(), types.TenantIDContextKey, uint64(8))
}

func TestMeteredEmbeddingPoolIsOneLogicalCall(t *testing.T) {
	inner := &meteringEmbedFake{}
	recorder := &embedRecorderFake{}
	m := &meteredEmbedder{inner: inner, recorder: recorder, provider: "fixture"}
	if _, err := m.BatchEmbedWithPool(embedMeteringContext(), m, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if inner.batchCalls != 1 || len(recorder.calls) != 1 {
		t.Fatalf("provider batches=%d model calls=%d", inner.batchCalls, len(recorder.calls))
	}
	call := recorder.calls[0]
	if call.RunID != nil || call.InputTokens != nil || call.UsageFinality != types.UsageFinalityUnavailable || call.CacheStatus != types.PromptCacheStatusUnsupported {
		t.Fatalf("call=%+v", call)
	}
}

func TestMeteredEmbeddingPersistenceFailureDoesNotChangeBusinessResult(t *testing.T) {
	recorder := &embedRecorderFake{err: errors.New("injected")}
	m := &meteredEmbedder{inner: &meteringEmbedFake{}, recorder: recorder}
	result, err := m.Embed(embedMeteringContext(), "secret")
	if err != nil || len(result) != 1 {
		t.Fatalf("business result changed: %v %v", result, err)
	}
}

// TestMeteredEmbeddingProviderErrorIsRecordedAsFailure covers the provider-error
// path: the business error is returned unchanged and the call is recorded as a
// failure with a non-empty error classification.
func TestMeteredEmbeddingProviderErrorIsRecordedAsFailure(t *testing.T) {
	recorder := &embedRecorderFake{}
	providerErr := errors.New("provider 502")
	m := &meteredEmbedder{inner: &meteringEmbedFake{err: providerErr}, recorder: recorder, provider: "fixture"}
	_, err := m.Embed(embedMeteringContext(), "secret text")
	if !errors.Is(err, providerErr) {
		t.Fatalf("provider error changed: %v", err)
	}
	if len(recorder.calls) != 1 || recorder.calls[0].Success || recorder.calls[0].ErrorType == "" {
		t.Fatalf("provider error not recorded as failure: %+v", recorder.calls)
	}
}
