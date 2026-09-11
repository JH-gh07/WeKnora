package metric

import "math"

// Standard retrieval metrics (Task016 Step 4).
//
// These implement the frozen measurement-contract semantics recorded in
// status/evidence/task016/preflight/metric_contract_preregistration.yaml:
//
//   - relevance is BINARY and derived ONLY from stable lineage IDs (I03): an
//     item is relevant iff its ID is in the ground-truth relevant-ID set;
//   - retrieved IDs are de-duplicated keeping the FIRST occurrence, and the
//     @k cutoff is applied AFTER de-duplication;
//   - empty retrieved list yields 0.0 for every metric;
//   - empty ground-truth set yields 0.0 and is recorded with an edge flag by
//     the caller (never silently averaged away, I15).
//
// These functions are pure and take already-cut lists; they do NOT read the
// legacy MetricInput averaging semantics. The legacy Precision/MAP remain in
// precision.go / map.go and are NOT reused by these standard names (AC07).

// dedupKeepingFirst de-duplicates IDs preserving their first occurrence order.
// This matches the frozen dedup rule: "duplicate retrieved IDs are de-duplicated
// keeping first occurrence, before cutoff".
func dedupKeepingFirst(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// truncateK applies the @k cutoff to an already-de-duplicated list.
func truncateK(ids []int, k int) []int {
	if k <= 0 {
		return nil
	}
	if len(ids) <= k {
		return ids
	}
	return ids[:k]
}

// relevantSet builds a binary-relevance lookup set from the ground-truth IDs.
func relevantSet(relevant []int) map[int]struct{} {
	set := make(map[int]struct{}, len(relevant))
	for _, id := range relevant {
		set[id] = struct{}{}
	}
	return set
}

// PrecisionAtK returns |retrieved@k ∩ relevant| / k.
// The denominator is the fixed cutoff k (not |retrieved@k| and not |relevant|),
// matching the frozen preregistration formula and ranx 0.3.20 (metrics/precision.py:
// `_hits(...) / k`). An empty ground truth does not make precision undefined; an
// empty retrieved list returns 0.0. The @k cutoff is applied AFTER de-duplication.
func PrecisionAtK(retrieved, relevant []int, k int) float64 {
	if k <= 0 {
		return 0.0
	}
	dedup := truncateK(dedupKeepingFirst(retrieved), k)
	if len(dedup) == 0 {
		return 0.0
	}
	set := relevantSet(relevant)
	hits := 0
	for _, id := range dedup {
		if _, ok := set[id]; ok {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// RecallAtK returns |retrieved@k ∩ relevant| / |relevant|.
// An empty ground-truth set returns 0.0 (the caller records the edge flag).
func RecallAtK(retrieved, relevant []int, k int) float64 {
	if len(relevant) == 0 {
		return 0.0
	}
	dedup := truncateK(dedupKeepingFirst(retrieved), k)
	if len(dedup) == 0 {
		return 0.0
	}
	set := relevantSet(relevant)
	hits := 0
	for _, id := range dedup {
		if _, ok := set[id]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(relevant))
}

// ReciprocalRankAtK returns the reciprocal rank of the FIRST relevant item in
// the top-k de-duplicated list, or 0.0 if none appears (1-based rank).
func ReciprocalRankAtK(retrieved, relevant []int, k int) float64 {
	dedup := truncateK(dedupKeepingFirst(retrieved), k)
	set := relevantSet(relevant)
	for i, id := range dedup {
		if _, ok := set[id]; ok {
			return 1.0 / float64(i+1)
		}
	}
	return 0.0
}

// AveragePrecision returns (1/|relevant|) * Σ_{i: retrieved[i] relevant}
// precision@(i+1), over the full de-duplicated list. Empty ground truth returns
// 0.0 (edge-flagged by the caller).
func AveragePrecision(retrieved, relevant []int) float64 {
	if len(relevant) == 0 {
		return 0.0
	}
	dedup := dedupKeepingFirst(retrieved)
	if len(dedup) == 0 {
		return 0.0
	}
	set := relevantSet(relevant)
	var sum float64
	hits := 0
	for i, id := range dedup {
		if _, ok := set[id]; ok {
			hits++
			sum += float64(hits) / float64(i+1)
		}
	}
	return sum / float64(len(relevant))
}

// MeanAveragePrecision returns the mean of AveragePrecision over the queries.
// Each query contributes its AP; empty-GT queries contribute 0.0 (edge-flagged
// by the caller, never averaged away silently).
func MeanAveragePrecision(ranked map[int][]int, relevant map[int][]int) float64 {
	qids := make([]int, 0, len(relevant))
	for qid := range relevant {
		qids = append(qids, qid)
	}
	if len(qids) == 0 {
		return 0.0
	}
	var sum float64
	for _, qid := range qids {
		sum += AveragePrecision(ranked[qid], relevant[qid])
	}
	return sum / float64(len(qids))
}

// NDCGAtK returns DCG@k / IDCG@k with binary relevance and log2(i+1) discount
// (i is 0-indexed, so the first position has discount log2(2)=1).
// DCG@k = Σ_{i=0..k-1} (2^{rel_i}-1) / log2(i+2). Empty retrieved or empty
// ground truth returns 0.0.
func NDCGAtK(retrieved, relevant []int, k int) float64 {
	if len(relevant) == 0 {
		return 0.0
	}
	dedup := truncateK(dedupKeepingFirst(retrieved), k)
	if len(dedup) == 0 {
		return 0.0
	}
	set := relevantSet(relevant)

	var dcg float64
	for i, id := range dedup {
		if _, ok := set[id]; ok {
			dcg += 1.0 / math.Log2(float64(i)+2.0)
		}
	}

	// IDCG: the |relevant| relevant items occupy ranks 1..min(k, |relevant|).
	idealLen := len(relevant)
	if idealLen > k {
		idealLen = k
	}
	if idealLen == 0 {
		return 0.0
	}
	var idcg float64
	for r := 0; r < idealLen; r++ {
		idcg += 1.0 / math.Log2(float64(r)+2.0)
	}
	if idcg == 0 {
		return 0.0
	}
	return dcg / idcg
}
