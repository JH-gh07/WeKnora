package service

import (
	"encoding/json"
	"reflect"
	"regexp"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service/metric"
	"github.com/Tencent/WeKnora/internal/types"
)

// Task016 Step 3 — lineage identity mapping (plan W1).
// mapRetrievalToPassageIDs must map via SourcePassageID ONLY and treat any
// result without a stable identity as LINEAGE_UNAVAILABLE (counted, never
// guessed). It must never fall back to text content matching.

func TestMapRetrievalToPassageIDsIdentityMapping(t *testing.T) {
	src := []*types.SearchResult{
		{Content: "passage five body", SourcePassageID: "5"},
		{Content: "passage zero body", SourcePassageID: "0"},
	}
	ids, unavailable := mapRetrievalToPassageIDs(src)
	if !reflect.DeepEqual(ids, []int{5, 0}) {
		t.Fatalf("ids = %v, want [5 0]", ids)
	}
	if unavailable != 0 {
		t.Fatalf("unavailable = %d, want 0", unavailable)
	}
}

func TestMapRetrievalToPassageIDsDeduplicatesDuplicateIdentity(t *testing.T) {
	src := []*types.SearchResult{
		{Content: "a", SourcePassageID: "3"},
		{Content: "b", SourcePassageID: "3"},
	}
	ids, unavailable := mapRetrievalToPassageIDs(src)
	if !reflect.DeepEqual(ids, []int{3}) {
		t.Fatalf("ids = %v, want [3]", ids)
	}
	if unavailable != 0 {
		t.Fatalf("unavailable = %d, want 0", unavailable)
	}
}

func TestMapRetrievalToPassageIDsNoLineageIsUnavailableNeverGuessed(t *testing.T) {
	src := []*types.SearchResult{
		{Content: "identical shared text", SourcePassageID: ""},
	}
	ids, unavailable := mapRetrievalToPassageIDs(src)
	if len(ids) != 0 {
		t.Fatalf("ids = %v, want empty (no guessing)", ids)
	}
	if unavailable != 1 {
		t.Fatalf("unavailable = %d, want 1", unavailable)
	}
}

func TestMapRetrievalToPassageIDsUnparseableIdentityIsUnavailable(t *testing.T) {
	src := []*types.SearchResult{
		{Content: "x", SourcePassageID: "not-an-int"},
	}
	ids, unavailable := mapRetrievalToPassageIDs(src)
	if len(ids) != 0 {
		t.Fatalf("ids = %v, want empty", ids)
	}
	if unavailable != 1 {
		t.Fatalf("unavailable = %d, want 1", unavailable)
	}
}

func TestMapRetrievalToPassageIDsSkipsNilAndEmptyContent(t *testing.T) {
	src := []*types.SearchResult{
		nil,
		{Content: "", SourcePassageID: "7"},
		{Content: "ok", SourcePassageID: "7"},
	}
	ids, unavailable := mapRetrievalToPassageIDs(src)
	if !reflect.DeepEqual(ids, []int{7}) {
		t.Fatalf("ids = %v, want [7]", ids)
	}
	if unavailable != 0 {
		t.Fatalf("unavailable = %d, want 0 (empty/nil are not lineage failures)", unavailable)
	}
}

func TestMapRetrievalToPassageIDsDuplicateTextDistinctIdentity(t *testing.T) {
	src := []*types.SearchResult{
		{Content: "dup", SourcePassageID: "100"},
		{Content: "dup", SourcePassageID: "200"},
	}
	ids, unavailable := mapRetrievalToPassageIDs(src)
	if !reflect.DeepEqual(ids, []int{100, 200}) {
		t.Fatalf("ids = %v, want [100 200]", ids)
	}
	if unavailable != 0 {
		t.Fatalf("unavailable = %d, want 0", unavailable)
	}
}

func TestHookMetricLineageUnavailableTracksCount(t *testing.T) {
	h := NewHookMetric(2)
	h.recordInit(0)
	h.recordQaPair(0, &types.QAPair{PIDs: []int{1}, Passages: []string{"p"}, Answer: "a"})
	h.recordSearchResult(0, []*types.SearchResult{
		{Content: "no identity", SourcePassageID: ""},
	})
	h.recordFinish(0)

	h.recordInit(1)
	h.recordQaPair(1, &types.QAPair{PIDs: []int{2}, Passages: []string{"p"}, Answer: "a"})
	h.recordSearchResult(1, []*types.SearchResult{
		{Content: "has identity", SourcePassageID: "2"},
	})
	h.recordFinish(1)

	if got := h.LineageUnavailable(); got != 1 {
		t.Fatalf("LineageUnavailable = %d, want 1", got)
	}
}

func TestHookMetricStandardMetricResult(t *testing.T) {
	h := NewHookMetric(2)

	h.recordInit(0)
	h.recordQaPair(0, &types.QAPair{PIDs: []int{1, 2}, Passages: []string{"a", "b"}, Answer: "a"})
	h.recordSearchResult(0, []*types.SearchResult{
		{Content: "a", SourcePassageID: "1"},
		{Content: "b", SourcePassageID: "2"},
	})
	h.recordFinish(0)

	h.recordInit(1)
	h.recordQaPair(1, &types.QAPair{PIDs: []int{1, 2}, Passages: []string{"a", "b"}, Answer: "a"})
	h.recordSearchResult(1, []*types.SearchResult{
		{Content: "x", SourcePassageID: "9"},
	})
	h.recordFinish(1)

	r := h.StandardMetricResult()
	// Query 0 retrieves [1,2] (both relevant), query 1 retrieves [9] (nothing
	// relevant). With the fixed-cutoff precision@10 (ranx semantics):
	//   precision@10: (2/10 + 0)/2 = 0.1 ; recall@10/MRR/MAP/nDCG@10 = 0.5.
	if r.PrecisionAt10 != 0.1 || r.RecallAt10 != 0.5 || r.MRR != 0.5 || r.MAP != 0.5 || r.NDCGAt10 != 0.5 {
		t.Fatalf("StandardMetricResult = %+v, want precision 0.1 and 0.5 elsewhere", r)
	}
}

// Task016 Step 4 — MetricsJSONPayload must carry the legacy result, the standard
// measurement-contract/v1 metrics and the contract hash (I04, AC08).
func TestHookMetricMetricsJSONPayloadCarriesStandardAndContract(t *testing.T) {
	h := NewHookMetric(1)
	h.recordInit(0)
	h.recordQaPair(0, &types.QAPair{PIDs: []int{1, 2}, Passages: []string{"a", "b"}, Answer: "a"})
	h.recordSearchResult(0, []*types.SearchResult{
		{Content: "a", SourcePassageID: "1"},
	})
	h.recordFinish(0)

	payload := h.MetricsJSONPayload()
	var top struct {
		RetrievalMetrics        map[string]float64 `json:"retrieval_metrics"`
		GenerationMetrics       map[string]float64 `json:"generation_metrics"`
		StandardRetrieval       map[string]float64 `json:"standard_retrieval_metrics"`
		MeasurementContractHash string             `json:"measurement_contract_hash"`
	}
	if err := json.Unmarshal([]byte(payload), &top); err != nil {
		t.Fatalf("payload not valid JSON: %v\n%s", err, payload)
	}
	if _, ok := top.RetrievalMetrics["legacy_nonstandard_precision"]; !ok {
		t.Fatalf("legacy retrieval must use legacy_nonstandard_precision: %v", top.RetrievalMetrics)
	}
	if _, ok := top.RetrievalMetrics["precision"]; ok {
		t.Fatalf("legacy retrieval must NOT expose unprefixed precision: %v", top.RetrievalMetrics)
	}
	if _, ok := top.StandardRetrieval["precision_at_10"]; !ok {
		t.Fatalf("standard_retrieval_metrics missing precision_at_10: %v", top.StandardRetrieval)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(top.MeasurementContractHash) {
		t.Fatalf("measurement_contract_hash = %q, want 64 lowercase hex", top.MeasurementContractHash)
	}
}

// TestHookMetricMetricsJSONPayloadStableContractHash verifies the contract hash
// embedded in the payload matches the frozen measurement contract hash.
func TestHookMetricMetricsJSONPayloadStableContractHash(t *testing.T) {
	h := NewHookMetric(1)
	h.recordInit(0)
	h.recordQaPair(0, &types.QAPair{PIDs: []int{1}, Passages: []string{"a"}, Answer: "a"})
	h.recordSearchResult(0, []*types.SearchResult{{Content: "a", SourcePassageID: "1"}})
	h.recordFinish(0)

	var top struct {
		MeasurementContractHash string `json:"measurement_contract_hash"`
	}
	if err := json.Unmarshal([]byte(h.MetricsJSONPayload()), &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if top.MeasurementContractHash != metric.MeasurementContractHash() {
		t.Fatalf("payload contract hash = %q, want %q", top.MeasurementContractHash, metric.MeasurementContractHash())
	}
}

func TestAggregateMetricsFromItemFactsRecomputesAndKeepsFailureDenominator(t *testing.T) {
	h := NewHookMetric(1)
	h.recordInit(0)
	h.recordQaPair(0, &types.QAPair{QID: 7, PIDs: []int{1}, Passages: []string{"a"}, Answer: "a"})
	h.recordSearchResult(0, []*types.SearchResult{{Content: "a", SourcePassageID: "1"}})
	result := h.recordFinish(0)
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal item result: %v", err)
	}
	items := []*types.EvaluationRunItem{
		{ItemID: "ok", Status: types.EvaluationItemStatusSucceeded, ResultJSON: raw},
		{ItemID: "failed", Status: types.EvaluationItemStatusFailedTerm},
	}
	payload, err := AggregateMetricsFromItemFacts(items)
	if err != nil {
		t.Fatalf("aggregate item metrics: %v", err)
	}
	var got metricPersistPayload
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode aggregate: %v", err)
	}
	if got.StandardRetrieval.RecallAt10 != 0.5 || got.StandardRetrieval.MRR != 0.5 || got.StandardRetrieval.PrecisionAt10 != 0.05 {
		t.Fatalf("failed item disappeared from denominator: %+v", got.StandardRetrieval)
	}

	result.StandardRetrieval.MRR = 0.123
	tampered, _ := json.Marshal(result)
	items[0].ResultJSON = tampered
	if _, err := AggregateMetricsFromItemFacts(items); err == nil {
		t.Fatal("tampered standard metric must fail closed")
	}
}
