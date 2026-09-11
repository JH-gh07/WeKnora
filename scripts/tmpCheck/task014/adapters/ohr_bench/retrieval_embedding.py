#!/usr/bin/env python
"""Task014 OHR-Bench retrieval scorer (pilot, LOCAL neural embedding).

Replaces the earlier TF-IDF downgrade with a real neural embedding model run
locally on CPU (no API, no quota): BAAI/bge-small-en-v1.5 via fastembed/ONNX.

Embedding contract: ``embedding=local-bge-small-en-v1.5`` (frozen model name,
frozen chunk 512/64, frozen top_k 5/10). This is a real embedding but is still
NOT the plan §6.2 frozen provider/model (that gate remains open); results stay
SECONDARY until an owner-approved embedding provider is frozen.

Run:
    cd <weknora>/docreader && uv run python \
        ../scripts/tmpCheck/task014/adapters/ohr_bench/retrieval_embedding.py \
        --manifest <ohr_subset.json> --outputs-dir <outputs/OHR-Bench> \
        --engines builtin,markitdown,opendataloader --out <ohr_retrieval_results.tsv>
"""

from __future__ import annotations

import argparse
import json
import re
from collections import defaultdict
from pathlib import Path

import numpy as np
from fastembed import TextEmbedding

CHUNK_SIZE = 512
CHUNK_OVERLAP = 64
TOP_K = 10
MODEL_NAME = "BAAI/bge-small-en-v1.5"
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

    man = json.loads(args.manifest.read_text(encoding="utf-8"))
    questions_by_doc: dict[str, list[dict]] = defaultdict(list)
    for r in man["records"]:
        questions_by_doc[r["doc_name"].split("/")[-1]].append(r)

    print(f"loading {MODEL_NAME} ...")
    model = TextEmbedding(model_name=MODEL_NAME)

    header = "engine\tdoc\tquestion\tRecall@5\tRecall@10\tMRR\tnDCG@10\tno_hit"
    rows = [header]

    for eng in engines:
        # chunk + embed every doc once per engine
        doc_embs: dict[str, np.ndarray] = {}
        doc_chunks: dict[str, list[str]] = {}
        for doc in questions_by_doc:
            md_file = args.outputs_dir / eng / f"ohr-doc-{doc}.md"
            md = md_file.read_text(encoding="utf-8") if md_file.exists() else ""
            chunks = chunk_text(md)
            doc_chunks[doc] = chunks
            if chunks:
                embs = list(model.embed(chunks, batch_size=256))
                doc_embs[doc] = np.stack([l2(np.asarray(e, dtype=float)) for e in embs])
            else:
                doc_embs[doc] = np.zeros((0, 384))

        for doc, qs in questions_by_doc.items():
            mat = doc_embs[doc]
            chunks = doc_chunks[doc]
            if mat.shape[0] == 0:
                for q in qs:
                    rows.append("\t".join([eng, doc, q["qa_id"], "0", "0", "0", "0", "1"]))
                continue
            for q in qs:
                ev = norm(q["evidence_context"])
                qvec = l2(np.asarray(next(model.embed([q["question"]])), dtype=float))
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
        print(f"done engine {eng}")

    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text("\n".join(rows) + "\n", encoding="utf-8")
    print(f"wrote {len(rows) - 1} rows -> {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
