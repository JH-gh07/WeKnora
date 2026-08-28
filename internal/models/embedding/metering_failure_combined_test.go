package embedding

// Task008 GAP-2: provider failure AND metering persistence failure combined.

import (
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestMeteredEmbeddingCombinedProviderAndPersistenceFailure(t *testing.T) {
	recorder := &embedRecorderFake{err: errors.New("injected persistence failure")}
	providerErr := errors.New("provider down")
	m := &meteredEmbedder{inner: &meteringEmbedFake{err: providerErr}, recorder: recorder, provider: "fixture"}

	_, err := m.BatchEmbedWithPool(embedMeteringContext(), m, []string{"a", "b"})
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
