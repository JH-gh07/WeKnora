#!/usr/bin/env python
"""Task014 report generator (pilot).

Reads the aggregation JSON + scorer TSVs and writes parser_benchmark_report.md.

Run:
    python3 scripts/tmpCheck/task014/aggregate/make_report.py \
        --stats <statistical_tests.json> \
        --out <evidence/task014/parser_benchmark_report.md>
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

RUNNABLE = ["builtin", "markitdown", "opendataloader"]
UNSUPPORTED = ["anydoc", "weknoracloud", "mineru", "mineru_cloud", "paddleocr_vl"]


def ci(block: dict) -> str:
    if block.get("mean") is None:
        return "n/a"
    return f"{block['mean']:.3f} [{block['lo']:.3f}, {block['hi']:.3f}]"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--stats", type=Path, required=True)
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()
    s = json.loads(args.stats.read_text(encoding="utf-8"))

    L: list[str] = []
    A = L.append
    A("# Task014 Parser Benchmark — Pilot Report (四层各 50 条)")
    A("")
    A("> **状态：PILOT（非权威）。** 本报告是缩小规模的 pilot（四层公开基准各 50 条 + 8 页合成 Holdout），"
      "用于验证 runner/adapters/scorer 闭环，**不构成权威结论、不切换生产默认 Parser**。")
    A("")
    A("## 0. 环境与身份")
    A("")
    A("- WeKnora commit `ac986fd4`；docreader `3842068d`；macOS 14.5 Intel x86_64（无 NVIDIA GPU、无 CUDA）。")
    A("- Go 1.23.6 / Python 3.12.2 / Java 25 / uv 0.11.2。")
    A("- Zhipu key 已配置为 `ZHIPU_API_KEY`，但调用返回 **error 1113（余额不足）**，故 OHR embedding 降级。")
    A("")
    A("## 1. 引擎状态")
    A("")
    A("| engine_id | 状态 | 说明 |")
    A("| --- | --- | --- |")
    for e in RUNNABLE:
        st = s["engines"][e]
        p50 = st["latency_ms_p50"]
        p95 = st["latency_ms_p95"]
        A(f"| {e} | AVAILABLE | success={st['status_counts'].get('SUCCESS',0)}, p50={p50:.0f}ms, p95={p95:.0f}ms |")
    for e in UNSUPPORTED:
        st = s["engines"][e]
        A(f"| {e} | UNSUPPORTED | {st['status_counts'].get('UNSUPPORTED',0)} terminal records（降级，失败计 0） |")
    A("")
    A("> 降级规则：无 NVIDIA GPU 的 `mineru`/`paddleocr_vl`、无凭据的 `weknoracloud`/`mineru_cloud`、无 Rust 构建的 `anydoc` 均按 `UNSUPPORTED` 闭合记录，不静默 fallback、不借用他引擎输出。")
    A("")
    A("## 2. OmniDocBench（50 页，pilot-simplified scorer）")
    A("")
    A("| engine | text 1-CER | block coverage | equation coverage | table coverage |")
    A("| --- | --- | --- | --- | --- |")
    for e in RUNNABLE:
        o = s["omnidocbench"].get(e, {})
        A(f"| {e} | {ci(o.get('text_1_minus_cer',{}))} | {ci(o.get('block_coverage',{}))} | {ci(o.get('equation_coverage',{}))} | {ci(o.get('table_coverage',{}))} |")
    A("")
    A("> **注意：** OmniDocBench 是渲染页图像（image-only PDF），`markitdown` 无 OCR → 输出为空（EMPTY_OUTPUT，按 0 计）。"
      "官方 NED/BLEU/METEOR/TEDS/mAP 评估器未运行（expansion 项），本层为简化 scorer。")
    A("")
    A("## 3. olmOCR-Bench（50 文档断言）")
    A("")
    A("| engine | document pass rate | assertion pass rate | n_documents | n_assertions |")
    A("| --- | --- | --- | --- | --- |")
    for e in RUNNABLE:
        o = s["olmocr_bench"].get(e, {})
        A(f"| {e} | {ci(o.get('document_pass_rate',{}))} | {ci(o.get('assertion_pass_rate',{}))} | {o.get('n_documents','-')} | {o.get('n_assertions','-')} |")
    A("")
    A("## 4. OHR-Bench（50 QA 检索传导，embedding=lexical-tfidf-downgrade）")
    A("")
    A("| engine | Recall@5 | Recall@10 | MRR | nDCG@10 | no-hit rate |")
    A("| --- | --- | --- | --- | --- | --- |")
    for e in RUNNABLE:
        o = s["ohr_bench"].get(e, {})
        A(f"| {e} | {ci(o.get('Recall@5',{}))} | {ci(o.get('Recall@10',{}))} | {ci(o.get('MRR',{}))} | {ci(o.get('nDCG@10',{}))} | {ci(o.get('no_hit_rate',{}))} |")
    A("")
    A("> **降级说明：** Zhipu embedding-3 无余额、Ollama 未运行，故本层用 TF-IDF 词法检索作为 `embedding=lexical-tfidf-downgrade`，"
      "结果仅 `SECONDARY_EXPLORATORY`，不满足 §6.2 冻结 embedding 合同。")
    A("")
    A("## 5. WeKnora Holdout（8 页合成，pilot）")
    A("")
    A("| engine | 平均 text 1-CER |")
    A("| --- | --- |")
    for e in RUNNABLE:
        # holdout numbers injected separately if available
        A(f"| {e} | 见 holdout_results.tsv |")
    A("")
    A("## 6. 结论强度与边界")
    A("")
    A("1. 本报告只适用于**已冻结的 pilot 子集、3 个可运行引擎、无 GPU/无云凭据的降级环境**。")
    A("2. 5 个引擎 `UNSUPPORTED`：结论不能推广到「8 引擎完整对比」，权威运行需 GPU/云凭据 + 官方 evaluator parity。")
    A("3. OmniDocBench/OHR 均为降级 scorer/embedding，非上游官方评估器；不冒充官方完整榜单。")
    A("4. 未产生「跨层唯一总体推荐」（不满足 §8.5 七条件），只输出 per-layer 场景结果。")
    A("5. `production_default_changed=false`：本 pilot 不改变 WeKnora 默认 Parser。")
    A("")
    A("## 7. 产物清单")
    A("")
    A("- `execution_ledger.jsonl`（1,128 条 terminal records）")
    A("- `omnidocbench_results.tsv` / `olmocr_assertion_results.tsv` / `ohr_retrieval_results.tsv` / `holdout_results.tsv`")
    A("- `statistical_tests.json` / `engine_manifest.json` / `source_identity.json` / `benchmark_sources.lock.json`")
    A("- `verify_task014.sh`（一致性门禁）")
    A("")
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text("\n".join(L) + "\n", encoding="utf-8")
    print(f"wrote {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
