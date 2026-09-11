package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"

	"github.com/Tencent/WeKnora/internal/application/service/metric"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// MetricList stores and aggregates metric results
type MetricList struct {
	results []*types.MetricResult
}

// metricCalculators defines all metrics to be calculated
var metricCalculators = []struct {
	calc     interfaces.Metrics                 // Metric calculator implementation
	getField func(*types.MetricResult) *float64 // Field accessor for result
}{
	// Retrieval Metrics
	{metric.NewPrecisionMetric(), func(r *types.MetricResult) *float64 { return &r.RetrievalMetrics.Precision }},
	{metric.NewRecallMetric(), func(r *types.MetricResult) *float64 { return &r.RetrievalMetrics.Recall }},
	{metric.NewNDCGMetric(3), func(r *types.MetricResult) *float64 { return &r.RetrievalMetrics.NDCG3 }},
	{metric.NewNDCGMetric(10), func(r *types.MetricResult) *float64 { return &r.RetrievalMetrics.NDCG10 }},
	{metric.NewMRRMetric(), func(r *types.MetricResult) *float64 { return &r.RetrievalMetrics.MRR }},
	{metric.NewMAPMetric(), func(r *types.MetricResult) *float64 { return &r.RetrievalMetrics.MAP }},

	// Generation Metrics
	{metric.NewBLEUMetric(true, metric.BLEU1Gram), func(r *types.MetricResult) *float64 {
		return &r.GenerationMetrics.BLEU1
	}},
	{metric.NewBLEUMetric(true, metric.BLEU2Gram), func(r *types.MetricResult) *float64 {
		return &r.GenerationMetrics.BLEU2
	}},
	{metric.NewBLEUMetric(true, metric.BLEU4Gram), func(r *types.MetricResult) *float64 {
		return &r.GenerationMetrics.BLEU4
	}},
	{metric.NewRougeMetric(true, "rouge-1", "f"), func(r *types.MetricResult) *float64 {
		return &r.GenerationMetrics.ROUGE1
	}},
	{metric.NewRougeMetric(true, "rouge-2", "f"), func(r *types.MetricResult) *float64 {
		return &r.GenerationMetrics.ROUGE2
	}},
	{metric.NewRougeMetric(true, "rouge-l", "f"), func(r *types.MetricResult) *float64 {
		return &r.GenerationMetrics.ROUGEL
	}},
}

// Append calculates and stores metrics for given input
func (m *MetricList) Append(metricInput *types.MetricInput) *types.MetricResult {
	result := &types.MetricResult{}
	// Calculate all configured metrics
	for _, c := range metricCalculators {
		score := c.calc.Compute(metricInput)
		*c.getField(result) = score
	}
	logger.Infof(context.Background(), "metric: %v", result)
	m.results = append(m.results, result)
	return result
}

// Avg calculates average of all stored metric results
func (m *MetricList) Avg() *types.MetricResult {
	if len(m.results) == 0 {
		return &types.MetricResult{}
	}

	avgResult := &types.MetricResult{}
	count := float64(len(m.results))

	// Calculate average for each metric
	for _, config := range metricCalculators {
		sum := 0.0
		for _, r := range m.results {
			sum += *config.getField(r)
		}
		*config.getField(avgResult) = sum / count
	}
	return avgResult
}

// HookMetric tracks evaluation metrics for QA pairs
type HookMetric struct {
	qaPairMetricList []*qaPairMetric // Per-QA pair metrics
	metricResults    *MetricList     // Aggregated results
	mu               *sync.RWMutex   // Thread safety

	// lineageUnavailable counts retrieved chunks that could not be mapped to a
	// passage because they carry no stable lineage identity (LINEAGE_UNAVAILABLE,
	// Task016 Step 3). These are never guessed into a passage ID.
	lineageUnavailable int

	// standardSums/standardCount accumulate the standard measurement-contract/v1
	// retrieval metrics per query (Task016 Step 4). They are averaged over the
	// number of finished queries to yield MAP/MRR/nDCG/Precision@k/Recall@k.
	standardSums  types.StandardRetrievalMetrics
	standardCount int
}

// qaPairMetric stores metrics for a single QA pair
type qaPairMetric struct {
	qaPair       *types.QAPair
	searchResult []*types.SearchResult
	rerankResult []*types.SearchResult
	chatResponse *types.ChatResponse
}

// NewHookMetric creates a new HookMetric with given capacity
func NewHookMetric(capacity int) *HookMetric {
	return &HookMetric{
		metricResults:    &MetricList{},
		qaPairMetricList: make([]*qaPairMetric, capacity),
		mu:               &sync.RWMutex{},
	}
}

// recordInit initializes metric tracking for a QA pair
func (h *HookMetric) recordInit(index int) {
	h.qaPairMetricList[index] = &qaPairMetric{}
}

// recordQaPair records the QA pair data
func (h *HookMetric) recordQaPair(index int, qaPair *types.QAPair) {
	h.qaPairMetricList[index].qaPair = qaPair
}

// recordSearchResult records search results
func (h *HookMetric) recordSearchResult(index int, searchResult []*types.SearchResult) {
	h.qaPairMetricList[index].searchResult = searchResult
}

// recordRerankResult records reranked results
func (h *HookMetric) recordRerankResult(index int, rerankResult []*types.SearchResult) {
	h.qaPairMetricList[index].rerankResult = rerankResult
}

// recordChatResponse records the generated chat response
func (h *HookMetric) recordChatResponse(index int, chatResponse *types.ChatResponse) {
	h.qaPairMetricList[index].chatResponse = chatResponse
}

// recordFinish finalizes metrics for a QA pair
func (h *HookMetric) recordFinish(index int) evaluationItemResult {
	// Prepare retrieval source: prefer rerank results, fall back to search results
	retrievalSource := h.qaPairMetricList[index].rerankResult
	if len(retrievalSource) == 0 {
		retrievalSource = h.qaPairMetricList[index].searchResult
	}

	// Map retrieved chunks back to original passage IDs via the STABLE lineage
	// identity carried on the result (SourcePassageID), never via text content
	// matching (Task016 Step 3, I03). A result without lineage is
	// LINEAGE_UNAVAILABLE and is counted but never guessed into a passage ID.
	qaPair := h.qaPairMetricList[index].qaPair
	retrievalIDs, lineageUnavailable := mapRetrievalToPassageIDs(retrievalSource)

	// Get generated text if available
	generatedTexts := ""
	if h.qaPairMetricList[index].chatResponse != nil {
		generatedTexts = h.qaPairMetricList[index].chatResponse.Content
	}

	// Prepare metric input data
	metricInput := &types.MetricInput{
		RetrievalGT:    [][]int{qaPair.PIDs},
		RetrievalIDs:   retrievalIDs,
		GeneratedTexts: generatedTexts,
		GeneratedGT:    qaPair.Answer,
	}

	// Thread-safe append of metrics
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lineageUnavailable += lineageUnavailable
	legacy := h.metricResults.Append(metricInput)
	standard := standardMetricsForItem(qaPair.PIDs, retrievalIDs)
	h.standardSums.PrecisionAt10 += standard.PrecisionAt10
	h.standardSums.RecallAt10 += standard.RecallAt10
	h.standardSums.MRR += standard.MRR
	h.standardSums.AP += standard.AP
	h.standardSums.NDCGAt3 += standard.NDCGAt3
	h.standardSums.NDCGAt10 += standard.NDCGAt10
	h.standardCount++
	return evaluationItemResult{
		QID: qaPair.QID, RelevantPassageIDs: append([]int(nil), qaPair.PIDs...),
		RetrievedPassageIDs: append([]int(nil), retrievalIDs...),
		LineageUnavailable:  lineageUnavailable, LegacyMetrics: *legacy,
		StandardRetrieval: standard, MeasurementContractHash: metric.MeasurementContractHash(),
	}
}

// accumulateStandard folds the per-query standard metrics into the running sums.
// The caller must hold h.mu. The standard metrics are the measurement-contract/v1
// definitions (Precision@k/Recall@k/MRR/AP/MAP/nDCG@k) — NOT the legacy averages.
func (h *HookMetric) accumulateStandard(relevant []int, retrieved []int) {
	standard := standardMetricsForItem(relevant, retrieved)
	h.standardSums.PrecisionAt10 += standard.PrecisionAt10
	h.standardSums.RecallAt10 += standard.RecallAt10
	h.standardSums.MRR += standard.MRR
	h.standardSums.AP += standard.AP
	h.standardSums.NDCGAt3 += standard.NDCGAt3
	h.standardSums.NDCGAt10 += standard.NDCGAt10
	h.standardCount++
}

func standardMetricsForItem(relevant, retrieved []int) types.StandardRetrievalMetrics {
	ap := metric.AveragePrecision(retrieved, relevant)
	return types.StandardRetrievalMetrics{
		PrecisionAt10: metric.PrecisionAtK(retrieved, relevant, 10),
		RecallAt10:    metric.RecallAtK(retrieved, relevant, 10),
		MRR:           metric.ReciprocalRankAtK(retrieved, relevant, 10),
		AP:            ap, MAP: ap,
		NDCGAt3:  metric.NDCGAtK(retrieved, relevant, 3),
		NDCGAt10: metric.NDCGAtK(retrieved, relevant, 10),
	}
}

// evaluationItemResult is the prompt-free, independently recomputable result
// committed to one item fact. Retrieval metrics can be recomputed from the ID
// lists; generation text is never persisted.
type evaluationItemResult struct {
	QID                     int                            `json:"qid"`
	RelevantPassageIDs      []int                          `json:"relevant_passage_ids"`
	RetrievedPassageIDs     []int                          `json:"retrieved_passage_ids"`
	LineageUnavailable      int                            `json:"lineage_unavailable"`
	LegacyMetrics           types.MetricResult             `json:"legacy_metrics"`
	StandardRetrieval       types.StandardRetrievalMetrics `json:"standard_retrieval_metrics"`
	MeasurementContractHash string                         `json:"measurement_contract_hash"`
}

// mapRetrievalToPassageIDs maps retrieved chunks back to their dataset passage
// IDs using ONLY the stable lineage identity (SourcePassageID). It performs no
// text content matching (I03). A retrieved result with empty/unparseable
// SourcePassageID is LINEAGE_UNAVAILABLE: counted in the returned unavailable
// total but never guessed into a passage ID.
func mapRetrievalToPassageIDs(retrievalSource []*types.SearchResult) (retrievalIDs []int, lineageUnavailable int) {
	seen := make(map[int]struct{})
	for _, r := range retrievalSource {
		if r == nil || r.Content == "" {
			continue
		}
		if r.SourcePassageID == "" {
			lineageUnavailable++
			continue
		}
		pid, err := strconv.Atoi(r.SourcePassageID)
		if err != nil {
			lineageUnavailable++
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		retrievalIDs = append(retrievalIDs, pid)
	}
	return retrievalIDs, lineageUnavailable
}

// MetricResult returns the averaged metric results
func (h *HookMetric) MetricResult() *types.MetricResult {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.metricResults.Avg()
}

// LineageUnavailable returns the number of retrieved chunks that could not be
// mapped to a passage because they carry no stable lineage identity. It is the
// LINEAGE_UNAVAILABLE audit signal for Task016 Step 3 (plan W1).
func (h *HookMetric) LineageUnavailable() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lineageUnavailable
}

// StandardMetricResult returns the averaged standard measurement-contract/v1
// retrieval metrics (Task016 Step 4). MAP is derived from the summed AP here;
// the stored AP field is the per-query mean (equivalent to MAP over a single
// query list, but kept as its own field for transparency).
func (h *HookMetric) StandardMetricResult() types.StandardRetrievalMetrics {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.standardCount == 0 {
		return types.StandardRetrievalMetrics{}
	}
	n := float64(h.standardCount)
	return types.StandardRetrievalMetrics{
		PrecisionAt10: h.standardSums.PrecisionAt10 / n,
		RecallAt10:    h.standardSums.RecallAt10 / n,
		MRR:           h.standardSums.MRR / n,
		AP:            h.standardSums.AP / n,
		MAP:           h.standardSums.AP / n,
		NDCGAt3:       h.standardSums.NDCGAt3 / n,
		NDCGAt10:      h.standardSums.NDCGAt10 / n,
	}
}

// metricPersistPayload is the JSON shape persisted in EvaluationRun.MetricsJSON
// (Task016 Step 4). It embeds the legacy MetricResult (whose legacy field names
// are already prefixed legacy_nonstandard_) plus the standard
// measurement-contract/v1 retrieval metrics and the measurement contract hash
// under which they were computed.
type metricPersistPayload struct {
	*types.MetricResult
	StandardRetrieval       types.StandardRetrievalMetrics `json:"standard_retrieval_metrics"`
	MeasurementContractHash string                         `json:"measurement_contract_hash"`
	ItemAggregateHash       string                         `json:"item_aggregate_hash,omitempty"`
}

// MetricsJSONPayload returns the complete MetricsJSON for the current hook state:
// legacy averaged metrics + standard measurement-contract/v1 metrics + contract
// hash. The contract hash is attached so a reader can always tell which
// measurement contract produced the standard numbers (I04, AC08).
func (h *HookMetric) MetricsJSONPayload() types.JSON {
	standard := h.StandardMetricResult()
	payload := metricPersistPayload{
		MetricResult:            h.MetricResult(),
		StandardRetrieval:       standard,
		MeasurementContractHash: metric.MeasurementContractHash(),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return types.JSON("")
	}
	return types.JSON(b)
}

// AggregateMetricsFromItemFacts rebuilds the run metrics from committed item
// facts. Failed/cancelled items remain in the denominator as zero-valued
// observations (I15); no in-memory accumulator is authoritative.
func AggregateMetricsFromItemFacts(items []*types.EvaluationRunItem) (types.JSON, error) {
	legacy := &MetricList{results: make([]*types.MetricResult, 0, len(items))}
	var standardSums types.StandardRetrievalMetrics
	contractHash := metric.MeasurementContractHash()
	for _, item := range items {
		if item == nil || !item.Status.IsTerminal() {
			return nil, fmt.Errorf("item ledger contains a non-terminal fact")
		}
		if item.Status != types.EvaluationItemStatusSucceeded {
			legacy.results = append(legacy.results, &types.MetricResult{})
			continue
		}
		var result evaluationItemResult
		if err := json.Unmarshal(item.ResultJSON, &result); err != nil {
			return nil, fmt.Errorf("item %s result: %w", item.ItemID, err)
		}
		if result.MeasurementContractHash != contractHash {
			return nil, fmt.Errorf("item %s measurement contract mismatch", item.ItemID)
		}
		recomputed := standardMetricsForItem(result.RelevantPassageIDs, result.RetrievedPassageIDs)
		if !sameStandardMetrics(recomputed, result.StandardRetrieval) {
			return nil, fmt.Errorf("item %s standard metric mismatch", item.ItemID)
		}
		legacyCopy := result.LegacyMetrics
		legacy.results = append(legacy.results, &legacyCopy)
		standardSums.PrecisionAt10 += recomputed.PrecisionAt10
		standardSums.RecallAt10 += recomputed.RecallAt10
		standardSums.MRR += recomputed.MRR
		standardSums.AP += recomputed.AP
		standardSums.NDCGAt3 += recomputed.NDCGAt3
		standardSums.NDCGAt10 += recomputed.NDCGAt10
	}
	n := float64(len(items))
	standard := types.StandardRetrievalMetrics{}
	if n > 0 {
		standard = types.StandardRetrievalMetrics{
			PrecisionAt10: standardSums.PrecisionAt10 / n,
			RecallAt10:    standardSums.RecallAt10 / n,
			MRR:           standardSums.MRR / n,
			AP:            standardSums.AP / n,
			MAP:           standardSums.AP / n,
			NDCGAt3:       standardSums.NDCGAt3 / n,
			NDCGAt10:      standardSums.NDCGAt10 / n,
		}
	}
	itemAggregate, err := AggregateRunItems(items, true)
	if err != nil {
		return nil, err
	}
	itemAggregateHash, err := itemAggregate.CanonicalHash(items)
	if err != nil {
		return nil, err
	}
	payload := metricPersistPayload{MetricResult: legacy.Avg(), StandardRetrieval: standard, MeasurementContractHash: contractHash, ItemAggregateHash: itemAggregateHash}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return types.JSON(raw), nil
}

func sameStandardMetrics(a, b types.StandardRetrievalMetrics) bool {
	const epsilon = 1e-12
	return math.Abs(a.PrecisionAt10-b.PrecisionAt10) <= epsilon &&
		math.Abs(a.RecallAt10-b.RecallAt10) <= epsilon &&
		math.Abs(a.MRR-b.MRR) <= epsilon && math.Abs(a.AP-b.AP) <= epsilon &&
		math.Abs(a.MAP-b.MAP) <= epsilon && math.Abs(a.NDCGAt3-b.NDCGAt3) <= epsilon &&
		math.Abs(a.NDCGAt10-b.NDCGAt10) <= epsilon
}
