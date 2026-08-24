package rerank

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

type meteringRerankFake struct{ err error }

func (f *meteringRerankFake) Rerank(context.Context, string, []string) ([]RankResult, error) {
	return []RankResult{{Index: 0}}, f.err
}
func (*meteringRerankFake) GetModelName() string { return "fixture-rerank" }
func (*meteringRerankFake) GetModelID() string   { return "rerank-id" }

type rerankRecorderFake struct {
	calls  []*types.ModelCall
	begins []uint64
	err    error
}

func (r *rerankRecorderFake) BeginModelCall(_ context.Context, tenantID uint64, _ time.Time) (string, error) {
	r.begins = append(r.begins, tenantID)
	return "health-" + string(rune('0'+len(r.begins))), nil
}

func (r *rerankRecorderFake) RecordModelCall(_ context.Context, _ string, call *types.ModelCall) error {
	r.calls = append(r.calls, call)
	return r.err
}

func TestMeteredRerankProviderAndPersistenceFailuresAreSeparated(t *testing.T) {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(9))
	t.Run("provider failure", func(t *testing.T) {
		recorder := &rerankRecorderFake{}
		providerErr := errors.New("provider down")
		m := &meteredReranker{inner: &meteringRerankFake{err: providerErr}, recorder: recorder}
		_, err := m.Rerank(ctx, "secret query", []string{"secret doc"})
		if !errors.Is(err, providerErr) || len(recorder.calls) != 1 || recorder.calls[0].Success {
			t.Fatalf("err=%v calls=%+v", err, recorder.calls)
		}
	})
	t.Run("persistence failure", func(t *testing.T) {
		recorder := &rerankRecorderFake{err: errors.New("injected persistence failure")}
		m := &meteredReranker{inner: &meteringRerankFake{}, recorder: recorder}
		result, err := m.Rerank(ctx, "secret query", []string{"secret doc"})
		if err != nil || len(result) != 1 || !recorder.calls[0].Success {
			t.Fatalf("business result changed: result=%v err=%v calls=%+v", result, err, recorder.calls)
		}
		if recorder.calls[0].InputTokens != nil || recorder.calls[0].UsageFinality != types.UsageFinalityUnavailable {
			t.Fatalf("estimated usage claimed as provider usage: %+v", recorder.calls[0])
		}
	})
}
