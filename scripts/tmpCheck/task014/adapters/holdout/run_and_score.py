#!/usr/bin/env python
"""Task014 Holdout run + score (pilot, synthetic).

Invokes the 3 runnable engines on the synthetic holdout PDFs and scores text
extraction via 1 - CER against the known ground-truth text. UNSUPPORTED engines
are recorded as UNSUPPORTED (counted 0). This is a PILOT synthetic holdout.

Run:
    PYTHONPATH=<weknora-root> docreader/.venv/bin/python \
        scripts/tmpCheck/task014/adapters/holdout/run_and_score.py \
        --manifest <holdout_manifest.json> \
        --pdf-dir <status/raw/task014/holdout> \
        --out <evidence/task014/holdout_results.tsv>
"""

from __future__ import annotations

import argparse
import difflib
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "runner"))
from engine_invoker import invoke  # noqa: E402

ENGINES = ["builtin", "markitdown", "opendataloader", "anydoc", "weknoracloud", "mineru", "mineru_cloud", "paddleocr_vl"]
_WS = re.compile(r"\s+")


def norm(s: str) -> str:
    return _WS.sub(" ", s).strip()


def cer(a: str, b: str) -> float:
    if not a and not b:
        return 0.0
    if not a or not b:
        return 1.0
    if len(a) < len(b):
        a, b = b, a
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        cur = [i]
        for j, cb in enumerate(b, 1):
            cur.append(min(cur[-1] + 1, prev[j] + 1, prev[j - 1] + (ca != cb)))
        prev = cur
    return min(1.0, prev[-1] / max(len(a), len(b)))


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifest", type=Path, required=True)
    ap.add_argument("--pdf-dir", type=Path, required=True)
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()

    man = json.loads(args.manifest.read_text(encoding="utf-8"))
    rows = ["engine\tdocument\tstratum\tstatus\ttext_1_minus_cer\tlatency_ms"]
    for e in ENGINES:
        for r in man["records"]:
            pdf = args.pdf_dir / r["pdf"]
            res = invoke(e, str(pdf), "native_pdf")
            if res.status != "SUCCESS":
                rows.append("\t".join([e, r["subset_id"], r["stratum"], res.status, "0", str(res.latency_ms)]))
                continue
            md = norm(res.markdown)
            gt = norm(r["gt_text"])
            score = 1.0 - cer(gt, md)
            rows.append("\t".join([e, r["subset_id"], r["stratum"], res.status, f"{score:.4f}", str(res.latency_ms)]))
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text("\n".join(rows) + "\n", encoding="utf-8")
    print(f"wrote {len(rows) - 1} holdout rows -> {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
