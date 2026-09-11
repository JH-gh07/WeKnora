#!/usr/bin/env python3
"""
Task016 Step 4 / W2 — Metric Differential reference-vector generator.

Generates 10,000 fixed-seed random ranking cases and computes the reference
retrieval metrics using the EXACT formulas of the pinned reference evaluator
ranx 0.3.20 (PyPI). ranx 0.3.20's metric semantics were read directly from the
installed wheel source:

  ranx/metrics/precision.py        -> Precision@k  = _hits(qrels, run, k) / k
  ranx/metrics/recall.py           -> Recall@k     = _hits(qrels, run, k) / |qrels|
  ranx/metrics/reciprocal_rank.py  -> RR@k         = 1/(1-based rank of first hit)
  ranx/metrics/average_precision.py-> AP           = (1/|qrels|) * sum of precision@r over hits
  ranx/metrics/ndcg.py (jarvelin)  -> nDCG@k       = DCG@k / IDCG@k, discount log2(i+2)
  ranx/metrics/common.py           -> fix_k(k,run) = min(k, |run|) for k>0; clean_qrels(rel_lvl=1)

The reference evaluator is run with binary relevance (rel in {0,1}, rel_lvl=1),
matching the frozen measurement contract (I03). Because the contract de-duplicates
retrieved IDs keeping the first occurrence BEFORE cutoff (and ranx does not), every
generated case uses UNIQUE retrieved IDs so de-duplication is the identity and the
two implementations are directly comparable (the dedup rule is covered separately by
Go-only golden tests).

Seed: 161803399 (frozen in workload_manifest.json W2).
"""
import hashlib
import random
import sys

SEED = 161803399
N_CASES = 10000
ID_SPACE = 500          # candidate IDs live in [0, ID_SPACE)
MAX_RELEVANT = 10
MAX_RETRIEVED = 30


def fix_k(k, n_run):
    # ranx/metrics/common.py: run.shape[0] if k == 0 or k > run.shape[0] else k
    return n_run if (k == 0 or k > n_run) else k


def hits(relevant_set, run, k):
    # ranx/metrics/hits.py _hits (binary relevance, no dedup)
    kk = fix_k(k, len(run))
    h = 0
    for i in range(kk):
        if run[i] in relevant_set:
            h += 1
    return h


def precision_at_k(relevant_set, run, k):
    # ranx/metrics/precision.py _precision: k = k if k != 0 else len(run); hits/k
    kk = k if k != 0 else len(run)
    if kk == 0:
        return 0.0
    return hits(relevant_set, run, kk) / float(kk)


def recall_at_k(relevant_set, run, k):
    # ranx/metrics/recall.py _recall: hits / |qrels| (clean_qrels already applied)
    if len(relevant_set) == 0:
        return 0.0
    kk = k if k != 0 else len(run)
    if kk == 0:
        return 0.0
    return hits(relevant_set, run, kk) / float(len(relevant_set))


def reciprocal_rank_at_k(relevant_set, run, k):
    # ranx/metrics/reciprocal_rank.py _reciprocal_rank
    if len(relevant_set) == 0:
        return 0.0
    kk = fix_k(k, len(run))
    for i in range(kk):
        if run[i] in relevant_set:
            return 1.0 / (i + 1)
    return 0.0


def average_precision(relevant_set, run):
    # ranx/metrics/average_precision.py _average_precision with k=0 (full list)
    if len(relevant_set) == 0:
        return 0.0
    kk = fix_k(0, len(run))  # full list
    hits_so_far = 0
    total = 0.0
    for i in range(kk):
        if run[i] in relevant_set:
            hits_so_far += 1
            total += float(hits_so_far) / float(i + 1)
    return total / float(len(relevant_set))


def ndcg_at_k(relevant_set, run, k):
    # ranx/metrics/ndcg.py _ndcg (jarvelin=True): DCG@k / IDCG@k, log2(i+2)
    if len(relevant_set) == 0:
        return 0.0
    import math
    kk = fix_k(k, len(run))
    dcg = 0.0
    for i in range(kk):
        if run[i] in relevant_set:
            dcg += 1.0 / math.log2(float(i) + 2.0)
    ideal_len = min(k, len(relevant_set))
    idcg = 0.0
    for r in range(ideal_len):
        idcg += 1.0 / math.log2(float(r) + 2.0)
    if idcg == 0.0:
        idcg = 1.0
    return dcg / idcg


def main():
    rng = random.Random(SEED)
    rows = []
    for case_id in range(N_CASES):
        m = rng.randint(0, MAX_RELEVANT)
        n = rng.randint(0, MAX_RETRIEVED)
        # unique sorted relevant IDs
        relevant = sorted(rng.sample(range(ID_SPACE), m))
        # unique retrieved IDs (order = retrieval rank); uniqueness => dedup is identity
        retrieved = rng.sample(range(ID_SPACE), n)
        rel_set = set(relevant)
        p10 = precision_at_k(rel_set, retrieved, 10)
        r10 = recall_at_k(rel_set, retrieved, 10)
        mrr = reciprocal_rank_at_k(rel_set, retrieved, 10)
        ap = average_precision(rel_set, retrieved)
        n3 = ndcg_at_k(rel_set, retrieved, 3)
        n10 = ndcg_at_k(rel_set, retrieved, 10)
        rows.append((case_id, relevant, retrieved, p10, r10, mrr, ap, n3, n10))

    lines = []
    for case_id, relevant, retrieved, p10, r10, mrr, ap, n3, n10 in rows:
        rel_s = ",".join(str(x) for x in relevant)
        ret_s = ",".join(str(x) for x in retrieved)
        lines.append("%d\t%s\t%s\t%.17g\t%.17g\t%.17g\t%.17g\t%.17g\t%.17g" % (
            case_id, rel_s, ret_s, p10, r10, mrr, ap, n3, n10))

    body = "\n".join(lines) + "\n"
    sha = hashlib.sha256(body.encode("utf-8")).hexdigest()
    out = sys.argv[1] if len(sys.argv) > 1 else "metric_differential.tsv"
    with open(out, "w", encoding="utf-8") as f:
        f.write(body)
    print("cases=%d bytes=%d sha256=%s" % (N_CASES, len(body), sha))
    print("wrote", out)


if __name__ == "__main__":
    main()
