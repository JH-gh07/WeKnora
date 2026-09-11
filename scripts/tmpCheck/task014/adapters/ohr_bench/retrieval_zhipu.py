#!/usr/bin/env python
"""Task014 OHR-Bench retrieval scorer — Zhipu embedding-3 (frozen provider).

Replaces the TF-IDF downgrade with the plan §6.2 embedding contract:
``embedding=zhipu-embedding-3`` (2048-dim), chunk 512/64, top_k 5/10.

Run (needs ZHIPU_API_KEY in env):
    ZHIPU_API_KEY=... python3 scripts/tmpCheck/task014/adapters/ohr_bench/retrieval_zhipu.py \
        --manifest <ohr_subset.json> --outputs-dir <outputs/OHR-Bench> \
        --engines builtin,markitdown,opendataloader --out <ohr_retrieval_results.tsv>
"""

from __future__ import annotations

import argparse
import json
import os
import re
import time
from collections import defaultdict
from pathlib import Path

import numpy as np
import requests

CHUNK_SIZE = 512
CHUNK_OVERLAP = 64
TOP_K = 10
MODEL = "embedding-3"
DIM = 2048
BATCH = 32
ENDPOINT = "https://open.bigmodel.cn/api/paas/v4/embeddings"
_WS = re.compile(r"\s+")


def norm(s: str) -> str:
    return _WS.sub(" ", s).strip()


def chunk_text(text: str, size: int = CHUNK_SIZE, overlap: int = CHUNK_OVERLAP) -> list[str]:
    text = norm(text)
    if not text:
        return []
    chunks: list[str] = []
    i = 0
    while i < len(text):
        chunks.append(text[i : i + size])
        if i + size >= len(text):
            break
        i += size - overlap
    return chunks


def embed_batch(texts: list[str], key: str, max_retries: int = 4) -> np.ndarray:
    """Embed a batch of texts via Zhipu embedding-3. Returns (n, 2048)."""
    for attempt in range(max_retries):
        try:
            r = requests.post(
                ENDPOINT,
                headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
                json={"model": MODEL, "input": texts},
                timeout=60,
            )
            if r.status_code == 200:
                data = r.json()["data"]
                data = sorted(data, key=lambda x: x["index"])
                return np.stack([np.asarray(d["embedding"], dtype=float) for d in data])
            if r.status_code == 429 or r.status_code >= 500:
                time.sleep(2 * (attempt + 1))
                continue
            raise RuntimeError(f"embedding http {r.status_code}: {r.text[:200]}")
        except Exception as e:  # noqa: BLE001
            if attempt == max_retries - 1:
                raise
            time.sleep(2 * (attempt + 1))
    raise RuntimeError("embedding failed after retries")


def l2(v: np.ndarray) -> np.ndarray:
    n = np.linalg.norm(v)
    return v / n if n > 0 else v


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
    questions_by_doc: dict[str, list[dict]] = defaultdict(list)
    for r in man["records"]:
        questions_by_doc[r["doc_name"].split("/")[-1]].append(r)

    header = "engine\tdoc\tquestion\tRecall@5\tRecall@10\tMRR\tnDCG@10\tno_hit"
    rows = [header]

    for eng in engines:
        print(f"== engine {eng} ==", flush=True)
        doc_embs: dict[str, np.ndarray] = {}
        doc_chunks: dict[str, list[str]] = {}
        for doc in questions_by_doc:
            md_file = args.outputs_dir / eng / f"ohr-doc-{doc}.md"
            md = md_file.read_text(encoding="utf-8") if md_file.exists() else ""
            chunks = chunk_text(md)
            doc_chunks[doc] = chunks
            if not chunks:
                doc_embs[doc] = np.zeros((0, DIM))
                continue
            embs = []
            for i in range(0, len(chunks), BATCH):
                embs.append(embed_batch(chunks[i : i + BATCH], key))
            mat = np.vstack(embs)
            doc_embs[doc] = mat / np.linalg.norm(mat, axis=1, keepdims=True)
            print(f"  {doc}: {len(chunks)} chunks embedded", flush=True)

        for doc, qs in questions_by_doc.items():
            mat = doc_embs[doc]
            chunks = doc_chunks[doc]
            if mat.shape[0] == 0:
                for q in qs:
                    rows.append("\t".join([eng, doc, q["qa_id"], "0", "0", "0", "0", "1"]))
                continue
            for q in qs:
                ev = norm(q["evidence_context"])
                qvec = l2(embed_batch([q["question"]], key)[0])
                sims = mat @ qvec
                order = np.argsort(-sims)
                top5 = order[:5]
                top10 = order[:10]
                hits = [i for i in top10 if ev and ev in chunks[i]]
                if not ev:
                    ans = norm(q["answer"])
                    hits = [i for i in top10 if ans and ans in chunks[i]]
                hit5 = int(any(i in top5 for i in hits))
                hit10 = int(len(hits) > 0)
                first = None
                for rank, i in enumerate(top10, start=1):
                    if i in hits:
                        first = rank
                        break
                mrr = 1.0 / first if first else 0.0
                n = sum(1.0 / np.log2(r + 1) for r, i in enumerate(top10, start=1) if i in hits)
                rows.append("\t".join([eng, doc, q["qa_id"], str(hit5), str(hit10), f"{mrr:.4f}", f"{n:.4f}", str(1 - hit10)]))

    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text("\n".join(rows) + "\n", encoding="utf-8")
    print(f"wrote {len(rows) - 1} rows -> {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
