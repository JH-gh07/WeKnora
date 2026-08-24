package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

/*
corpus: pid -> content
queries: qid -> content
answers: aid -> content
qrels: qid -> pid
arels: qid -> aid
*/

// EvaluationService handles evaluation tasks for knowledge base and chat models.
type EvaluationService struct {
	config               *config.Config                  // Application configuration
	dataset              interfaces.DatasetService       // Service for dataset operations
	knowledgeBaseService interfaces.KnowledgeBaseService // Service for knowledge base operations
	knowledgeService     interfaces.KnowledgeService     // Service for knowledge operations
	sessionService       interfaces.SessionService       // Service for chat sessions
	modelService         interfaces.ModelService         // Service for model operations
	tenantService        interfaces.TenantService        // Service for tenant lookup (reconciliation)
	temporaryKBFinder    interfaces.TemporaryKnowledgeBaseFinder
	runRepository        interfaces.EvaluationRunRepository
	buildInfo            EvaluationBuildInfo

	evaluationMemoryStorage *evaluationMemoryStorage // In-process params cache (NOT the source of truth)
}

func NewEvaluationService(
	config *config.Config,
	dataset interfaces.DatasetService,
	knowledgeBaseService interfaces.KnowledgeBaseService,
	knowledgeService interfaces.KnowledgeService,
	sessionService interfaces.SessionService,
	modelService interfaces.ModelService,
	tenantService interfaces.TenantService,
	temporaryKBFinder interfaces.TemporaryKnowledgeBaseFinder,
	runRepository interfaces.EvaluationRunRepository,
	buildInfo EvaluationBuildInfo,
) interfaces.EvaluationService {
	evaluationMemoryStorage := newEvaluationMemoryStorage()
	return &EvaluationService{
		config:                  config,
		dataset:                 dataset,
		knowledgeBaseService:    knowledgeBaseService,
		knowledgeService:        knowledgeService,
		sessionService:          sessionService,
		modelService:            modelService,
		tenantService:           tenantService,
		temporaryKBFinder:       temporaryKBFinder,
		runRepository:           runRepository,
		buildInfo:               buildInfo,
		evaluationMemoryStorage: evaluationMemoryStorage,
	}
}

// evaluationMemoryStorage caches the full in-process EvaluationDetail so the
// legacy GET path can still return Params during the current process. It is
// NOT the durable source of truth — restart-safe queries read the repository.
type evaluationMemoryStorage struct {
	store map[string]*types.EvaluationDetail // Map of taskID to evaluation details
	mu    *sync.RWMutex                      // Read-write lock for concurrent access
}

func newEvaluationMemoryStorage() *evaluationMemoryStorage {
	res := &evaluationMemoryStorage{
		store: make(map[string]*types.EvaluationDetail),
		mu:    &sync.RWMutex{},
	}
	return res
}

func (e *evaluationMemoryStorage) register(params *types.EvaluationDetail) {
	e.mu.Lock()
	defer e.mu.Unlock()
	logger.Infof(context.Background(), "Registering evaluation task: %s", params.Task.ID)
	e.store[params.Task.ID] = params
}

func (e *evaluationMemoryStorage) get(taskID string) (*types.EvaluationDetail, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	res, ok := e.store[taskID]
	return res, ok
}

// mapRunStatusToLegacy maps the durable run status to the legacy
// EvaluationStatue enum. INTERRUPTED has no legacy equivalent and is surfaced
// as Failed with an explanatory ErrMsg; the authoritative INTERRUPTED state is
// read from the persisted EvaluationRun row.
func mapRunStatusToLegacy(status types.EvaluationRunStatus) types.EvaluationStatue {
	switch status {
	case types.EvaluationRunStatusPending:
		return types.EvaluationStatuePending
	case types.EvaluationRunStatusRunning:
		return types.EvaluationStatueRunning
	case types.EvaluationRunStatusCompleted:
		return types.EvaluationStatueSuccess
	default:
		return types.EvaluationStatueFailed
	}
}

// mapRunToDetail converts a persisted EvaluationRun into the legacy
// EvaluationDetail response shape. Params is not reconstructed from the
// secret-free snapshot (prompt text is deliberately not persisted); callers
// may enrich it from the in-process cache.
func mapRunToDetail(run *types.EvaluationRun) *types.EvaluationDetail {
	detail := &types.EvaluationDetail{
		Task: &types.EvaluationTask{
			ID:       run.TaskID,
			TenantID: run.TenantID,
			Status:   mapRunStatusToLegacy(run.Status),
		},
	}
	if run.StartedAt != nil {
		detail.Task.StartTime = *run.StartedAt
	}
	detail.Task.Total = run.TotalCount
	detail.Task.Finished = run.FinishedCount

	if run.Status == types.EvaluationRunStatusInterrupted {
		reason := run.InterruptionReason
		if reason == "" {
			reason = "unknown"
		}
		detail.Task.ErrMsg = fmt.Sprintf("interrupted (%s)", reason)
	} else if run.Status == types.EvaluationRunStatusFailed {
		detail.Task.ErrMsg = run.ErrorMessage
	}

	// Extract dataset_id from the protocol snapshot when present.
	if len(run.ProtocolSnapshot) > 0 {
		var proto struct {
			DatasetID string `json:"dataset_id"`
		}
		if err := json.Unmarshal(run.ProtocolSnapshot, &proto); err == nil {
			detail.Task.DatasetID = proto.DatasetID
		}
	}

	if run.MetricsValid && len(run.MetricsJSON) > 0 {
		var metric types.MetricResult
		if err := json.Unmarshal(run.MetricsJSON, &metric); err == nil {
			detail.Metric = &metric
		}
	}
	return detail
}

func (e *EvaluationService) EvaluationResult(ctx context.Context, taskID string) (*types.EvaluationDetail, error) {
	logger.Info(ctx, "Start getting evaluation result")
	logger.Infof(ctx, "Task ID: %s", taskID)

	tenantID := types.MustTenantIDFromContext(ctx)
	run, err := e.runRepository.GetByTaskID(ctx, tenantID, taskID)
	if err != nil {
		if errors.Is(err, repository.ErrEvaluationRunNotFound) {
			logger.Errorf(ctx, "Failed to get evaluation run: %v", err)
			return nil, errors.New("task not found")
		}
		logger.Errorf(ctx, "Failed to query evaluation run: %v", err)
		return nil, err
	}

	detail := mapRunToDetail(run)

	// Enrich Params from the in-process cache when available (prompt text is
	// never persisted; this only works for the current process).
	if cached, ok := e.evaluationMemoryStorage.get(taskID); ok && cached.Params != nil {
		detail.Params = cached.Params
	}

	logger.Info(ctx, "Evaluation result retrieved successfully")
	return detail, nil
}

// Evaluation starts a new evaluation task with given parameters
// datasetID: ID of the dataset to evaluate against
// knowledgeBaseID: ID of the knowledge base to use (empty to create new)
// chatModelID: ID of the chat model to evaluate
// rerankModelID: ID of the rerank model to evaluate
func (e *EvaluationService) Evaluation(ctx context.Context,
	datasetID string, knowledgeBaseID string, chatModelID string, rerankModelID string,
) (*types.EvaluationDetail, error) {
	logger.Info(ctx, "Start evaluation")
	logger.Infof(ctx, "Dataset ID: %s, Knowledge Base ID: %s, Chat Model ID: %s, Rerank Model ID: %s",
		datasetID, knowledgeBaseID, chatModelID, rerankModelID)

	// Get tenant ID from context for multi-tenancy support
	tenantID := types.MustTenantIDFromContext(ctx)
	logger.Infof(ctx, "Tenant ID: %d", tenantID)

	// Resolve models once.
	models, err := e.modelService.ListModels(ctx)
	if err != nil {
		logger.Errorf(ctx, "Failed to list models: %v", err)
		return nil, err
	}

	// Resolve embedding + summary model for the temporary KB.
	var embeddingModelID, llmModelID string
	if knowledgeBaseID == "" {
		for _, model := range models {
			if model == nil {
				continue
			}
			if model.Type == types.ModelTypeEmbedding && embeddingModelID == "" {
				embeddingModelID = model.ID
			}
			if model.Type == types.ModelTypeKnowledgeQA && llmModelID == "" {
				llmModelID = model.ID
			}
		}
		if embeddingModelID == "" || llmModelID == "" {
			return nil, fmt.Errorf("no default models found for evaluation")
		}
	} else {
		kb, err := e.knowledgeBaseService.GetKnowledgeBaseByID(ctx, knowledgeBaseID)
		if err != nil {
			logger.Errorf(ctx, "Failed to get knowledge base: %v", err)
			return nil, err
		}
		embeddingModelID = kb.EmbeddingModelID
		llmModelID = kb.SummaryModelID
	}

	// Resolve default chat + rerank models when not provided.
	if chatModelID == "" {
		for _, model := range models {
			if model == nil {
				continue
			}
			if model.Type == types.ModelTypeKnowledgeQA {
				chatModelID = model.ID
				break
			}
		}
		if chatModelID == "" {
			return nil, fmt.Errorf("no default chat model found")
		}
	}
	if rerankModelID == "" {
		for _, model := range models {
			if model == nil {
				continue
			}
			if model.Type == types.ModelTypeRerank {
				rerankModelID = model.ID
				break
			}
		}
		if rerankModelID == "" {
			logger.Warnf(ctx, "No rerank model found, skipping rerank")
		}
	}

	// Normalize dataset ID and load dataset to derive a stable content hash.
	if datasetID == "" {
		datasetID = "default"
	}
	dataset, err := e.dataset.GetDatasetByID(ctx, datasetID)
	if err != nil {
		logger.Errorf(ctx, "Failed to get dataset: %v", err)
		return nil, err
	}
	contentHash := datasetContentHash(dataset)

	// Prepare the immutable pipeline params (same shape as before).
	detail := e.buildEvaluationDetail(tenantID, datasetID, chatModelID, rerankModelID)
	params := detail.Params

	// Allocate the durable run identity BEFORE any temporary resource side
	// effect, and derive a deterministic resource locator from it.
	taskID := detail.Task.ID
	runID := uuid.NewString()
	resourceKey := runID

	// Build protocol + provenance snapshots.
	protocolJSON, protocolHash, err := buildProtocolSnapshot(protocolSnapshotInput{
		DatasetID:             datasetID,
		DatasetContentHash:    contentHash,
		EmbeddingModelID:      embeddingModelID,
		ChatModelID:           chatModelID,
		RerankModelID:         rerankModelID,
		SourceKnowledgeBaseID: knowledgeBaseID,
		Params:                params,
	})
	if err != nil {
		logger.Errorf(ctx, "Failed to build protocol snapshot: %v", err)
		return nil, err
	}
	provenanceJSON, err := buildRunProvenance(runProvenance{
		SchemaVersion: evaluationProtocolSchemaVersion,
		GitCommit:     e.buildInfo.GitCommit,
		AppVersion:    e.buildInfo.AppVersion,
		GoVersion:     runtime.Version(),
		BuildTime:     e.buildInfo.BuildTime,
		DBDriver:      os.Getenv("DB_DRIVER"),
		StartedAt:     time.Now(),
		Models:        modelRevisions(models, embeddingModelID, chatModelID, rerankModelID),
	})
	if err != nil {
		logger.Errorf(ctx, "Failed to build provenance: %v", err)
		return nil, err
	}

	now := time.Now()
	run := &types.EvaluationRun{
		RunID:                runID,
		TaskID:               taskID,
		TenantID:             tenantID,
		ProtocolHash:         protocolHash,
		ProtocolSnapshot:     protocolJSON,
		RunProvenance:        provenanceJSON,
		GitCommit:            e.buildInfo.GitCommit,
		AppVersion:           e.buildInfo.AppVersion,
		Status:               types.EvaluationRunStatusPending,
		StartedAt:            &now,
		CleanupStatus:        types.CleanupStatusCreating,
		TemporaryResourceKey: resourceKey,
		MeasurementStatus:    types.MeasurementStatusUnknown,
		PersistenceStatus:    types.PersistenceStatusPersisted,
	}
	if err := e.runRepository.Create(ctx, run); err != nil {
		logger.Errorf(ctx, "Failed to persist evaluation run: %v", err)
		return nil, err
	}

	// Create the temporary KB with a deterministic name + resource-key locator.
	kb, err := e.knowledgeBaseService.CreateKnowledgeBase(ctx, &types.KnowledgeBase{
		Name:             "evaluation-" + shortResourceKey(resourceKey),
		Description:      resourceKey,
		EmbeddingModelID: embeddingModelID,
		SummaryModelID:   llmModelID,
		IsTemporary:      true,
	})
	if err != nil {
		logger.Errorf(ctx, "Failed to create temporary knowledge base: %v", err)
		e.markRunFailed(ctx, run, "CREATE_KB_FAILED", err)
		return nil, err
	}
	knowledgeBaseID = kb.ID
	logger.Infof(ctx, "Created temporary knowledge base with ID: %s, resource key: %s", knowledgeBaseID, resourceKey)

	// Persist the actual temporary KB ID and CREATED cleanup status. This is a
	// fail-closed write: the durable resource locator is a core Task002 fact, so
	// if it cannot be persisted we must not proceed (a restart would otherwise
	// see CREATING with no KB ID and be unable to locate the KB). We delete the
	// just-created KB and fail the run.
	run.TemporaryKBID = kb.ID
	run.CleanupStatus = types.CleanupStatusCreated
	if err := e.persistRun(ctx, run); err != nil {
		logger.Errorf(ctx, "Failed to persist temporary KB ID: %v", err)
		// Fail-closed: clean up the KB we just created (no knowledge was created
		// yet, so knowledgeID is empty) and fail the run.
		e.cleanupTemporaryResources(ctx, run, "", knowledgeBaseID)
		e.markRunFailed(ctx, run, "PERSISTENCE_FAILED", err)
		return nil, err
	}

	// Cache params for the in-process GET path (prompt text is NOT persisted).
	detail.Task.Status = types.EvaluationStatueRunning
	e.evaluationMemoryStorage.register(detail)

	// Start evaluation in background goroutine.
	logger.Info(ctx, "Starting evaluation in background")
	go func() {
		newCtx := logger.CloneContext(ctx)
		newCtx = types.WithLLMCallScope(newCtx, runID, taskID, runID)
		newCtx = types.WithLLMCallMetadata(newCtx, "evaluation", "")
		logger.Infof(newCtx, "Background evaluation started for task ID: %s", taskID)

		// Transition to RUNNING (durably). This is fail-closed: if we cannot
		// durably record RUNNING, we must not execute the evaluation (the DB
		// would still say PENDING) and must clean up the temporary KB.
		run.Status = types.EvaluationRunStatusRunning
		run.InterruptionReason = ""
		if err := e.persistRun(newCtx, run); err != nil {
			logger.Errorf(newCtx, "Failed to mark run RUNNING: %v", err)
			// Fail-closed: clean up the temporary KB (no knowledge created yet)
			// and fail the run rather than execute it in an unrecorded state.
			e.cleanupTemporaryResources(newCtx, run, "", knowledgeBaseID)
			e.markRunFailed(newCtx, run, "PERSISTENCE_FAILED", err)
			return
		}

		// Execute the actual evaluation. EvalDataset persists progress and the
		// final COMPLETED/FAILED state (metrics + cleanup) itself.
		if err := e.EvalDataset(newCtx, run, detail, knowledgeBaseID); err != nil {
			e.markRunFailed(newCtx, run, "EVALUATION_FAILED", err)
			logger.Errorf(newCtx, "Evaluation task failed, task ID: %s", taskID)
			return
		}

		if run.PersistenceStatus == types.PersistenceStatusPersistFailed {
			logger.Warnf(newCtx, "Evaluation finished but a critical durable write failed; run %s is not fully persisted", taskID)
			return
		}
		logger.Infof(newCtx, "Evaluation task completed successfully, task ID: %s", taskID)
	}()

	logger.Infof(ctx, "Evaluation task created successfully, task ID: %s", taskID)
	return detail, nil
}

// buildEvaluationDetail constructs the legacy EvaluationDetail (params only) so
// the POST response and in-process cache match the previous contract.
func (e *EvaluationService) buildEvaluationDetail(
	tenantID uint64, datasetID, chatModelID, rerankModelID string,
) *types.EvaluationDetail {
	taskID := utils.GenerateTaskID("evaluation", tenantID, datasetID)
	detail := &types.EvaluationDetail{
		Task: &types.EvaluationTask{
			ID:        taskID,
			TenantID:  tenantID,
			DatasetID: datasetID,
			Status:    types.EvaluationStatuePending,
			StartTime: time.Now(),
		},
		Params: &types.ChatManage{
			PipelineRequest: types.PipelineRequest{
				VectorThreshold:  e.config.Conversation.VectorThreshold,
				KeywordThreshold: e.config.Conversation.KeywordThreshold,
				EmbeddingTopK:    e.config.Conversation.EmbeddingTopK,
				MaxRounds:        e.config.Conversation.MaxRounds,
				RerankModelID:    rerankModelID,
				RerankTopK:       e.config.Conversation.RerankTopK,
				RerankThreshold:  e.config.Conversation.RerankThreshold,
				ChatModelID:      chatModelID,
				SummaryConfig: types.SummaryConfig{
					MaxTokens:           e.config.Conversation.Summary.MaxTokens,
					RepeatPenalty:       e.config.Conversation.Summary.RepeatPenalty,
					TopK:                e.config.Conversation.Summary.TopK,
					TopP:                e.config.Conversation.Summary.TopP,
					Prompt:              e.config.Conversation.Summary.Prompt,
					ContextTemplate:     e.config.Conversation.Summary.ContextTemplate,
					FrequencyPenalty:    e.config.Conversation.Summary.FrequencyPenalty,
					PresencePenalty:     e.config.Conversation.Summary.PresencePenalty,
					NoMatchPrefix:       e.config.Conversation.Summary.NoMatchPrefix,
					Temperature:         e.config.Conversation.Summary.Temperature,
					Seed:                e.config.Conversation.Summary.Seed,
					MaxCompletionTokens: e.config.Conversation.Summary.MaxCompletionTokens,
				},
				FallbackResponse:    e.config.Conversation.FallbackResponse,
				RewritePromptSystem: e.config.Conversation.RewritePromptSystem,
				RewritePromptUser:   e.config.Conversation.RewritePromptUser,
			},
		},
	}
	return detail
}

// errorTypeToSafeMessage is an allowlist mapping a stable error category to a
// fixed, human-authored, secret-free message. Unknown categories fall back to
// the category name itself — never an arbitrary error string — so a prompt or
// provider request/response body can never be persisted to the run row.
var errorTypeToSafeMessage = map[string]string{
	"CREATE_KB_FAILED":   "failed to create temporary knowledge base",
	"EVALUATION_FAILED":  "evaluation execution failed",
	"PERSISTENCE_FAILED": "failed to persist evaluation run state",
}

// markRunFailed transitions a run to FAILED and records only a stable error
// category plus a safe generic message. The detailed error is intentionally NOT
// written anywhere persistent: the raw error may embed prompt text, request
// bodies or provider tokens, so the default log carries only run_id + error_type
// + the safe message (run_id on the same row is the correlation id).
func (e *EvaluationService) markRunFailed(
	ctx context.Context, run *types.EvaluationRun, errorType string, err error,
) {
	if err == nil {
		return
	}
	run.Status = types.EvaluationRunStatusFailed
	run.ErrorType = errorType
	run.ErrorMessage = errorTypeToSafeMessage[errorType]
	if run.ErrorMessage == "" {
		run.ErrorMessage = errorType
	}
	run.MetricsValid = false
	ended := time.Now()
	run.EndedAt = &ended
	logger.Errorf(ctx, "Evaluation run %s failed (%s): %s", run.RunID, errorType, run.ErrorMessage)
	if persistErr := e.persistRun(ctx, run); persistErr != nil {
		logger.Errorf(ctx, "Failed to persist failed run: %v", persistErr)
	}
}

// persistRun durably writes the run and keeps persistence_status consistent:
// every successful write resets it to PERSISTED (so a previously-failed write
// cannot keep the run a reconciliation candidate forever), while a failed write
// marks it PERSIST_FAILED in memory (best-effort observable; the DB row keeps
// whatever it last durably stored, so the run remains a candidate until a later
// write succeeds). This is what makes reconciliation converge: a run is only
// ever excluded from the candidate list once a full Update actually lands.
func (e *EvaluationService) persistRun(ctx context.Context, run *types.EvaluationRun) error {
	run.PersistenceStatus = types.PersistenceStatusPersisted
	if err := e.runRepository.Update(ctx, run); err != nil {
		run.PersistenceStatus = types.PersistenceStatusPersistFailed
		return err
	}
	return nil
}

// shortResourceKey returns a short, deterministic suffix for the KB name.
func shortResourceKey(resourceKey string) string {
	if len(resourceKey) > 8 {
		return resourceKey[:8]
	}
	return resourceKey
}

// modelRevisions builds secret-free model identity summaries for provenance.
func modelRevisions(
	models []*types.Model, embeddingModelID, chatModelID, rerankModelID string,
) []modelRevision {
	want := map[string]bool{
		embeddingModelID: true,
		chatModelID:      true,
		rerankModelID:    true,
	}
	var out []modelRevision
	for _, m := range models {
		if m == nil || !want[m.ID] {
			continue
		}
		out = append(out, modelRevision{
			ID:     m.ID,
			Type:   string(m.Type),
			Source: string(m.Source),
			Name:   m.Name,
		})
	}
	// Deterministic order for stable provenance serialization.
	sortModelRevisions(out)
	return out
}

func sortModelRevisions(revs []modelRevision) {
	for i := 0; i < len(revs); i++ {
		for j := i + 1; j < len(revs); j++ {
			if revs[j].ID < revs[i].ID {
				revs[i], revs[j] = revs[j], revs[i]
			}
		}
	}
}

// EvalDataset performs the actual evaluation of a dataset.
// It persists progress, the final metrics and the terminal state through the
// repository, and performs temporary resource cleanup in a deferred step.
func (e *EvaluationService) EvalDataset(
	ctx context.Context, run *types.EvaluationRun, detail *types.EvaluationDetail, knowledgeBaseID string,
) error {
	logger.Info(ctx, "Start evaluating dataset")
	logger.Infof(ctx, "Task ID: %s, Run ID: %s", run.TaskID, run.RunID)

	// Install cleanup FIRST so every exit path (including a knowledge-ingest
	// failure before the defer below could previously be reached) still cleans
	// the temporary KB. knowledgeID is empty until ingestion succeeds, and
	// cleanupTemporaryResources treats an empty knowledge ID as "nothing to
	// delete for knowledge".
	var knowledgeID string
	defer func() {
		e.cleanupTemporaryResources(ctx, run, knowledgeID, knowledgeBaseID)
	}()

	// Retrieve dataset from storage.
	dataset, err := e.dataset.GetDatasetByID(ctx, detail.Task.DatasetID)
	if err != nil {
		logger.Errorf(ctx, "Failed to get dataset: %v", err)
		return err
	}
	logger.Infof(ctx, "Dataset retrieved successfully with %d QA pairs", len(dataset))

	// Persist total QA pairs count.
	run.TotalCount = len(dataset)
	if err := e.persistRun(ctx, run); err != nil {
		logger.Errorf(ctx, "Failed to persist total count: %v", err)
	}

	// Extract and organize passages from dataset.
	passages := getPassageList(dataset)
	logger.Infof(ctx, "Creating knowledge from %d passages", len(passages))

	// Create knowledge base from passages (sync: wait for indexing to complete).
	knowledge, err := e.knowledgeService.CreateKnowledgeFromPassageSync(ctx, knowledgeBaseID, passages, "")
	if err != nil {
		logger.Errorf(ctx, "Failed to create knowledge from passages")
		return err
	}
	knowledgeID = knowledge.ID
	logger.Infof(ctx, "Knowledge created and indexed successfully, ID: %s", knowledge.ID)

	// Initialize parallel evaluation metrics.
	var finished int
	var mu sync.Mutex
	var g errgroup.Group
	metricHook := NewHookMetric(len(dataset))

	// Set worker limit based on available CPUs.
	g.SetLimit(max(runtime.GOMAXPROCS(0)-1, 1))
	logger.Infof(ctx, "Starting evaluation with %d parallel workers", max(runtime.GOMAXPROCS(0)-1, 1))

	// Process each QA pair in parallel.
	for i, qaPair := range dataset {
		qaPair := qaPair
		i := i
		g.Go(func() error {
			logger.Infof(ctx, "Processing QA pair %d, question: %s", i, qaPair.Question)

			// Prepare chat management parameters for this QA pair.
			chatManage := detail.Params.Clone()
			chatManage.Query = qaPair.Question
			chatManage.RewriteQuery = qaPair.Question
			chatManage.KnowledgeBaseIDs = []string{knowledgeBaseID}
			chatManage.SearchTargets = types.SearchTargets{
				&types.SearchTarget{
					Type:            types.SearchTargetTypeKnowledgeBase,
					KnowledgeBaseID: knowledgeBaseID,
				},
			}

			// Execute knowledge QA pipeline. The error is scoped to this
			// closure (fixes the previous shared-`err` data race).
			if err := e.sessionService.KnowledgeQAByEvent(ctx, chatManage, types.Pipline["rag"]); err != nil {
				logger.Errorf(ctx, "Failed to process question %d", i)
				return err
			}

			// Record evaluation metrics.
			metricHook.recordInit(i)
			metricHook.recordQaPair(i, qaPair)
			metricHook.recordSearchResult(i, chatManage.SearchResult)
			metricHook.recordRerankResult(i, chatManage.RerankResult)
			metricHook.recordChatResponse(i, chatManage.ChatResponse)
			metricHook.recordFinish(i)

			// Update and persist best-available progress. The shared run is
			// mutated and the DB write is issued under the same mutex so that
			// concurrent workers never race on run fields (or on the aggregate
			// snapshot). The QA pipeline itself remains fully parallel.
			mu.Lock()
			finished++
			metric := metricHook.MetricResult()
			run.ProcessedCount = finished
			run.FinishedCount = finished
			if b, err := json.Marshal(metric); err == nil {
				run.MetricsJSON = types.JSON(b)
			}
			persistErr := e.persistRun(ctx, run)
			mu.Unlock()
			if persistErr != nil {
				logger.Errorf(ctx, "Failed to persist progress: %v", persistErr)
			}
			return nil
		})
	}

	// Wait for all parallel evaluations to complete.
	if err := g.Wait(); err != nil {
		logger.Errorf(ctx, "Evaluation error")
		return err
	}

	// Finalize: persist the durable metrics + COMPLETED state.
	if err := e.persistCompleted(ctx, run, finished, metricHook.MetricResult()); err != nil {
		// The evaluation itself finished, but its terminal state could not be
		// durably written. Fail-closed: mark FAILED with PERSISTENCE_FAILED so
		// the durable row never pretends to be COMPLETED.
		logger.Errorf(ctx, "Evaluation completed but terminal state could not be persisted: %v", err)
		e.markRunFailed(ctx, run, "PERSISTENCE_FAILED", err)
		return nil
	}
	logger.Infof(ctx, "Dataset evaluation completed successfully, task ID: %s", run.TaskID)
	return nil
}

// persistCompleted records the terminal COMPLETED state with a durable,
// reliable aggregate (metrics_valid=true). It returns an error if the terminal
// write fails so the caller can fail-closed instead of pretending success.
func (e *EvaluationService) persistCompleted(
	ctx context.Context, run *types.EvaluationRun, finished int, metric *types.MetricResult,
) error {
	run.Status = types.EvaluationRunStatusCompleted
	run.ProcessedCount = finished
	run.FinishedCount = finished
	if b, err := json.Marshal(metric); err == nil {
		run.MetricsJSON = types.JSON(b)
	}
	run.MetricsValid = true
	ended := time.Now()
	run.EndedAt = &ended
	if err := e.persistRun(ctx, run); err != nil {
		logger.Errorf(ctx, "Failed to persist completed run: %v", err)
		return err
	}
	return nil
}

// cleanupTemporaryResources deletes the temporary knowledge + KB and records a
// durable cleanup_status. An empty knowledgeID / knowledgeBaseID means that
// resource was never created, so it is skipped. On success the status is
// DELETE_REQUESTED (soft-delete accepted; physical cleanup is async), not DONE.
// On KB delete failure temporary_kb_id is kept so a later reconciliation can
// retry the delete.
func (e *EvaluationService) cleanupTemporaryResources(
	ctx context.Context, run *types.EvaluationRun, knowledgeID, knowledgeBaseID string,
) {
	var anyFailed bool
	var kbDeleteFailed bool
	if knowledgeID != "" {
		logger.Infof(ctx, "Cleaning up resources - deleting knowledge: %s", knowledgeID)
		if err := e.knowledgeService.DeleteKnowledge(ctx, knowledgeID); err != nil {
			logger.Errorf(ctx, "Failed to delete knowledge: %v, knowledge ID: %s", err, knowledgeID)
			anyFailed = true
		}
	}

	if knowledgeBaseID != "" {
		logger.Infof(ctx, "Cleaning up resources - deleting knowledge base: %s", knowledgeBaseID)
		if err := e.knowledgeBaseService.DeleteKnowledgeBase(ctx, knowledgeBaseID); err != nil {
			logger.Errorf(ctx, "Failed to delete knowledge base: %v, knowledge base ID: %s", err, knowledgeBaseID)
			anyFailed = true
			kbDeleteFailed = true
		}
	}

	if kbDeleteFailed {
		// The KB still exists: keep temporary_kb_id so reconciliation can retry.
		run.CleanupStatus = types.CleanupStatusFailed
	} else {
		// KB delete accepted (or no KB existed): clear the locator id.
		run.TemporaryKBID = ""
		if anyFailed {
			run.CleanupStatus = types.CleanupStatusFailed
		} else {
			// Soft-delete accepted; physical cleanup is async.
			run.CleanupStatus = types.CleanupStatusDeleteRequested
		}
	}
	if err := e.persistRun(ctx, run); err != nil {
		logger.Errorf(ctx, "Failed to persist cleanup status: %v", err)
	}
}

// ReconcileInterruptedRuns is the single-worker startup reconciliation. It
// lists the runs needing reconciliation (RUNNING + PENDING/FAILED with
// incomplete cleanup), marks non-terminal ones INTERRUPTED with a non-empty
// reason, preserves best-available progress (metrics_valid=false because the
// in-memory aggregate was lost), and discovers/cleans temporary resources via
// the resource key. It returns the number of runs reconciled.
func (e *EvaluationService) ReconcileInterruptedRuns(ctx context.Context) (int, error) {
	runs, err := e.runRepository.ListReconciliationCandidates(ctx)
	if err != nil {
		return 0, err
	}

	reconciled := 0
	for _, run := range runs {
		if run == nil {
			continue
		}
		logger.Infof(ctx, "[reconcile] candidate run %s (tenant %d, status %s, cleanup %s)",
			run.RunID, run.TenantID, run.Status, run.CleanupStatus)

		// RUNNING (owner died mid-run) and PENDING (owner died before start)
		// never reached a terminal state → mark INTERRUPTED. FAILED runs are
		// already terminal; we only clean up their leftover temporary resource.
		switch run.Status {
		case types.EvaluationRunStatusRunning, types.EvaluationRunStatusPending:
			run.Status = types.EvaluationRunStatusInterrupted
			run.InterruptionReason = types.InterruptionReasonServerRestart
			// Best-available progress is preserved; the aggregate is NOT reliable
			// because the in-process metric accumulator died with the process.
			run.MetricsValid = false
			ended := time.Now()
			run.EndedAt = &ended
			if err := e.persistRun(ctx, run); err != nil {
				logger.Errorf(ctx, "[reconcile] failed to mark run %s interrupted: %v", run.RunID, err)
				continue
			}
		}

		// Discover and clean the temporary resource via its locator.
		e.reconcileTemporaryResource(ctx, run)
		reconciled++
	}
	return reconciled, nil
}

// reconcileTemporaryResource deletes the temporary KB (and knowledge) for an
// interrupted run, discovering it by resource key when the actual KB ID was
// not yet persisted. It must distinguish three finder outcomes: found (delete
// it), definitively absent (mark DONE), and transient failure (do NOT mark DONE,
// keep the run a candidate so the lookup is retried next startup).
func (e *EvaluationService) reconcileTemporaryResource(ctx context.Context, run *types.EvaluationRun) {
	kbID := run.TemporaryKBID
	if kbID == "" && run.TemporaryResourceKey != "" {
		tenantCtx := context.WithValue(ctx, types.TenantIDContextKey, run.TenantID)
		kb, err := e.temporaryKBFinder.GetTemporaryKnowledgeBaseByResourceKey(
			tenantCtx, run.TenantID, run.TemporaryResourceKey,
		)
		switch {
		case err == nil && kb != nil:
			kbID = kb.ID
			run.TemporaryKBID = kb.ID
		case errors.Is(err, repository.ErrKnowledgeBaseNotFound):
			// Definitively absent: fall through to mark DONE below.
		case err != nil:
			// Transient lookup failure (DB timeout, connection reset, ...). Do NOT
			// mark DONE — keep cleanup_status and temporary_kb_id untouched so this
			// run remains a reconciliation candidate and the lookup is retried.
			logger.Errorf(ctx, "[reconcile] failed to look up temporary KB for run %s: %v", run.RunID, err)
			return
		}
	}

	if kbID == "" {
		// Nothing to clean (no persisted id, no discoverable KB). Mark DONE so
		// reconciliation stays idempotent and does not re-list this run forever.
		logger.Infof(ctx, "[reconcile] run %s has no temporary KB to clean", run.RunID)
		run.CleanupStatus = types.CleanupStatusDone
		run.TemporaryKBID = ""
		if err := e.persistRun(ctx, run); err != nil {
			logger.Errorf(ctx, "[reconcile] failed to persist cleanup status: %v", err)
		}
		return
	}

	tenantCtx := context.WithValue(ctx, types.TenantIDContextKey, run.TenantID)
	// DeleteKnowledgeBase dereferences the *Tenant carried in the context to
	// compute EffectiveEngines. The reconciliation context only carries the
	// tenant ID, so load the full tenant here; if it cannot be resolved we
	// leave cleanup_status untouched (this run stays a candidate) rather than
	// panic and crash the whole process during startup reconciliation.
	tenant, tenantErr := e.tenantService.GetTenantByID(ctx, run.TenantID)
	if tenantErr != nil || tenant == nil {
		logger.Errorf(ctx, "[reconcile] failed to load tenant %d for run %s: %v", run.TenantID, run.RunID, tenantErr)
		return
	}
	tenantCtx = context.WithValue(tenantCtx, types.TenantInfoContextKey, tenant)
	if err := e.knowledgeBaseService.DeleteKnowledgeBase(tenantCtx, kbID); err != nil {
		if errors.Is(err, repository.ErrKnowledgeBaseNotFound) {
			// The KB row is already soft-deleted: a previous delete's logical phase
			// completed, but this run's cleanup write was never durably recorded.
			// Treat that as "logical delete already done" so a crash in this exact
			// window cannot leave the run stuck in FAILED forever.
			logger.Infof(ctx, "[reconcile] temporary KB %s already soft-deleted; marking cleanup requested", kbID)
			run.CleanupStatus = types.CleanupStatusDeleteRequested
			run.TemporaryKBID = ""
		} else {
			logger.Errorf(ctx, "[reconcile] failed to delete temporary KB %s: %v", kbID, err)
			// Keep temporary_kb_id on failure so a future reconciliation can retry.
			run.CleanupStatus = types.CleanupStatusFailed
		}
	} else {
		// Soft-delete accepted; physical cleanup is async.
		run.CleanupStatus = types.CleanupStatusDeleteRequested
		run.TemporaryKBID = ""
	}
	if err := e.persistRun(ctx, run); err != nil {
		logger.Errorf(ctx, "[reconcile] failed to persist cleanup status: %v", err)
	}
}

// getPassageList extracts and organizes passages from QA pairs.
func getPassageList(dataset []*types.QAPair) []string {
	pIDMap := make(map[int]string)
	maxPID := 0
	for _, qaPair := range dataset {
		for i := 0; i < len(qaPair.PIDs); i++ {
			pIDMap[qaPair.PIDs[i]] = qaPair.Passages[i]
			maxPID = max(maxPID, qaPair.PIDs[i])
		}
	}
	passages := make([]string, maxPID+1)
	for i := 0; i <= maxPID; i++ {
		if _, ok := pIDMap[i]; ok {
			passages[i] = pIDMap[i]
		}
	}
	return passages
}
