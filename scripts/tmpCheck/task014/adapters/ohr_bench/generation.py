#!/usr/bin/env python
"""Task014 OHR-Bench generation scorer (pilot) — glm-4-flash LLM-as-judge QA.

Approximates the OHR-Bench "generation" stage: for each question, retrieve the
top-3 chunks from the engine's parsed markdown via a lexical (BM25-lite) score
(no GT leakage: query = question only), then ask glm-4-flash to answer from that
context, and compare to the GT answer.

Model: glm-4-flash (owner quota). This is a LIGHT approximation of the official
OHR generation stage (full official evaluator needs torch/FlagEmbedding/llama-index;
deferred as expansion). Metric: normalized exact-match (primary) + contains.

Run:
    ZHIPU_API_KEY=... python3 scripts/tmpCheck/task014/adapters/ohr_bench/generation.py \
        --manifest <ohr_subset.json> --outputs-dir <outputs/OHR-Bench> \
        --engines builtin,markitdown,opendataloader --out <ohr_generation_results.tsv>
"""

from __future__ import annotations

import argparse
import json
import os
import re
import time
from collections import Counter
from pathlib import Path

import requests

CHUNK_SIZE = 512
CHUNK_OVERLAP = 64
TOP_CONTEXT = 3
CHAT_ENDPOINT = "https://open.bigmodel.cn/api/paas/v4/chat/completions"
CHAT_MODEL = "glm-4-flash"
_WS = re.compile(r"\s+")
_ALNUM = re.compile(r"[^a-z0-9]+")


def norm(s: str) -> str:
    return _WS.sub(" ", s).strip()


def norm_answer(s: str) -> str:
    return _ALNUM.sub("", norm(s).lower())


def chunk_text(text: str, size: int = CHUNK_SIZE, overlap: int = CHUNK_OVERLAP) -> list[str]:
    text = norm(text)
    if not text:
        return []
    out: list[str] = []
    i = 0
    while i < len(text):
        out.append(text[i : i + size])
        if i + size >= len(text):
            break
        i += size - overlap
    return out


def _tokens(s: str) -> Counter:
    return Counter(re.findall(r"[a-z0-9]+", s.lower()))


def lexical_topk(query: str, chunks: list[str], k: int = TOP_CONTEXT) -> list[int]:
    qt = _tokens(query)
    if not qt:
        return list(range(min(k, len(chunks))))
    scores = []
    for i, c in enumerate(chunks):
        ct = _tokens(c)
        overlap = sum((qt & ct).values())
        if overlap:
            scores.append((overlap, i))
    scores.sort(reverse=True)
    top = [i for _, i in scores[:k]]
    if len(top) < k:
        for i in range(min(k, len(chunks))):
            if i not in top:
                top.append(i)
    return top[:k]


def chat_answer(question: str, context: str, key: str, max_retries: int = 3) -> str:
    prompt = (
        "You are a precise document question-answering assistant. Answer the question "
        "using ONLY the provided context. Give a short, direct answer (a phrase, number, or yes/no). "
        "If the context does not contain the answer, say 'not found'.\n\n"
        f"Context:\n{context}\n\nQuestion: {question}\nAnswer:"
    )
    for attempt in range(max_retries):
        try:
            r = requests.post(
                CHAT_ENDPOINT,
                headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
                json={"model": CHAT_MODEL, "messages": [{"role": "user", "content": prompt}], "temperature": 0.0},
                timeout=60,
            )
            if r.status_code == 200:
                return r.json()["choices"][0]["message"]["content"].strip()
            if r.status_code == 429:
                time.sleep(3 * (attempt + 1))
                continue
            raise RuntimeError(f"chat http {r.status_code}: {r.text[:200]}")
        except Exception as e:  # noqa: BLE001
            if attempt == max_retries - 1:
                return f"ERROR: {e}"
            time.sleep(2 * (attempt + 1))
    return "ERROR"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifest", type=Path, required=True)
    ap.add_argument("--outputs-dir", type=Path, required=True)
    ap.add_argument("--engines", required=True)
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()
    engines = [e.strip() for e in args.engines.split(",") if e.strip()]
    key = os.environ.get("ZHIPU_API_KEY", "").strip()
    if not key:
        raise SystemExit("ZHIPU_API_KEY not set")

    man = json.loads(args.manifest.read_text(encoding="utf-8"))
    header = "engine\tdoc\tquestion\tgt_answer\tpred_answer\texact_match\tcontains"
    rows = [header]

    for eng in engines:
        print(f"== engine {eng} ==", flush=True)
        # group questions by doc
        from collections import defaultdict
        q_by_doc: dict[str, list[dict]] = defaultdict(list)
        for r in man["records"]:
            q_by_doc[r["doc_name"].split("/")[-1]].append(r)
        doc_chunks: dict[str, list[str]] = {}
        for doc in q_by_doc:
            md_file = args.outputs_dir / eng / f"ohr-doc-{doc}.md"
            md = md_file.read_text(encoding="utf-8") if md_file.exists() else ""
            doc_chunks[doc] = chunk_text(md)
        for doc, qs in q_by_doc.items():
            chunks = doc_chunks[doc]
            for q in qs:
                if not chunks:
                    rows.append("\t".join([eng, doc, q["qa_id"], norm(q["answer"]), "not found", "0", "0"]))
                    continue
                top = lexical_topk(q["question"], chunks)
                context = "\n".join(chunks[i] for i in top)
                pred = chat_answer(q["question"], context, key)
                gt = norm(q["answer"])
                pred_norm = norm(pred)
                exact = int(norm_answer(pred) == norm_answer(gt))
                contains = int(norm_answer(gt) in norm_answer(pred)) if norm_answer(gt) else 0
                rows.append("\t".join([eng, doc, q["qa_id"], gt, pred_norm[:200], str(exact), str(contains)]))
                print(f"  {q['qa_id'][:8]} gt={gt[:30]!r} pred={pred_norm[:40]!r} exact={exact}", flush=True)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text("\n".join(rows) + "\n", encoding="utf-8")
    print(f"wrote {len(rows) - 1} rows -> {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
