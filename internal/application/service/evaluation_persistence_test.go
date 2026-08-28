package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// --- Error sanitization ---

// TestMarkRunFailedPersistsSafeGenericMessage verifies that a raw provider
// error (which may embed prompt text or a token) is never persisted to the run
// row. Only the stable error_type and a fixed, allowlisted generic message are
// written; the detailed error goes to the log only.
func TestMarkRunFailedPersistsSafeGenericMessage(t *testing.T) {
	svc := &EvaluationService{runRepository: &fakeRunRepository{}}
	run := &types.EvaluationRun{RunID: "run-1", TaskID: "task-1", TenantID: 1}

	err := errors.New("provider failed: request body prompt='what is the capital of france' api_key=TEST_REDACTION_SENTINEL")
	svc.markRunFailed(context.Background(), run, "EVALUATION_FAILED", err)

	if run.Status != types.EvaluationRunStatusFailed {
		t.Fatalf("status must be FAILED, got %s", run.Status)
	}
	if run.ErrorType != "EVALUATION_FAILED" {
		t.Fatalf("error_type must be stable category, got %q", run.ErrorType)
	}
	if strings.Contains(run.ErrorMessage, "what is the capital") || strings.Contains(run.ErrorMessage, "TEST_REDACTION_SENTINEL") {
		t.Fatalf("raw error/prompt/token leaked into error_message: %q", run.ErrorMessage)
	}
	if run.ErrorMessage != "evaluation execution failed" {
		t.Fatalf("expected safe generic message, got %q", run.ErrorMessage)
	}
	if run.MetricsValid {
		t.Fatalf("metrics_valid must be downgraded on failure")
	}
	if run.EndedAt == nil {
		t.Fatalf("ended_at must be set")
	}
}

// TestMarkRunFailedUnknownCategoryFallsBackToType verifies an unknown error
// category persists only the category name, never the raw error.
func TestMarkRunFailedUnknownCategoryFallsBackToType(t *testing.T) {
	svc := &EvaluationService{runRepository: &fakeRunRepository{}}
	run := &types.EvaluationRun{RunID: "run-2", TaskID: "task-2", TenantID: 1}

	err := errors.New("sensitive detail: user prompt='reveal the password'")
	svc.markRunFailed(context.Background(), run, "UNKNOWN_CATEGORY", err)

	if run.ErrorType != "UNKNOWN_CATEGORY" {
		t.Fatalf("error_type must be kept, got %q", run.ErrorType)
	}
	if run.ErrorMessage != "UNKNOWN_CATEGORY" {
		t.Fatalf("unknown category must fall back to the category name, got %q", run.ErrorMessage)
	}
	if strings.Contains(run.ErrorMessage, "reveal the password") {
		t.Fatalf("raw error leaked: %q", run.ErrorMessage)
	}
}

// --- Fail-closed cleanup on ingest failure (P1 orphan window) ---

type failIngestKnowledgeService struct {
	interfaces.KnowledgeService
}

func (f *failIngestKnowledgeService) CreateKnowledgeFromPassageSync(
	_ context.Context, _ string, _ []string, _ string,
) (*types.Knowledge, error) {
	return nil, context.DeadlineExceeded
}

type recordDeleteKBService struct {
	interfaces.KnowledgeBaseService
	deleted []string
}

func (f *recordDeleteKBService) DeleteKnowledgeBase(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func TestEvalDatasetCleansTemporaryKBOnIngestFailure(t *testing.T) {
	// Knowledge ingestion fails BEFORE the old cleanup defer used to be
	// installed; the new early-installed defer must still delete the temp KB.
	kbSvc := &recordDeleteKBService{}
	runRepo := &fakeRunRepository{}
	svc := &EvaluationService{
		dataset:              &fakeDatasetService{pairs: []*types.QAPair{{QID: 0, Question: "q", PIDs: []int{0}, Passages: []string{"p"}, Answer: "a"}}},
		knowledgeService:     &failIngestKnowledgeService{},
		knowledgeBaseService: kbSvc,
		sessionService:       &fakeSessionService{},
		runRepository:        runRepo,
	}

	run := &types.EvaluationRun{RunID: "run-1", TaskID: "task-1"}
	detail := &types.EvaluationDetail{
		Task:   &types.EvaluationTask{ID: "task-1", DatasetID: "default"},
		Params: &types.ChatManage{PipelineRequest: types.PipelineRequest{}},
	}

	if err := svc.EvalDataset(context.Background(), run, detail, "kb-1"); err == nil {
		t.Fatalf("expected ingest failure error")
	}
	if len(kbSvc.deleted) != 1 || kbSvc.deleted[0] != "kb-1" {
		t.Fatalf("temp KB must be cleaned on ingest failure, deleted=%v", kbSvc.deleted)
	}
}
