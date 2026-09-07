package langfuse

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

// dummyPayload is a minimal payload that embeds TracingContext, mirroring
// how real asynq payloads opt into trace propagation.
type dummyPayload struct {
	types.TracingContext
	KnowledgeID string `json:"knowledge_id"`
}

// TestInjectTracing_DisabledIsZero verifies InjectTracing is a no-op when
// Langfuse is disabled: no panics, no trace fields written.
func TestInjectTracing_DisabledIsZero(t *testing.T) {
	_, _ = Init(Config{Enabled: false})

	p := &dummyPayload{KnowledgeID: "k1"}
	InjectTracing(context.Background(), p)
	if p.LangfuseTraceparent != "" || p.LangfuseTraceID != "" {
		t.Fatalf("expected no tracing fields on disabled manager, got %+v", p.TracingContext)
	}
}

func TestInjectTracing_DisabledStillCarriesModelCallScope(t *testing.T) {
	_, _ = Init(Config{Enabled: false})

	ctx := types.WithLLMCallScope(context.Background(), "run-1", "task-1", "trace-1")
	p := &dummyPayload{KnowledgeID: "k1"}
	InjectTracing(ctx, p)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"llm_run_id":"run-1"`,
		`"llm_task_id":"task-1"`,
		`"llm_trace_id":"trace-1"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("payload %s does not contain %s", raw, want)
		}
	}
}

func TestAsynqMiddleware_RestoresModelCallScopeWhenLangfuseDisabled(t *testing.T) {
	_, _ = Init(Config{Enabled: false})

	raw := []byte(`{"llm_run_id":"run-1","llm_task_id":"task-1","llm_trace_id":"trace-1"}`)
	var gotRun, gotTask, gotTrace string
	mw := AsynqMiddleware()(asynq.HandlerFunc(func(ctx context.Context, _ *asynq.Task) error {
		gotRun, gotTask, gotTrace = types.LLMCallScopeFromContext(ctx)
		return nil
	}))
	if err := mw.ProcessTask(context.Background(), asynq.NewTask("test:type", raw)); err != nil {
		t.Fatal(err)
	}
	if gotRun != "run-1" || gotTask != "task-1" || gotTrace != "trace-1" {
		t.Fatalf("restored scope = (%q, %q, %q)", gotRun, gotTask, gotTrace)
	}
}

// TestInjectTracing_PopulatesTraceparent checks that when a trace is active
// on the context, a W3C traceparent is stamped onto the payload (so the
// asynq worker can resume the same trace).
func TestInjectTracing_PopulatesTraceparent(t *testing.T) {
	m, _ := newTestManager(t)

	ctx, trace := m.StartTrace(context.Background(), TraceOptions{Name: "parent"})
	p := &dummyPayload{KnowledgeID: "k1"}
	InjectTracing(ctx, p)

	if p.LangfuseTraceparent == "" {
		t.Fatal("expected LangfuseTraceparent to be populated")
	}
	// The traceparent carries the trace id; trace.ID is the OTel trace id.
	if !strings.HasPrefix(p.LangfuseTraceparent, "00-"+trace.ID) {
		t.Errorf("traceparent %q does not carry trace id %s", p.LangfuseTraceparent, trace.ID)
	}
	if p.LangfuseTraceID != trace.ID {
		t.Errorf("LangfuseTraceID = %q, want %q", p.LangfuseTraceID, trace.ID)
	}
}

// TestAsynqMiddleware_TraceparentPropagation is the cross-process correlation
// core test: InjectTracing stamps a traceparent onto the payload; the worker
// middleware re-extracts it, and the worker span inherits the upstream trace
// id — stitching the HTTP trace and the async job into one LiteFuse tree.
func TestAsynqMiddleware_TraceparentPropagation(t *testing.T) {
	m, exp := newTestManager(t)

	// Upstream caller opens a span and injects a traceparent onto the payload.
	upstreamCtx, upstreamSpan := m.Tracer().Start(context.Background(), "upstream-http")
	remoteTraceID := upstreamSpan.SpanContext().TraceID()
	payload := &dummyPayload{KnowledgeID: "k1"}
	InjectTracing(upstreamCtx, payload)
	if payload.LangfuseTraceparent == "" {
		t.Fatal("InjectTracing did not stamp a traceparent")
	}
	raw, _ := json.Marshal(payload)

	mw := AsynqMiddleware()(asynq.HandlerFunc(func(context.Context, *asynq.Task) error { return nil }))
	if err := mw.ProcessTask(context.Background(), asynq.NewTask("test:type", raw)); err != nil {
		t.Fatalf("handler err: %v", err)
	}

	for _, s := range exp.GetSpans() {
		if s.Name != "asynq.test:type" {
			continue
		}
		if s.SpanContext.TraceID() != remoteTraceID {
			t.Errorf("worker span trace id = %s, want upstream %s (traceparent not propagated)",
				s.SpanContext.TraceID(), remoteTraceID)
		}
		return
	}
	t.Fatal("asynq worker span not exported")
}

// TestAsynqMiddleware_StandaloneTrace asserts that when the payload carries
// NO upstream traceparent (e.g. a scheduled job), the middleware opens a
// standalone trace named after the task type.
func TestAsynqMiddleware_StandaloneTrace(t *testing.T) {
	_, exp := newTestManager(t)

	payload := &dummyPayload{KnowledgeID: "kX"}
	raw, _ := json.Marshal(payload)

	mw := AsynqMiddleware()(asynq.HandlerFunc(func(context.Context, *asynq.Task) error { return nil }))
	if err := mw.ProcessTask(context.Background(), asynq.NewTask("scheduled:ping", raw)); err != nil {
		t.Fatalf("handler err: %v", err)
	}

	// The standalone run opens a root trace span ("asynq.scheduled:ping",
	// type=trace) plus a worker span with the same name (type=span).
	var sawRoot, sawSpan bool
	for _, s := range exp.GetSpans() {
		if s.Name != "asynq.scheduled:ping" {
			continue
		}
		switch spanType(s) {
		case obsTypeTrace:
			sawRoot = true
		case obsTypeSpan:
			sawSpan = true
		}
	}
	if !sawRoot {
		t.Error("standalone run should open a root trace span named asynq.scheduled:ping")
	}
	if !sawSpan {
		t.Error("standalone run should open a worker span")
	}
}
