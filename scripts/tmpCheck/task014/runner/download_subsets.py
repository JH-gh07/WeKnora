#!/usr/bin/env python
"""Download the sampled Task014 pilot files from HuggingFace.

Reads the subset manifests and downloads only the sampled items (not the full
upstream archives), so no restricted full-dataset mirror is created locally.
OHR-Bench source PDFs live inside a single upstream pdfs.zip and are handled
separately (download_ohr_zip.py).

Run:
    python3 scripts/tmpCheck/task014/runner/download_subsets.py \
        --manifests-dir <evidence/task014/dataset_manifests> \
        --raw-dir <status/raw/task014>
"""

from __future__ import annotations

import argparse
import concurrent.futures as cf
import json
import subprocess
import sys
from pathlib import Path

OMNIDOC_BASE = "https://huggingface.co/datasets/opendatalab/OmniDocBench/resolve/main"
OLMOCR_BASE = "https://huggingface.co/datasets/allenai/olmOCR-bench/resolve/main"


def _dl(url: str, dest: Path) -> bool:
    if dest.exists() and dest.stat().st_size > 0:
        return True
    dest.parent.mkdir(parents=True, exist_ok=True)
    r = subprocess.run(
        ["curl", "-sL", "--fail", "-o", str(dest), url],
        capture_output=True,
    )
    ok = r.returncode == 0 and dest.exists() and dest.stat().st_size > 0
    if not ok and dest.exists():
        dest.unlink()
    return ok


def dl_omnidoc(manifest: Path, raw_dir: Path) -> tuple[int, int]:
    m = json.loads(manifest.read_text(encoding="utf-8"))
    jobs = []
    for r in m["records"]:
        rel = r["image_path"]
        # JSON image_path is a bare basename ("page-UUID.png"); HF stores it
        # under images/.
        url = f"{OMNIDOC_BASE}/images/{rel}"
        dest = raw_dir / "omnidocbench" / "images" / rel
        jobs.append((url, dest))
    ok = 0
    with cf.ThreadPoolExecutor(max_workers=8) as ex:
        for res in ex.map(lambda j: _dl(*j), jobs):
            ok += int(res)
    return ok, len(jobs)


def dl_olmocr(manifest: Path, raw_dir: Path) -> tuple[int, int]:
    m = json.loads(manifest.read_text(encoding="utf-8"))
    jobs = []
    for r in m["records"]:
        rel = r["pdf"]
        url = f"{OLMOCR_BASE}/bench_data/pdfs/{rel}"
        dest = raw_dir / "olmocr" / "pdfs" / rel
        jobs.append((url, dest))
    ok = 0
    with cf.ThreadPoolExecutor(max_workers=8) as ex:
        for res in ex.map(lambda j: _dl(*j), jobs):
            ok += int(res)
    return ok, len(jobs)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifests-dir", type=Path, required=True)
    ap.add_argument("--raw-dir", type=Path, required=True)
    args = ap.parse_args()
    raw = args.raw_dir

    om_ok, om_n = dl_omnidoc(args.manifests_dir / "omnidocbench_subset.json", raw)
    print(f"OmniDocBench images: {om_ok}/{om_n}")

    ol_ok, ol_n = dl_olmocr(args.manifests_dir / "olmocr_subset.json", raw)
    print(f"olmOCR PDFs: {ol_ok}/{ol_n}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
