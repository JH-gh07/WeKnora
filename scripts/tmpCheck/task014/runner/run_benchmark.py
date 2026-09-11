#!/usr/bin/env python
"""Task014 pilot benchmark runner.

Runs all 8 engines over the sampled inputs (3 runnable + 5 UNSUPPORTED) and
writes an execution ledger (JSONL) with one terminal record per engine x unit.
Imports the parser classes ONCE so the ~30s import cost is amortized.

Run from WeKnora repo root:
    PYTHONPATH=<weknora-root> docreader/.venv/bin/python \
        scripts/tmpCheck/task014/runner/run_benchmark.py \
        --manifests-dir <evidence/task014/dataset_manifests> \
        --raw-dir <status/raw/task014> \
        --ledger <status/evidence/task014/execution_ledger.jsonl>
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from engine_invoker import invoke  # noqa: E402

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


def build_tasks(manifests_dir: Path, raw_dir: Path) -> list[dict]:
    tasks: list[dict] = []

    om = json.loads((manifests_dir / "omnidocbench_subset.json").read_text(encoding="utf-8"))
    for r in om["records"]:
        pdf = raw_dir / "omnidocbench" / "pdfs" / (r["image_path"][:-4] + ".pdf")
        for e in ENGINES:
            tasks.append(
                {
                    "layer": "OmniDocBench",
                    "unit_id": r["subset_id"],
                    "engine_id": e,
                    "input": str(pdf),
                    "input_mode": "rendered_page_image",
                }
            )

    ol = json.loads((manifests_dir / "olmocr_subset.json").read_text(encoding="utf-8"))
    for r in ol["records"]:
        pdf = raw_dir / "olmocr" / "pdfs" / r["pdf"]
        for e in ENGINES:
            tasks.append(
                {
                    "layer": "olmOCR-Bench",
                    "unit_id": r["subset_id"],
                    "engine_id": e,
                    "input": str(pdf),
                    "input_mode": "native_pdf",
                }
            )

    oh = json.loads((manifests_dir / "ohr_subset.json").read_text(encoding="utf-8"))
    seen: set[str] = set()
    for r in oh["records"]:
        doc = r["doc_name"].split("/")[-1]
        if doc in seen:
            continue
        seen.add(doc)
        pdf = raw_dir / "ohr" / "pdfs" / (doc + ".pdf")
        for e in ENGINES:
            tasks.append(
                {
                    "layer": "OHR-Bench",
                    "unit_id": f"ohr-doc-{doc}",
                    "engine_id": e,
                    "input": str(pdf),
                    "input_mode": "native_pdf",
                }
            )
    return tasks


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifests-dir", type=Path, required=True)
    ap.add_argument("--raw-dir", type=Path, required=True)
    ap.add_argument("--ledger", type=Path, required=True)
    args = ap.parse_args()

    tasks = build_tasks(args.manifests_dir, args.raw_dir)
    print(f"tasks: {len(tasks)}", file=sys.stderr)

    args.ledger.parent.mkdir(parents=True, exist_ok=True)
    out_dir = args.raw_dir / "outputs"
    out_dir.mkdir(parents=True, exist_ok=True)
    with open(args.ledger, "w", encoding="utf-8") as out:
        for i, t in enumerate(tasks):
            res = invoke(t["engine_id"], t["input"], t["input_mode"])
            # store canonical markdown for runnable engines (raw, gitignored)
            canonical_path = ""
            if res.status == "SUCCESS" and res.markdown:
                rel = f"{t['layer']}/{t['engine_id']}/{t['unit_id']}.md"
                p = out_dir / rel
                p.parent.mkdir(parents=True, exist_ok=True)
                p.write_text(res.markdown, encoding="utf-8")
                canonical_path = str(p)
            rec = {
                "schema_version": "parser-benchmark-result/v1.1",
                "run_id": "pilot-50-each",
                "attempt_id": f"a{i:04d}",
                "benchmark_layer": t["layer"],
                "subset_id": t["unit_id"],
                "evaluation_unit_id": t["unit_id"],
                "engine_id": t["engine_id"],
                "requested_engine": t["engine_id"],
                "effective_engine": res.effective_engine,
                "input_sha256": res.input_sha256,
                "input_mode": t["input_mode"],
                "status": res.status,
                "latency_ms": res.latency_ms,
                "markdown_sha256": res.markdown_sha256,
                "markdown_len": res.markdown_len,
                "canonical_output_path": canonical_path,
                "error_class": res.error_class,
                "error_detail_sanitized": res.error_detail_sanitized,
            }
            out.write(json.dumps(rec, ensure_ascii=False) + "\n")
            if (i + 1) % 50 == 0:
                print(f"  {i + 1}/{len(tasks)}", file=sys.stderr)
    print(f"done -> {args.ledger}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
