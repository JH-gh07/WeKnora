#!/usr/bin/env python
"""Task014 Holdout builder (pilot, synthetic).

Generates a small set of programmatically-generated PDFs with KNOWN ground-truth
text (single-column, multi-column, table, formula) so text-extraction accuracy
can be scored without manual annotation. This is a PILOT synthetic holdout, NOT
the 40-PDF business-distribution holdout (which needs real authorized PDFs +
double annotation per plan §3.5).

Run (reportlab required):
    python3 scripts/tmpCheck/task014/adapters/holdout/build.py \
        --out <status/raw/task014/holdout>
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from reportlab.lib.pagesizes import A4
from reportlab.lib.units import cm
from reportlab.pdfgen import canvas

DOCS = [
    {
        "id": "W01-single-01",
        "stratum": "W01",
        "title": "Quarterly Review Report",
        "paragraphs": [
            "This is the first paragraph of the quarterly review report. It summarizes overall performance across all business units.",
            "Revenue increased by twelve percent compared with the previous quarter. Operating costs remained stable.",
            "The engineering team delivered the parser benchmark infrastructure ahead of schedule with full test coverage.",
        ],
        "table": None,
        "formula": None,
    },
    {
        "id": "W01-single-02",
        "stratum": "W01",
        "title": "Meeting Minutes",
        "paragraphs": [
            "Attendees agreed to freeze the evaluation protocol before any engine scores are produced.",
            "The budget ceiling remains three hundred CNY unless the owner approves a replacement.",
            "Action items were assigned to the quality and release teams respectively.",
        ],
        "table": None,
        "formula": None,
    },
    {
        "id": "W02-multicol-01",
        "stratum": "W02",
        "title": "Technical Report",
        "paragraphs": [
            "Column one discusses the latency characteristics of the builtin parser on native PDF input.",
            "Column two discusses the accuracy characteristics of the markitdown parser on native PDF input.",
            "A comparative conclusion is deferred until all eight engines are measured under a frozen protocol.",
        ],
        "table": None,
        "formula": None,
    },
    {
        "id": "W05-table-01",
        "stratum": "W05",
        "title": "Cost Summary Table",
        "paragraphs": ["The table below lists known estimated cost per document for each engine."],
        "table": {
            "header": ["Engine", "Cost (CNY)", "Status"],
            "rows": [
                ["builtin", "0.00", "local"],
                ["markitdown", "0.00", "local"],
                ["opendataloader", "0.00", "local"],
                ["mineru_cloud", "unknown", "cloud"],
            ],
        },
        "formula": None,
    },
    {
        "id": "W05-table-02",
        "stratum": "W05",
        "title": "Latency Table",
        "paragraphs": ["Median parse latency in milliseconds per engine."],
        "table": {
            "header": ["Engine", "p50 ms", "p95 ms"],
            "rows": [
                ["builtin", "1278", "3000"],
                ["markitdown", "2292", "5000"],
                ["opendataloader", "5347", "9000"],
            ],
        },
        "formula": None,
    },
    {
        "id": "W06-formula-01",
        "stratum": "W06",
        "title": "Formula Sample",
        "paragraphs": ["The following display equation defines the character error rate."],
        "table": None,
        "formula": "CER = ( S + D + I ) / N",
    },
    {
        "id": "W06-formula-02",
        "stratum": "W06",
        "title": "Formula Sample Two",
        "paragraphs": ["The precision recall harmonic mean is defined below."],
        "table": None,
        "formula": "F1 = 2 P R / ( P + R )",
    },
    {
        "id": "W08-long-01",
        "stratum": "W08",
        "title": "Long Mixed Document",
        "paragraphs": [
            "This long document begins with an executive summary and continues for several pages.",
            "The parser benchmark evaluates text, layout, table, and formula quality across engines.",
            "A footer on each page records the document identifier for traceability.",
        ],
        "table": None,
        "formula": None,
    },
]


def build_pdf(doc: dict, path: Path) -> str:
    c = canvas.Canvas(str(path), pagesize=A4)
    w, h = A4
    y = h - 2 * cm
    c.setFont("Helvetica-Bold", 16)
    c.drawString(2 * cm, y, doc["title"])
    y -= 1.2 * cm
    c.setFont("Helvetica", 11)
    for p in doc["paragraphs"]:
        # simple word wrap
        words = p.split()
        line = ""
        for wd in words:
            if c.stringWidth(line + " " + wd, "Helvetica", 11) > w - 4 * cm:
                c.drawString(2 * cm, y, line)
                y -= 0.55 * cm
                line = wd
            else:
                line = (line + " " + wd).strip()
        if line:
            c.drawString(2 * cm, y, line)
            y -= 0.55 * cm
        y -= 0.25 * cm
    if doc["table"]:
        y -= 0.5 * cm
        x = 2 * cm
        c.setFont("Helvetica-Bold", 10)
        col_w = (w - 4 * cm) / len(doc["table"]["header"])
        for j, hd in enumerate(doc["table"]["header"]):
            c.drawString(x + j * col_w, y, hd)
        y -= 0.5 * cm
        c.setFont("Helvetica", 10)
        for row in doc["table"]["rows"]:
            for j, cell in enumerate(row):
                c.drawString(x + j * col_w, y, cell)
            y -= 0.5 * cm
    if doc["formula"]:
        y -= 0.7 * cm
        c.setFont("Helvetica-Oblique", 12)
        c.drawString(2 * cm, y, doc["formula"])
    c.showPage()
    c.save()
    # ground-truth text = title + paragraphs + table cells + formula
    gt_parts = [doc["title"]] + list(doc["paragraphs"])
    if doc["table"]:
        gt_parts += list(doc["table"]["header"])
        for row in doc["table"]["rows"]:
            gt_parts += list(row)
    if doc["formula"]:
        gt_parts.append(doc["formula"])
    return " ".join(gt_parts)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    records = []
    for d in DOCS:
        pdf = args.out / f"{d['id']}.pdf"
        gt_text = build_pdf(d, pdf)
        records.append(
            {
                "subset_id": d["id"],
                "stratum": d["stratum"],
                "pdf": pdf.name,
                "gt_text": gt_text,
            }
        )
    manifest = {"layer": "WeKnoraHoldout", "n": len(records), "records": records}
    (args.out / "holdout_manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    print(f"built {len(records)} synthetic holdout PDFs -> {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
