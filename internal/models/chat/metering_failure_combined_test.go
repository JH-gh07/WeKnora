package chat

// Task008 GAP-2: provider failure AND metering persistence failure combined.
// Frozen contract: business error is preserved; the failure fact is attempted
// (durable STARTED marker semantics live in the repository layer) and the
// observation gap stays visible — never a fabricated COMPLETE.

import (
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestMeteredChatCombinedProviderAndPersistenceFailure(t *testing.T) {
	recorder := &chatRecorderFake{err: errors.New("injected persistence failure")}
	providerErr := errors.New("provider down")
	m := &meteredChat{inner: &meteringChatFake{err: providerErr}, recorder: recorder, provider: "fixture"}

	resp, err := m.Chat(meteringContext(), []Message{{Role: "user", Content: "q"}}, &ChatOptions{})
	if !errors.Is(err, providerErr) {
		t.Fatalf("business error changed: got %v want %v", err, providerErr)
	}
	if resp != nil {
		t.Fatalf("provider failure must not fabricate a response: %+v", resp)
	}
	if len(recorder.calls) != 1 {
		t.Fatalf("expected exactly one logical failure fact attempt, got %d", len(recorder.calls))
	}
	call := recorder.calls[0]
	if call.Success || call.ErrorType == "" || call.UsageFinality != types.UsageFinalityUnavailable {
		t.Fatalf("failure fact misrecorded: success=%v error_type=%q finality=%s", call.Success, call.ErrorType, call.UsageFinality)
	}
}
