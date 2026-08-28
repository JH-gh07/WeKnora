package service

// Task008 GAP-1 (F7A) + F1A service-level fail-fast evidence.
// Written under Decision 008-4 evidence-gap protocol: the audit found no test
// proving "invalid config -> zero side effects" at the Evaluation() entry and
// no test proving "run create failure -> no temporary KB". These tests pin the
// frozen Control Fact contract (fail closed before any side effect).

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type failFastDatasetService struct {
	interfaces.DatasetService
	err error
}

func (f *failFastDatasetService) GetDatasetByID(_ context.Context, _ string) ([]*types.QAPair, error) {
	return nil, f.err
}

type failFastModelService struct {
	interfaces.ModelService
	models []*types.Model
}

func (f *failFastModelService) ListModels(_ context.Context) ([]*types.Model, error) {
	return f.models, nil
}

type failFastKBService struct {
	interfaces.KnowledgeBaseService
	kb          *types.KnowledgeBase
	getErr      error
	createCalls int
}

func (f *failFastKBService) GetKnowledgeBaseByID(_ context.Context, _ string) (*types.KnowledgeBase, error) {
	return f.kb, f.getErr
}

func (f *failFastKBService) CreateKnowledgeBase(_ context.Context, _ *types.KnowledgeBase) (*types.KnowledgeBase, error) {
	f.createCalls++
	return &types.KnowledgeBase{ID: "kb-created"}, nil
}

type failFastRunRepo struct {
	interfaces.EvaluationRunRepository
	createCalls int
	createErr   error
	updated     []*types.EvaluationRun
}

func (f *failFastRunRepo) Create(_ context.Context, _ *types.EvaluationRun) error {
	f.createCalls++
	return f.createErr
}

func (f *failFastRunRepo) Update(_ context.Context, run *types.EvaluationRun) error {
	f.updated = append(f.updated, run)
	return nil
}

func newFailFastService(ds interfaces.DatasetService, ms interfaces.ModelService,
	kbs interfaces.KnowledgeBaseService, rr interfaces.EvaluationRunRepository) *EvaluationService {
	svc := NewEvaluationService(
		&config.Config{Conversation: &config.ConversationConfig{
			Summary: &config.SummaryConfig{},
		}}, ds, kbs, nil, nil, ms,
		&fakeReconcileTenantService{tenant: &types.Tenant{ID: 1}},
		nil, rr, EvaluationBuildInfo{},
	)
	return svc.(*EvaluationService)
}

func mustModel(id string, t types.ModelType) *types.Model {
	return &types.Model{ID: id, Type: t}
}

// TestEvaluationInvalidConfigFailsBeforeAnySideEffect pins F7A: every invalid
// input is rejected before the run row and the temporary KB exist.
func TestEvaluationInvalidConfigFailsBeforeAnySideEffect(t *testing.T) {
	happyModels := []*types.Model{
		mustModel("emb-1", types.ModelTypeEmbedding),
		mustModel("llm-1", types.ModelTypeKnowledgeQA),
		mustModel("rerank-1", types.ModelTypeRerank),
	}

	cases := []struct {
		name    string
		ds      interfaces.DatasetService
		ms      interfaces.ModelService
		kbs     interfaces.KnowledgeBaseService
		kbID    string
		wantErr bool
	}{
		{
			name:    "dataset not found",
			ds:      &failFastDatasetService{err: errors.New("dataset not found")},
			ms:      &failFastModelService{models: happyModels},
			kbs:     &failFastKBService{},
			wantErr: true,
		},
		{
			name:    "knowledge base not found",
			ds:      &failFastDatasetService{},
			ms:      &failFastModelService{models: happyModels},
			kbs:     &failFastKBService{getErr: errors.New("kb not found")},
			kbID:    "kb-missing",
			wantErr: true,
		},
		{
			name:    "no default models",
			ds:      &failFastDatasetService{},
			ms:      &failFastModelService{},
			kbs:     &failFastKBService{},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runRepo := &failFastRunRepo{}
			kbs := tc.kbs.(*failFastKBService)
			svc := newFailFastService(tc.ds, tc.ms, kbs, runRepo)
			ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
			_, err := svc.Evaluation(ctx, "default", tc.kbID, "", "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got err=%v", tc.wantErr, err)
			}
			if runRepo.createCalls != 0 {
				t.Fatalf("provider/run side effect: runRepository.Create called %d times before validation", runRepo.createCalls)
			}
			if kbs.createCalls != 0 {
				t.Fatalf("temporary resource side effect: CreateKnowledgeBase called %d times before validation", kbs.createCalls)
			}
		})
	}
}

// TestEvaluationRunCreateFailureNoTemporaryKB pins F1A: when the run row
// cannot be persisted, the temporary KB is never created and the error
// propagates (fail closed, no orphan side effects).
func TestEvaluationRunCreateFailureNoTemporaryKB(t *testing.T) {
	models := []*types.Model{
		mustModel("emb-1", types.ModelTypeEmbedding),
		mustModel("llm-1", types.ModelTypeKnowledgeQA),
		mustModel("rerank-1", types.ModelTypeRerank),
	}
	runRepo := &failFastRunRepo{createErr: errors.New("injected create failure")}
	kbs := &failFastKBService{}
	svc := newFailFastService(&failFastDatasetService{}, &failFastModelService{models: models}, kbs, runRepo)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))

	_, err := svc.Evaluation(ctx, "default", "", "", "")
	if err == nil {
		t.Fatal("expected create failure to propagate")
	}
	if runRepo.createCalls != 1 {
		t.Fatalf("expected exactly one Create attempt, got %d", runRepo.createCalls)
	}
	if kbs.createCalls != 0 {
		t.Fatalf("temporary KB created despite run persistence failure: %d calls", kbs.createCalls)
	}
}
