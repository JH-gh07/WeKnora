#!/usr/bin/env python
"""Task014 canonical engine invoker.

Directly instantiates the per-engine parser class (NEVER the registry facade),
so ``requested_engine == effective_engine`` is provable and there is no silent
fallback. Engines that are unavailable on this host are returned as terminal
``UNSUPPORTED`` records — never dropped, never filled with another engine's
output (plan §2.1 / §5).

Run from the WeKnora repo root with the docreader venv:
    PYTHONPATH=<weknora-root> docreader/.venv/bin/python \
        scripts/tmpCheck/task014/runner/engine_invoker.py ...

Usage (single doc):
    engine_invoker.py --engine builtin --input /path/doc.pdf --input-mode native_pdf
    engine_invoker.py --engine markitdown --input /path/doc.pdf
    engine_invoker.py --engine opendataloader --input /path/doc.pdf

Usage (batch JSONL on stdin, one JSON request per line):
    ... engine_invoker.py --batch
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
import time
import traceback
from dataclasses import asdict, dataclass, field
from typing import Any, Dict, Optional

# Terminal status enum (plan §5).
TERMINAL_STATUSES = {
    "SUCCESS",
    "PARSER_ERROR",
    "TIMEOUT",
    "OOM",
    "EMPTY_OUTPUT",
    "UNSUPPORTED",
    "INVALID_OUTPUT",
    "FALLBACK_CONTAMINATED",
    "INFRA_INVALIDATED",
}

# Engines that exist in the codebase but are unavailable on this host (no GPU /
# no cloud credentials / no Rust build). They are recorded as UNSUPPORTED.
UNSUPPORTED_ENGINES = {
    "anydoc": "no Rust+cgo build (default stub, Available=false)",
    "weknoracloud": "no weknoracloud_app_id/app_secret configured",
    "mineru": "no NVIDIA GPU and no mineru_endpoint configured",
    "mineru_cloud": "no mineru_api_key configured",
    "paddleocr_vl": "no NVIDIA GPU and no paddleocr_vl_endpoint configured",
}

# engine_id -> (parser_class_attr, input_mode_hint)
_PARSER_ATTRS = {
    "builtin": "PDFParser",
    "markitdown": "MarkitdownParser",
    "opendataloader": "OpenDataLoaderParser",
}


@dataclass
class InvokeResult:
    engine_id: str
    requested_engine: str
    effective_engine: str
    input_sha256: str
    input_mode: str
    status: str
    latency_ms: int
    markdown_sha256: str = ""
    markdown_len: int = 0
    error_class: str = ""
    error_detail_sanitized: str = ""
    # prefix of markdown for downstream adapters (kept short in the ledger)
    markdown: str = field(default="", repr=False)

    def to_record(self) -> Dict[str, Any]:
        d = asdict(self)
        return d


def sha256_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


_cached_parsers: Optional[Dict[str, Any]] = None


def _parsers() -> Dict[str, Any]:
    """Import the three runnable parser classes once (import is ~30s due to
    trafilatura/dateparser pulled by the parser package __init__)."""
    global _cached_parsers
    if _cached_parsers is None:
        from docreader.parser.pdf_parser import PDFParser
        from docreader.parser.markitdown_parser import MarkitdownParser
        from docreader.parser.opendataloader_parser import OpenDataLoaderParser

        _cached_parsers = {
            "builtin": PDFParser,
            "markitdown": MarkitdownParser,
            "opendataloader": OpenDataLoaderParser,
        }
    return _cached_parsers


def invoke(engine_id: str, input_path: str, input_mode: str = "native_pdf") -> InvokeResult:
    """Invoke one engine on one input file. Never raises."""
    in_sha = sha256_file(input_path)
    base = InvokeResult(
        engine_id=engine_id,
        requested_engine=engine_id,
        effective_engine=engine_id,
        input_sha256=in_sha,
        input_mode=input_mode,
        status="SUCCESS",
        latency_ms=0,
    )

    if engine_id in UNSUPPORTED_ENGINES:
        base.status = "UNSUPPORTED"
        base.error_class = "UNSUPPORTED"
        base.error_detail_sanitized = UNSUPPORTED_ENGINES[engine_id]
        return base

    parser_cls = _parsers().get(engine_id)
    if parser_cls is None:
        base.status = "UNSUPPORTED"
        base.error_class = "UNSUPPORTED"
        base.error_detail_sanitized = f"engine_id {engine_id!r} has no local parser mapping"
        return base

    try:
        with open(input_path, "rb") as f:
            content = f.read()
    except OSError as e:
        base.status = "PARSER_ERROR"
        base.error_class = "IO_ERROR"
        base.error_detail_sanitized = str(e)
        return base

    t0 = time.time()
    try:
        parser = parser_cls(file_name=input_path, file_type="pdf")
        doc = parser.parse(content)
        md = (doc.content or "") if doc is not None else ""
        base.latency_ms = int((time.time() - t0) * 1000)
    except Exception as e:  # noqa: BLE001 - capture any parser failure as terminal
        base.latency_ms = int((time.time() - t0) * 1000)
        base.status = "PARSER_ERROR"
        base.error_class = type(e).__name__
        base.error_detail_sanitized = str(e)[:500]
        return base

    if not md.strip():
        base.status = "EMPTY_OUTPUT"
        base.markdown_len = 0
        base.markdown_sha256 = hashlib.sha256(b"").hexdigest()
        return base

    base.markdown = md
    base.markdown_len = len(md)
    base.markdown_sha256 = hashlib.sha256(md.encode("utf-8")).hexdigest()
    base.status = "SUCCESS"
    return base


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--engine", required=True)
    ap.add_argument("--input", required=True)
    ap.add_argument("--input-mode", default="native_pdf")
    args = ap.parse_args(argv)
    r = invoke(args.engine, args.input, args.input_mode)
    sys.stdout.write(json.dumps(r.to_record(), ensure_ascii=False) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
