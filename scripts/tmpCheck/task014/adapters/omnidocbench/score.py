#!/usr/bin/env python
"""Task014 OmniDocBench simplified pilot scorer.

PILOT-ONLY scorer: computes direction-normalized signal (1 - CER on text, plus
block/equation/table coverage) directly from the frozen OmniDocBench GT. It is
NOT the official OmniDocBench evaluator (NED/BLEU/METEOR/TEDS/mAP) — that is an
expansion step gated by upstream-evaluator parity. Output is labelled
``scorer=pilot-simplified`` so it is never mistaken for official scores.

Run (stdlib only):
    python3 scripts/tmpCheck/task014/adapters/omnidocbench/score.py \
        --meta <OmniDocBench.json> \
        --manifest <omnidocbench_subset.json> \
        --outputs-dir <status/raw/task014/outputs/OmniDocBench> \
        --engines builtin,markitdown,opendataloader \
        --out <evidence/task014/omnidocbench_results.tsv>
"""

from __future__ import annotations

import argparse
import difflib
import json
import re
from pathlib import Path

_WS = re.compile(r"\s+")


def norm(s: str) -> str:
    return _WS.sub(" ", s).strip()


def _levenshtein(a: str, b: str) -> int:
    if len(a) < len(b):
        a, b = b, a
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        cur = [i]
        for j, cb in enumerate(b, 1):
            cur.append(min(cur[-1] + 1, prev[j] + 1, prev[j - 1] + (ca != cb)))
        prev = cur
    return prev[-1]


def cer(a: str, b: str) -> float:
    """Character error rate (0..1) between two normalized strings."""
    if not a and not b:
        return 0.0
    if not a or not b:
        return 1.0
    return min(1.0, _levenshtein(a, b) / len(a))


def load_gt(meta_path: Path) -> dict[int, dict]:
    data = json.loads(meta_path.read_text(encoding="utf-8"))
    gt: dict[int, dict] = {}
    for i, e in enumerate(data):
        layout = e.get("layout_dets", [])
        text_blocks = [ld.get("text", "") for ld in layout if ld.get("category_type") in ("text_block", "title", "list_group", "header", "footer", "page_number", "table_caption", "figure_caption", "text")]
        text_blocks = [norm(t) for t in text_blocks if t and norm(t)]
        equations = [
            norm(ld.get("latex", ""))
            for ld in layout
            if ld.get("category_type") in ("equation_isolated", "equation_inline", "equation_caption", "equation_label")
            and ld.get("latex")
        ]
        tables = [
            norm(ld.get("html", "") or ld.get("latex", "") or ld.get("text", ""))
            for ld in layout
            if ld.get("category_type") == "table" and (ld.get("html") or ld.get("text"))
        ]
        gt[i] = {
            "text": " ".join(text_blocks),
            "n_text_blocks": len(text_blocks),
            "text_blocks": text_blocks,
            "equations": equations,
            "tables": tables,
        }
    return gt


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--meta", type=Path, required=True)
    ap.add_argument("--manifest", type=Path, required=True)
    ap.add_argument("--outputs-dir", type=Path, required=True)
    ap.add_argument("--engines", required=True)
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()
    engines = [e.strip() for e in args.engines.split(",") if e.strip()]

    gt = load_gt(args.meta)
    man = json.loads(args.manifest.read_text(encoding="utf-8"))

    header = "engine\tpage\ttext_1_minus_cer\tn_text_blocks\tblock_coverage\tn_equations\tequation_coverage\tn_tables\ttable_coverage"
    rows = [header]
    for eng in engines:
        for r in man["records"]:
            pi = r["page_index"]
            g = gt[pi]
            md_file = args.outputs_dir / eng / f"{r['subset_id']}.md"
            md = norm(md_file.read_text(encoding="utf-8")) if md_file.exists() else ""
            text_score = 1.0 - cer(g["text"], md) if g["text"] else None
            block_cov = (
                sum(1 for b in g["text_blocks"] if b in md) / len(g["text_blocks"])
                if g["text_blocks"] else None
            )
            eq_cov = (
                sum(1 for e in g["equations"] if norm(e) in md) / len(g["equations"])
                if g["equations"] else None
            )
            tab_cov = (
                sum(1 for t in g["tables"] if norm(t) in md) / len(g["tables"])
                if g["tables"] else None
            )
            rows.append(
                "\t".join(
                    [
                        eng,
                        r["subset_id"],
                        f"{text_score:.4f}" if text_score is not None else "NA",
                        str(g["n_text_blocks"]),
                        f"{block_cov:.4f}" if block_cov is not None else "NA",
                        str(len(g["equations"])),
                        f"{eq_cov:.4f}" if eq_cov is not None else "NA",
                        str(len(g["tables"])),
                        f"{tab_cov:.4f}" if tab_cov is not None else "NA",
                    ]
                )
            )
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text("\n".join(rows) + "\n", encoding="utf-8")
    print(f"wrote {len(rows) - 1} page rows -> {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
