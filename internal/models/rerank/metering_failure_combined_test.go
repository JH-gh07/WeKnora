package rerank

// Task008 GAP-2: provider failure AND metering persistence failure combined.

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestMeteredRerankCombinedProviderAndPersistenceFailure(t *testing.T) {
	recorder := &rerankRecorderFake{err: errors.New("injected persistence failure")}
	providerErr := errors.New("provider down")
	m := &meteredReranker{inner: &meteringRerankFake{err: providerErr}, recorder: recorder}

	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(9))
	_, err := m.Rerank(ctx, "secret query", []string{"secret doc"})
	if !errors.Is(err, providerErr) {
		t.Fatalf("business error changed: got %v want %v", err, providerErr)
	}
	if len(recorder.calls) != 1 {
		t.Fatalf("expected exactly one logical failure fact attempt, got %d", len(recorder.calls))
	}
	call := recorder.calls[0]
	if call.Success || call.ErrorType == "" || call.UsageFinality != types.UsageFinalityUnavailable {
		t.Fatalf("failure fact misrecorded: success=%v error_type=%q finality=%s", call.Success, call.ErrorType, call.UsageFinality)
	}
}
