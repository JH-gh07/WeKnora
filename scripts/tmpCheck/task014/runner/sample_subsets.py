#!/usr/bin/env python
"""Task014 subset sampling (deterministic, fixed seed).

Generates the three public-layer pilot manifests from downloaded upstream
metadata ONLY (no engine output is read — plan §2). All sampling uses seed
``20260909`` and is stratified over the available upstream strata.

Layers (pilot scale, "各 50 条"):
  - OmniDocBench: 50 pages, stratified by (data_source, layout).
  - olmOCR-Bench: 50 documents (unique single-page PDFs), stratified by category.
  - OHR-Bench:    50 questions, stratified by evidence_source (text/table/equation).

Run (stdlib only, from anywhere):
    python3 scripts/tmpCheck/task014/runner/sample_subsets.py \
        --omnidoc-meta <OmniDocBench.json> \
        --olmocr-dir <dir with 6 jsonl> \
        --ohr-parquet <OHR-Bench.parquet> \
        --out <evidence/task014/dataset_manifests>
"""

from __future__ import annotations

import argparse
import hashlib
import json
import random
from collections import Counter, defaultdict
from pathlib import Path

SEED = 20260909

OLMOCR_CATEGORIES = [
    "arxiv_math",
    "headers_footers",
    "long_tiny_text",
    "multi_column",
    "old_scans",
    "old_scans_math",
]


def sha256_str(s: str) -> str:
    return hashlib.sha256(s.encode("utf-8")).hexdigest()


# --------------------------------------------------------------------------- #
# OmniDocBench
# --------------------------------------------------------------------------- #
def sample_omnidoc(meta_path: Path, n: int = 50, available: set[str] | None = None) -> dict:
    data = json.loads(meta_path.read_text(encoding="utf-8"))
    strata = defaultdict(list)
    for idx, e in enumerate(data):
        if available is not None and e["page_info"]["image_path"] not in available:
            continue
        pa = e["page_info"].get("page_attribute", {})
        key = (pa.get("data_source", "?"), pa.get("layout", "?"))
        strata[key].append(idx)
    rng = random.Random(SEED)
    # one per non-empty stratum first, remainder by proportional round-robin
    picked: list[int] = []
    stratum_order = sorted(strata.keys())
    for key in stratum_order:
        members = strata[key]
        if members:
            picked.append(rng.choice(members))
    pool = [i for members in strata.values() for i in members if i not in picked]
    rng.shuffle(pool)
    need = n - len(picked)
    picked.extend(pool[:need])
    picked = picked[:n]
    picked.sort()
    frame_size = sum(len(v) for v in strata.values())
    manifest = {
        "layer": "OmniDocBench",
        "seed": SEED,
        "n": len(picked),
        "sampling_frame_note": (
            f"sampled from {frame_size} pages with downloadable images"
            if available is not None else f"sampled from {len(data)} pages"
        ),
        "stratum_counts": {str(k): len(v) for k, v in strata.items()},
        "records": [
            {
                "subset_id": f"omni-{i:04d}",
                "page_index": i,
                "image_path": data[i]["page_info"]["image_path"],
                "page_attribute": data[i]["page_info"]["page_attribute"],
                "categories_present": sorted(
                    {ld.get("category_type") for ld in data[i].get("layout_dets", [])}
                ),
            }
            for i in picked
        ],
    }
    return manifest


# --------------------------------------------------------------------------- #
# olmOCR-Bench
# --------------------------------------------------------------------------- #
def sample_olmocr(jsonl_dir: Path, n: int = 50) -> dict:
    docs: dict[str, list[dict]] = defaultdict(list)  # pdf -> assertions
    per_cat: dict[str, int] = defaultdict(int)
    cat_by_pdf: dict[str, str] = {}
    for cat in OLMOCR_CATEGORIES:
        p = jsonl_dir / f"{cat}.jsonl"
        if not p.exists():
            continue
        for line in p.read_text(encoding="utf-8").splitlines():
            if not line.strip():
                continue
            obj = json.loads(line)
            pdf = obj["pdf"]
            docs[pdf].append(obj)
            cat_by_pdf[pdf] = cat
            per_cat[cat] += 1

    # stratify: one per category, then remainder proportional
    by_cat: dict[str, list[str]] = defaultdict(list)
    for pdf, cat in cat_by_pdf.items():
        by_cat[cat].append(pdf)
    rng = random.Random(SEED)
    picked: list[str] = []
    for cat in OLMOCR_CATEGORIES:
        if by_cat.get(cat):
            picked.append(rng.choice(by_cat[cat]))
    pool = [p for p in cat_by_pdf if p not in picked]
    rng.shuffle(pool)
    picked.extend(pool[: n - len(picked)])
    picked = picked[:n]

    manifest = {
        "layer": "olmOCR-Bench",
        "seed": SEED,
        "n": len(picked),
        "category_assertion_totals": dict(per_cat),
        "records": [
            {
                "subset_id": f"olmocr-{j:04d}",
                "pdf": p,
                "category": cat_by_pdf[p],
                "n_assertions": len(docs[p]),
            }
            for j, p in enumerate(picked)
        ],
    }
    return manifest


# --------------------------------------------------------------------------- #
# OHR-Bench
# --------------------------------------------------------------------------- #
def sample_ohr(parquet_path: Path, n: int = 50) -> dict:
    import pandas as pd

    df = pd.read_parquet(parquet_path)
    qlist: list[dict] = []
    for _, row in df.iterrows():
        qas = row.get("qas")
        if not isinstance(qas, dict):
            continue
        q = qas.get("questions")
        if q is None:
            continue
        for i in range(len(q)):
            qlist.append(
                {
                    "doc_name": str(qas["doc_name"][i]),
                    "question": str(qas["questions"][i]),
                    "answer": str(qas["answers"][i]),
                    "evidence_source": str(qas["evidence_source"][i]),
                    "answer_form": str(qas["answer_form"][i]),
                    "evidence_context": str(qas["evidence_context"][i]),
                    "evidence_page_no": int(qas["evidence_page_no"][i]),
                    "qa_id": str(qas["ID"][i]),
                }
            )
    by_src: dict[str, list[dict]] = defaultdict(list)
    for q in qlist:
        by_src[q["evidence_source"]].append(q)
    rng = random.Random(SEED)
    picked: list[dict] = []
    for src in sorted(by_src.keys()):
        picked.append(rng.choice(by_src[src]))
    pool = [q for q in qlist if q not in picked]
    rng.shuffle(pool)
    picked.extend(pool[: n - len(picked)])
    picked = picked[:n]

    manifest = {
        "layer": "OHR-Bench",
        "seed": SEED,
        "n": len(picked),
        "evidence_source_totals": {k: len(v) for k, v in by_src.items()},
        "note": "upstream parquet evidence_source = {text, table, equation}; plan's chart/reading-order absent from this revision (deviation logged)",
        "records": [
            {
                "subset_id": f"ohr-{j:04d}",
                "doc_name": q["doc_name"],
                "qa_id": q["qa_id"],
                "question": q["question"],
                "answer": q["answer"],
                "evidence_source": q["evidence_source"],
                "answer_form": q["answer_form"],
                "evidence_context": q["evidence_context"],
                "evidence_page_no": q["evidence_page_no"],
            }
            for j, q in enumerate(picked)
        ],
    }
    return manifest


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--omnidoc-meta", type=Path)
    ap.add_argument("--omnidoc-available", type=Path, help="JSON list of available image basenames")
    ap.add_argument("--olmocr-dir", type=Path)
    ap.add_argument("--ohr-parquet", type=Path)
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)

    if args.omnidoc_meta:
        available = None
        if args.omnidoc_available:
            available = set(json.loads(args.omnidoc_available.read_text(encoding="utf-8")))
        m = sample_omnidoc(args.omnidoc_meta, available=available)
        out = args.out / "omnidocbench_subset.json"
        out.write_text(json.dumps(m, ensure_ascii=False, indent=2), encoding="utf-8")
        print(f"OmniDocBench: {m['n']} pages -> {out}")

    if args.olmocr_dir:
        m = sample_olmocr(args.olmocr_dir)
        out = args.out / "olmocr_subset.json"
        out.write_text(json.dumps(m, ensure_ascii=False, indent=2), encoding="utf-8")
        print(f"olmOCR-Bench: {m['n']} docs -> {out}")

    if args.ohr_parquet:
        m = sample_ohr(args.ohr_parquet)
        out = args.out / "ohr_subset.json"
        out.write_text(json.dumps(m, ensure_ascii=False, indent=2), encoding="utf-8")
        print(f"OHR-Bench: {m['n']} QA -> {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
