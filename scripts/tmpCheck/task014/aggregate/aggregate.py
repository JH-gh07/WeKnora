#!/usr/bin/env python
"""Task014 aggregation (pilot).

Reads the execution ledger + the three scorer TSVs and produces:
  - per-engine terminal-status + latency summary
  - per-engine, per-layer aggregate metrics (document-cluster bootstrap 95% CI)
  - a JSON summary written to --out

Run (stdlib + numpy):
    python3 scripts/tmpCheck/task014/aggregate/aggregate.py \
        --ledger <execution_ledger.jsonl> \
        --omnidoc <omnidocbench_results.tsv> \
        --olmocr <olmocr_assertion_results.tsv> \
        --ohr <ohr_retrieval_results.tsv> \
        --out <statistical_tests.json>
"""

from __future__ import annotations

import argparse
import json
from collections import defaultdict
from pathlib import Path

import numpy as np

ENGINES = [
    "builtin",
    "markitdown",
    "opendataloader",
    "anydoc",
    "weknoracloud",
    "mineru",
    "mineru_cloud",
    "paddleocr_vl",
]
RUNNABLE = ["builtin", "markitdown", "opendataloader"]
SEED = 20260909


def read_tsv(path: Path) -> list[dict]:
    if not path.exists():
        return []
    lines = path.read_text(encoding="utf-8").splitlines()
    header = lines[0].split("\t")
    rows = []
    for ln in lines[1:]:
        if not ln.strip():
            continue
        vals = ln.split("\t")
        rows.append(dict(zip(header, vals)))
    return rows


def bootstrap_mean_ci(values: list[float], n: int = 10000, seed: int = SEED) -> dict:
    vals = np.asarray(values, dtype=float)
    if vals.size == 0:
        return {"mean": None, "lo": None, "hi": None, "n": 0}
    rng = np.random.default_rng(seed)
    means = np.empty(n)
    for i in range(n):
        idx = rng.integers(0, vals.size, vals.size)
        means[i] = vals[idx].mean()
    return {
        "mean": float(vals.mean()),
        "lo": float(np.percentile(means, 2.5)),
        "hi": float(np.percentile(means, 97.5)),
        "n": int(vals.size),
    }


def ledger_summary(ledger: Path) -> dict:
    rows = [json.loads(l) for l in ledger.read_text(encoding="utf-8").splitlines() if l.strip()]
    per_engine: dict[str, dict] = {}
    for e in ENGINES:
        erows = [r for r in rows if r["engine_id"] == e]
        statuses = defaultdict(int)
        lat = []
        for r in erows:
            statuses[r["status"]] += 1
            if r["status"] == "SUCCESS" and r.get("latency_ms"):
                lat.append(r["latency_ms"])
        per_engine[e] = {
            "n_records": len(erows),
            "status_counts": dict(statuses),
            "latency_ms_p50": float(np.percentile(lat, 50)) if lat else None,
            "latency_ms_p95": float(np.percentile(lat, 95)) if lat else None,
        }
    return per_engine


def omnidoc_agg(rows: list[dict]) -> dict:
    out: dict[str, dict] = {}
    for eng in RUNNABLE:
        erows = [r for r in rows if r["engine"] == eng]
        text = [float(r["text_1_minus_cer"]) for r in erows if r["text_1_minus_cer"] != "NA"]
        block = [float(r["block_coverage"]) for r in erows if r["block_coverage"] != "NA"]
        eq = [float(r["equation_coverage"]) for r in erows if r["equation_coverage"] != "NA"]
        tab = [float(r["table_coverage"]) for r in erows if r["table_coverage"] != "NA"]
        out[eng] = {
            "text_1_minus_cer": bootstrap_mean_ci(text),
            "block_coverage": bootstrap_mean_ci(block),
            "equation_coverage": bootstrap_mean_ci(eq),
            "table_coverage": bootstrap_mean_ci(tab),
        }
    return out


def olmocr_agg(rows: list[dict]) -> dict:
    out: dict[str, dict] = {}
    for eng in RUNNABLE:
        erows = [r for r in rows if r["engine"] == eng]
        # aggregate to document first (cluster unit), then bootstrap
        by_doc: dict[str, list[int]] = defaultdict(list)
        for r in erows:
            by_doc[r["document"]].append(int(r["pass"]))
        doc_rates = [np.mean(v) for v in by_doc.values()]
        out[eng] = {
            "document_pass_rate": bootstrap_mean_ci(doc_rates),
            "assertion_pass_rate": bootstrap_mean_ci([float(r["pass"]) for r in erows]),
            "n_documents": len(by_doc),
            "n_assertions": len(erows),
        }
    return out


def ohr_agg(rows: list[dict]) -> dict:
    out: dict[str, dict] = {}
    for eng in RUNNABLE:
        erows = [r for r in rows if r["engine"] == eng]
        r5 = [float(r["Recall@5"]) for r in erows]
        r10 = [float(r["Recall@10"]) for r in erows]
        mrr = [float(r["MRR"]) for r in erows]
        nd = [float(r["nDCG@10"]) for r in erows]
        nohit = [float(r["no_hit"]) for r in erows]
        out[eng] = {
            "Recall@5": bootstrap_mean_ci(r5),
            "Recall@10": bootstrap_mean_ci(r10),
            "MRR": bootstrap_mean_ci(mrr),
            "nDCG@10": bootstrap_mean_ci(nd),
            "no_hit_rate": bootstrap_mean_ci(nohit),
            "n_questions": len(erows),
        }
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--ledger", type=Path, required=True)
    ap.add_argument("--omnidoc", type=Path)
    ap.add_argument("--olmocr", type=Path)
    ap.add_argument("--ohr", type=Path)
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()

    summary = {
        "schema_version": "task014-aggregate/v1.0",
        "engines": ledger_summary(args.ledger),
        "omnidocbench": omnidoc_agg(read_tsv(args.omnidoc)) if args.omnidoc else {},
        "olmocr_bench": olmocr_agg(read_tsv(args.olmocr)) if args.olmocr else {},
        "ohr_bench": ohr_agg(read_tsv(args.ohr)) if args.ohr else {},
        "note": (
            "pilot-50-each; OmniDocBench scorer = pilot-simplified (NOT official evaluator); "
            "OHR embedding = lexical-tfidf-downgrade (zhipu no-balance); UNSUPPORTED engines count 0."
        ),
    }
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"wrote {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
