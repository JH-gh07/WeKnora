package metric

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// MeasurementContractVersion is the non-UNVERSIONED measurement-contract schema
// version (Task016 Step 4, AC08). Every NEW evaluation result must carry a
// contract hash derived from this version; results without it (or with the
// legacy UNVERSIONED status) are NOT_COMPARABLE_MEASUREMENT_CONTRACT.
const MeasurementContractVersion = "measurement-contract/v1"

// standardMetricSource is the evaluator code artifact whose digest is folded into
// the measurement-contract hash. Embedding it makes the hash change automatically
// whenever the standard metric implementation changes (Step 4 action 5, F05).
//
//go:embed standard.go
var standardMetricSource []byte

// metricContractDef is one metric's frozen definition.
type metricContractDef struct {
	Name    string `json:"name"`
	Formula string `json:"formula"`
	Cutoff  string `json:"cutoff"`
	Edge    string `json:"edge_rules"`
}

// metricContract is the canonical, secret-free measurement contract document.
type metricContract struct {
	SchemaVersion string              `json:"schema_version"`
	Relevance     string              `json:"relevance_model"`
	Dedup         string              `json:"dedup_rule"`
	Metrics       []metricContractDef `json:"metrics"`
	ArtifactSHA   string              `json:"evaluator_artifact_sha256"`
}

// canonicalMetricContract builds the frozen contract document. Field order is
// deterministic (metrics are sorted by name), so the resulting hash is stable
// across processes and serializations (W3).
func canonicalMetricContract() metricContract {
	metrics := []metricContractDef{
		{"precision_at_k", "|retrieved@k ∩ relevant| / k", "k configurable; default 10", "empty retrieved=0.0; empty GT not applicable"},
		{"recall_at_k", "|retrieved@k ∩ relevant| / |relevant|", "k configurable; default 10", "empty retrieved=0.0; empty GT=0.0 edge-flagged"},
		{"mrr", "(1/|Q|) * Σ 1/rank_i; rank_i=1-based rank of first relevant", "top-k default 10", "0 if none in top-k"},
		{"ap", "(1/|relevant|) * Σ_{k: item@k relevant} precision@k", "full ranked list", "empty GT=0.0 edge-flagged"},
		{"map", "mean of AP over queries", "full ranked list", "query with empty GT contributes 0.0 edge-flagged"},
		{"ndcg_at_k", "DCG@k/IDCG@k; DCG=Σ(2^{rel_i}-1)/log2(i+1); binary rel", "k configurable; default 3 and 10", "empty retrieved=0.0; empty GT=0.0 edge-flagged"},
	}
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Name < metrics[j].Name })
	sum := sha256.Sum256(standardMetricSource)
	return metricContract{
		SchemaVersion: MeasurementContractVersion,
		Relevance:     "binary; relevance determined ONLY by stable lineage IDs (I03)",
		Dedup:         "de-duplicate retrieved IDs keeping first occurrence, before cutoff",
		Metrics:       metrics,
		ArtifactSHA:   hex.EncodeToString(sum[:]),
	}
}

// MeasurementContractHash returns the SHA-256 of the canonical measurement
// contract. It is stable for a given standard.go implementation and changes when
// the implementation (or the frozen definitions) changes. Used to gate blocking
// comparisons (I04, I14, AC07/AC08).
func MeasurementContractHash() string {
	b, err := json.Marshal(canonicalMetricContract())
	if err != nil {
		// json.Marshal of this fixed struct cannot fail; fail loudly if it ever does.
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
