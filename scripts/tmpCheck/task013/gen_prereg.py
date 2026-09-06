#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Task013 preregistration generator (plan §6.1/§9 Step 2).

Single source of truth for the frozen protocol: emits
  status/evidence/task013/experiment_preregistration.yaml       (human-readable)
  status/evidence/task013/experiment_preregistration_protocol.json (canonical JSON)
  status/evidence/task013/experiment_preregistration.sha256     (SHA-256 of yaml bytes)

protocol_hash (plan §6.1) = SHA-256 over the canonical JSON (sorted keys,
compact separators). Provenance fields (git_commit, timestamps, credential,
raw content) are excluded from the protocol JSON by construction.

Rerunning this script must reproduce byte-identical artifacts; any content
change intentionally creates a NEW protocol (plan §6.3).
"""
import hashlib
import json
import os
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
EVID = os.path.join(ROOT, "..", "status", "evidence", "task013")

PROTOCOL = {
    "protocol_version": "prompt-cache-ab-v1",
    "fixture_id": "t013-cache-abi",
    "fixture_manifest_hash": "25e6f6cb491e663d1907796596c72536fc7e785b21bdb0334636b101c6dcaa29",
    "semantic_section_contract_hash": "8b6043a873e4ee88dd363c1f3955caea8330a6b51f9b7e344f8ff9daeb7a01d5",
    "control_layout_version": "control-v1-variable-first",
    "treatment_layout_version": "treatment-v1-production",
    "production_builder_artifact_hash": "6885a02d85e7251d0c364bbbf18ec4078060cf26960c155726c452ec35113df2",
    "provider": "siliconflow",
    "model_exact": "Qwen/Qwen3-14B",
    "operation": "chat",
    "business_path": "wiki_page_modify",
    "endpoint_family": "openai_compatible",
    "stream_mode": "non_stream_primary",
    "temperature": 0.3,
    "max_tokens": 32768,
    "thinking": False,
    "tool_schema": "none",
    "tool_schema_hash": None,  # computed below: SHA-256 of canonical empty schema "[]"
    "cohort_marker": "wk-task013-ab-v1",
    "sample_count_planned": 12,
    "warmup_calls_per_arm": 1,
    "order_schedule": ("3 blocks x 4 pairs; block1 A-B,B-A,A-B,B-A; "
                       "block2 B-A,A-B,B-A,A-B; block3 A-B,B-A,A-B,B-A (plan §7.2)"),
    "order_schedule_seed": 20260905,
    "inter_call_delay_ms": 2000,
    "inter_block_cooldown_ms": 5000,
    "concurrency": 1,
    "minimum_cacheable_prefix_tokens": "PILOT_DETERMINED",
    "primary_metric": "prompt_cached_token_ratio",
    "minimum_effect_absolute": 0.10,
    "reporting_coverage_required": 1.00,
    "bootstrap_resamples": 10000,
    "bootstrap_seed": 20260905,
    "quality_contract_hash": "c1f75e165f98d1bd94c215313384f43a05ab358a8c9ac1955e865f89385d692f",
    "evaluator_artifact_hash": "a55bbfd097564b6239edece5cc95eef101704067da4c271cf091d70b8a2919f6",
    "pricing_catalog_hash": "046f2666b8fc0ff0cf6aacbaff5030bccaee81d93751a097efd20554ebc5f756",
    "pricing_rule_id": "task012-frozen-2026-09-06",
    "cache_price_dimension": "NO_CACHE_PRICE_TIER",
    "price_effective_window": "2026-09-06T00:00:00Z .. 2026-10-06T00:00:00Z",
    "budget_ceiling_nanos": 10000000000,
    "exclusion_rules": [
        "provider_response_malformed",
        "usage_finality_not_final",
        "cache_telemetry_contract_anomaly",
        "retry_count_asymmetric_between_arms",
        "price_window_differs_and_only_affects_cost",
        "output_truncated_by_max_tokens",
        "request_canceled_by_operator",
    ],
    "stopping_rules": [
        "budget_spent_ge_80pct_and_remaining_may_exceed",
        "full_prompt_or_response_in_evidence",
        "provider_model_identity_drift",
        "price_rule_crosses_effective_boundary",
        "3_consecutive_logical_call_failures",
        "429_5xx_ratio_gt_20pct",
        "cache_telemetry_missing_3_consecutive",
        "ab_section_manifest_mismatch",
        "singleflight_coalesces_provider_bound_calls",
        "fixture_or_evaluator_hash_drift",
    ],
    "minimum_valid_pairs": 10,
    "evidence_level_target": "B",
}

PROTOCOL["tool_schema_hash"] = hashlib.sha256(b"[]").hexdigest()

CANONICAL = json.dumps(PROTOCOL, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
PROTOCOL_HASH = hashlib.sha256(CANONICAL.encode("utf-8")).hexdigest()


def yaml_scalar(v):
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, (int, float)):
        return repr(v)
    return json.dumps(str(v), ensure_ascii=False)


def emit_yaml():
    lines = [
        "# Task013 experiment preregistration (frozen before pilot and primary run)",
        "# Plan §6.1 PromptCacheExperimentSpec. protocol_hash = SHA-256 over the",
        "# canonical JSON companion experiment_preregistration_protocol.json",
        "# (sorted keys, compact separators; provenance excluded per §6.1).",
        "schema: task013/preregistration/1",
        'frozen_at: "2026-09-06T18:30:00Z"',
        "preregistered_before_pilot_and_primary_run: true",
        "",
        "prompt_cache_experiment_protocol:",
    ]
    for k, v in PROTOCOL.items():
        if isinstance(v, list):
            lines.append("  %s:" % k)
            for item in v:
                lines.append("    - %s" % yaml_scalar(item))
        else:
            lines.append("  %s: %s" % (k, yaml_scalar(v)))
    lines += [
        "",
        "provenance_template:",
        "  experiment_id: task013-main-v1",
        "  git_commit: TO_BE_FILLED_AT_RUN",
        "  git_tree: TO_BE_FILLED_AT_RUN",
        "  dirty_patch_hash: TO_BE_FILLED_AT_RUN",
        "  operator: TO_BE_FILLED_AT_RUN",
        "  environment: TO_BE_FILLED_AT_RUN",
        "  reported_model_revision: TO_BE_OBSERVED_PER_CALL",
        "",
        "amendment_rules: plan §6.3 (spelling/artifact-hash supplements ok; sample count, thresholds, model, layout, prefix length or quality changes create a NEW protocol)",
        "",
    ]
    return "\n".join(lines) + "\n"


def main():
    yaml_text = emit_yaml()
    json_text = CANONICAL + "\n"
    yaml_path = os.path.join(EVID, "experiment_preregistration.yaml")
    json_path = os.path.join(EVID, "experiment_preregistration_protocol.json")
    sha_path = os.path.join(EVID, "experiment_preregistration.sha256")
    with open(yaml_path, "w", encoding="utf-8") as f:
        f.write(yaml_text)
    with open(json_path, "w", encoding="utf-8") as f:
        f.write(json_text)
    with open(sha_path, "w", encoding="utf-8") as f:
        f.write(hashlib.sha256(yaml_text.encode("utf-8")).hexdigest() + "\n")
    print("yaml_sha256   =", hashlib.sha256(yaml_text.encode("utf-8")).hexdigest())
    print("protocol_hash =", PROTOCOL_HASH)
    print("wrote:", yaml_path)
    print("wrote:", json_path)
    print("wrote:", sha_path)


if __name__ == "__main__":
    sys.exit(main())
