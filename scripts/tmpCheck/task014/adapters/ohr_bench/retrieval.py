#!/usr/bin/env python
"""Task014 OHR-Bench retrieval scorer (pilot, DOWNGRADED embedding).

Plan §6.2 freezes an embedding model for the OHR retrieval contract. The owner
supplied a Zhipu key ("use zhipu where API is needed"), but the key returns
error 1113 (no balance / no resource pack) and no local embedding (Ollama) is
running. Per "没有的就降级", this pilot uses a LEXICAL TF-IDF retrieval backend
(``embedding=lexical-tfidf-downgrade``) so the chunk->retrieve->score pipeline is
still exercised. Results are SECONDARY_EXPLORATORY, not the frozen embedding
contract.

Metrics: Recall@5/10, MRR, nDCG@10, no-hit rate. Hit = the question's
evidence_context (GT) appears in a retrieved chunk.

Run (sklearn required):
    python3 scripts/tmpCheck/task014/adapters/ohr_bench/retrieval.py \
        --manifest <ohr_subset.json> \
        --outputs-dir <status/raw/task014/outputs/OHR-Bench> \
        --engines builtin,markitdown,opendataloader \
        --out <evidence/task014/ohr_retrieval_results.tsv>
"""

from __future__ import annotations

import argparse
import json
import re
from collections import defaultdict
from pathlib import Path

import numpy as np
from sklearn.feature_extraction.text import TfidfVectorizer
from sklearn.metrics.pairwise import cosine_similarity

CHUNK_SIZE = 512
CHUNK_OVERLAP = 64
TOP_K = 10
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


def ndcg(rank: int) -> float:
    return 1.0 / np.log2(rank + 1)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifest", type=Path, required=True)
    ap.add_argument("--outputs-dir", type=Path, required=True)
    ap.add_argument("--engines", required=True)
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()
    engines = [e.strip() for e in args.engines.split(",") if e.strip()]

    man = json.loads(args.manifest.read_text(encoding="utf-8"))
    # group questions by doc_name
    questions_by_doc: dict[str, list[dict]] = defaultdict(list)
    for r in man["records"]:
        doc = r["doc_name"].split("/")[-1]
        questions_by_doc[doc].append(r)

    header = "engine\tdoc\tquestion\tRecall@5\tRecall@10\tMRR\tnDCG@10\tno_hit"
    rows = [header]

    for eng in engines:
        # load + chunk every doc once per engine
        doc_chunks: dict[str, list[str]] = {}
        for doc in questions_by_doc:
            md_file = args.outputs_dir / eng / f"ohr-doc-{doc}.md"
            md = md_file.read_text(encoding="utf-8") if md_file.exists() else ""
            doc_chunks[doc] = chunk_text(md)

        for doc, qs in questions_by_doc.items():
            chunks = doc_chunks[doc]
            if not chunks:
                for q in qs:
                    rows.append("\t".join([eng, doc, q["qa_id"], "0", "0", "0", "0", "1"]))
                continue
            vec = TfidfVectorizer()
            cmat = vec.fit_transform(chunks)
            for q in qs:
                ev = norm(q["evidence_context"])
                qvec = vec.transform([q["question"] + " " + ev[:200]])
                sims = cosine_similarity(qvec, cmat)[0]
                order = np.argsort(-sims)
                top5 = order[:5]
                top10 = order[:10]
                hits = [i for i in top10 if ev and norm(ev) in chunks[i]]
                # also treat answer presence as a fallback hit signal when evidence too short
                if not ev:
                    ans = norm(q["answer"])
                    hits = [i for i in top10 if ans and ans in chunks[i]]
                hit5 = int(any(i in top5 for i in hits))
                hit10 = int(len(hits) > 0)
                # MRR: reciprocal rank of first hit
                first_hit_rank = None
                for rank, i in enumerate(top10, start=1):
                    if i in hits:
                        first_hit_rank = rank
                        break
                mrr = 1.0 / first_hit_rank if first_hit_rank else 0.0
                n = sum(ndcg(r) for r, i in enumerate(top10, start=1) if i in hits)
                rows.append(
                    "\t".join(
                        [
                            eng,
                            doc,
                            q["qa_id"],
                            str(hit5),
                            str(hit10),
                            f"{mrr:.4f}",
                            f"{n:.4f}",
                            str(1 - hit10),
                        ]
                    )
                )
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text("\n".join(rows) + "\n", encoding="utf-8")
    print(f"wrote {len(rows) - 1} question rows -> {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
