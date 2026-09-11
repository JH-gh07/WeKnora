#!/usr/bin/env python
"""Task014 olmOCR-Bench assertion scorer (pilot).

Machine-verifiable assertion pass/fail per document, per engine, per category.
Normalization: collapse whitespace, optional case fold. ``max_diffs`` is honored
with a bounded fuzzy substring check (difflib). This is a pilot scorer; the
official olmOCR-bench evaluator is the authority for any full-scale run.

Run (stdlib only):
    python3 scripts/tmpCheck/task014/adapters/olmocr_bench/score.py \
        --manifest <olmocr_subset.json> \
        --jsonl-dir <olmocr/jsonl> \
        --outputs-dir <status/raw/task014/outputs/olmOCR-Bench> \
        --engines builtin,markitdown,opendataloader \
        --out <evidence/task014/olmocr_assertion_results.tsv>
"""

from __future__ import annotations

import argparse
import difflib
import json
import re
from pathlib import Path

CATEGORY_BY_FILE = {
    "arxiv_math": "math",
    "headers_footers": "headers_footers",
    "long_tiny_text": "long_tiny_text",
    "multi_column": "multi_column",
    "old_scans": "old_scans",
    "old_scans_math": "old_scans_math",
}

_WS = re.compile(r"\s+")


def norm(s: str) -> str:
    return _WS.sub(" ", s).strip()


def _fuzzy_substring(needle: str, haystack: str, max_diffs: int) -> bool:
    if not needle:
        return True
    if needle in haystack:
        return True
    if max_diffs <= 0:
        return False
    n = len(needle)
    # anchor on the first 6 chars to bound the fuzzy window search
    anchor = needle[:6]
    if not anchor:
        return False
    start = 0
    while True:
        pos = haystack.find(anchor, start)
        if pos == -1:
            break
        for off in range(max(0, pos - max_diffs), min(len(haystack) - n + 1, pos + max_diffs + 1)):
            window = haystack[off : off + n]
            if difflib.SequenceMatcher(None, needle, window).ratio() >= (n - max_diffs) / max(n, 1):
                return True
        start = pos + 1
    return False


def check(assertion: dict, md_norm: str) -> bool:
    typ = assertion.get("type")
    if typ == "order":
        before = norm(assertion.get("before", ""))
        after = norm(assertion.get("after", ""))
        bi = md_norm.find(before)
        ai = md_norm.find(after)
        return before in md_norm and after in md_norm and bi < ai
    if typ == "math" or "math" in assertion:
        expected = norm(assertion.get("math", ""))
        return _fuzzy_substring(expected, md_norm, int(assertion.get("max_diffs", 0)))
    # text present / absent
    expected = norm(assertion.get("text", ""))
    hay = md_norm
    if not assertion.get("case_sensitive", True):
        hay = md_norm.lower()
        expected = expected.lower()
    present = _fuzzy_substring(expected, hay, int(assertion.get("max_diffs", 0)))
    return present if typ != "absent" else (not present)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifest", type=Path, required=True)
    ap.add_argument("--jsonl-dir", type=Path, required=True)
    ap.add_argument("--outputs-dir", type=Path, required=True)
    ap.add_argument("--engines", required=True)
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()
    engines = [e.strip() for e in args.engines.split(",") if e.strip()]

    man = json.loads(args.manifest.read_text(encoding="utf-8"))
    doc_to_manifest = {r["pdf"]: r for r in man["records"]}

    # load all assertions, grouped by pdf
    assertions_by_pdf: dict[str, list[dict]] = {}
    for fname, cat in CATEGORY_BY_FILE.items():
        p = args.jsonl_dir / f"{fname}.jsonl"
        if not p.exists():
            continue
        for line in p.read_text(encoding="utf-8").splitlines():
            if not line.strip():
                continue
            obj = json.loads(line)
            obj["category"] = cat
            assertions_by_pdf.setdefault(obj["pdf"], []).append(obj)

    rows: list[str] = []
    header = "engine\tcategory\tdocument\tassertion_id\ttype\tpass\tfail_reason"
    rows.append(header)
    for eng in engines:
        # cache normalized markdown per document
        md_cache: dict[str, str] = {}
        for pdf, astrs in assertions_by_pdf.items():
            if pdf not in doc_to_manifest:
                continue
            md_file = args.outputs_dir / eng / f"{doc_to_manifest[pdf]['subset_id']}.md"
            md_cache[pdf] = norm(md_file.read_text(encoding="utf-8")) if md_file.exists() else ""
        for pdf, astrs in assertions_by_pdf.items():
            if pdf not in doc_to_manifest:
                continue
            md = md_cache[pdf]
            for a in astrs:
                ok = check(a, md)
                reason = ""
                if not ok:
                    reason = "absent" if a.get("type") == "absent" else "mismatch"
                rows.append(
                    "\t".join(
                        [
                            eng,
                            a["category"],
                            pdf,
                            a["id"],
                            a.get("type", "present"),
                            "1" if ok else "0",
                            reason,
                        ]
                    )
                )
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text("\n".join(rows) + "\n", encoding="utf-8")
    print(f"wrote {len(rows) - 1} assertion rows -> {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
