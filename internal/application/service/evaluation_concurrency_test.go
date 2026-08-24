package service

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// The fakes below embed the full interface and override only the methods that
// EvalDataset actually calls. Embedding a nil interface supplies the remaining
// method set at compile time without boilerplate; un-overridden methods would
// panic, but EvalDataset never calls them.

type fakeDatasetService struct {
	interfaces.DatasetService
	pairs []*types.QAPair
}

func (f *fakeDatasetService) GetDatasetByID(_ context.Context, _ string) ([]*types.QAPair, error) {
	return f.pairs, nil
}

type fakeKnowledgeService struct {
	interfaces.KnowledgeService
}

func (f *fakeKnowledgeService) CreateKnowledgeFromPassageSync(
	_ context.Context, _ string, _ []string, _ string,
) (*types.Knowledge, error) {
	return &types.Knowledge{ID: "knowledge-1"}, nil
}

func (f *fakeKnowledgeService) DeleteKnowledge(_ context.Context, _ string) error {
	return nil
}

type fakeKnowledgeBaseService struct {
	interfaces.KnowledgeBaseService
}

func (f *fakeKnowledgeBaseService) DeleteKnowledgeBase(_ context.Context, _ string) error {
	return nil
}

type fakeSessionService struct {
	interfaces.SessionService
}

func (f *fakeSessionService) KnowledgeQAByEvent(
	_ context.Context, cm *types.ChatManage, _ []types.EventType,
) error {
	cm.SearchResult = []*types.SearchResult{{ID: "s1", Content: "passage"}}
	cm.ChatResponse = &types.ChatResponse{Content: "generated"}
	return nil
}

type fakeRunRepository struct {
	interfaces.EvaluationRunRepository
	mu       sync.Mutex
	updates  int
	lastProc int
}

func (f *fakeRunRepository) Update(_ context.Context, run *types.EvaluationRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.lastProc = run.ProcessedCount
	return nil
}

func TestEvalDatasetConcurrentProgressHasNoDataRace(t *testing.T) {
	// A deterministic fixture that exercises the full parallel QA loop under
	// the race detector. It proves the shared-`err` closure and shared-`run`
	// mutation are race-free without needing a live provider.
	const n = 24
	pairs := make([]*types.QAPair, 0, n)
	for i := 0; i < n; i++ {
		pairs = append(pairs, &types.QAPair{
			QID:      i,
			Question: "question " + strconv.Itoa(i),
			PIDs:     []int{i},
			Passages: []string{"passage " + strconv.Itoa(i)},
			Answer:   "answer " + strconv.Itoa(i),
		})
	}

	runRepo := &fakeRunRepository{}
	svc := &EvaluationService{
		dataset:              &fakeDatasetService{pairs: pairs},
		knowledgeService:     &fakeKnowledgeService{},
		knowledgeBaseService: &fakeKnowledgeBaseService{},
		sessionService:       &fakeSessionService{},
		runRepository:        runRepo,
	}

	run := &types.EvaluationRun{
		RunID:  "run-1",
		TaskID: "task-1",
	}
	detail := &types.EvaluationDetail{
		Task:   &types.EvaluationTask{ID: "task-1", DatasetID: "default"},
		Params: &types.ChatManage{PipelineRequest: types.PipelineRequest{}},
	}

	if err := svc.EvalDataset(context.Background(), run, detail, "kb-1"); err != nil {
		t.Fatalf("EvalDataset: %v", err)
	}

	runRepo.mu.Lock()
	defer runRepo.mu.Unlock()
	if runRepo.lastProc != n {
		t.Fatalf("expected processed count %d, got %d", n, runRepo.lastProc)
	}
	if run.Status != types.EvaluationRunStatusCompleted {
		t.Fatalf("expected COMPLETED, got %s", run.Status)
	}
	if !run.MetricsValid {
		t.Fatalf("expected metrics_valid=true on full completion")
	}
}
