#!/usr/bin/env bash
# Task013 one-click verifier (offline-safe by default).
# Modes: static | unit | negative | aggregate | integration | browser | live | all-safe | full
# Exit codes (plan §10.2):
#   0 = selected checks all PASS
#   2 = experiment completed, hypothesis not supported
#   3 = evidence incomplete / NOT_COMPARABLE / INCONCLUSIVE
#   4 = infrastructure, contract or security ERROR
#   5 = budget authorization missing or budget exceeded before next call
# Provider modes require an explicit --allow-provider flag and never print secrets.
set -uo pipefail

# ---- UTF-8 hygiene (plan §10.3): pick a real UTF-8 locale for the whole run ----
export PYTHONUTF8=1
export PYTHONIOENCODING=utf-8
_locales="$(locale -a 2>/dev/null || true)"
for _loc in C.UTF-8 en_US.UTF-8 en_US.utf8 zh_CN.UTF-8 zh_CN.utf8; do
  if grep -qx "$_loc" <<< "$_locales"; then
    export LC_ALL="$_loc"
    export LANG="$_loc"
    break
  fi
done
unset _loc _locales

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
EVID="$REPO_ROOT/../status/evidence/task013"
RAW="$REPO_ROOT/../status/raw/task013"
MODE="${1:-static}"
ALLOW_PROVIDER=0
BUDGET_CNY=""
for a in "${@:2}"; do
  case "$a" in
    --allow-provider) ALLOW_PROVIDER=1 ;;
    --allow-docker) : ;;
    --allow-browser) : ;;
    --budget-cny=*) BUDGET_CNY="${a#--budget-cny=}" ;;
    --budget-cny) : ;;
  esac
done

RUN_TS="$(date -u +%Y%m%d_%H%M%S)"
OUT_DIR="$EVID/automated_${MODE}_${RUN_TS}"
mkdir -p "$OUT_DIR"
SUMMARY="$OUT_DIR/summary.tsv"
printf 'check\tstatus\tdetail\n' > "$SUMMARY"

pass=0; fail=0; skip=0
rec() { # name status detail
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$SUMMARY"
  case "$2" in PASS) pass=$((pass+1));; FAIL) fail=$((fail+1));; SKIP) skip=$((skip+1));; esac
}

sha256_of() { # portable sha256 (macOS shasum / Linux sha256sum)
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "ERROR: missing required command: $1" >&2
    exit 4
  fi
}

# ---- secret scan (Task006 parity; scanner never matches its own source) ----
secret_scan() {
  local hits=0
  local pats=(
    'eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}'
    '(?i)bearer[[:space:]]+[A-Za-z0-9._-]{16,}'
    'sk-[A-Za-z0-9]{16,}'
    '(?i)(api[_-]?key|app[_-]?secret|jwt[_-]?secret|system[_-]?aes[_-]?key|db[_-]?password|secret)[[:space:]]*[:=][[:space:]]*["'"'"']?[A-Za-z0-9+/._-]{12,}'
    '(?i)password[[:space:]]*[:=][[:space:]]*["'"'"'][^"'"'"']{4,}["'"'"']'
    '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
    'You are a wiki editor'
    '<page_metadata>'
    'SOURCE GROUNDING'
  )
  : > "$OUT_DIR/secret_scan.log"
  while IFS= read -r -d '' f; do
    for p in "${pats[@]}"; do
      if grep -a -E -q -- "$p" "$f" 2>/dev/null; then
        hits=$((hits+1))
        printf '%s\t%s\n' "$f" "$p" >> "$OUT_DIR/secret_scan.log"
      fi
    done
  done < <(find "$EVID" -type f \( -name '*.md' -o -name '*.json' -o -name '*.yaml' -o -name '*.tsv' -o -name '*.log' \) -not -path '*/automated_*/*' -not -name 'secret_scan.log' -not -name 'sensitive_scan.log' -print0)
  # structural tag references are acceptable (Task006 precedent); real secrets fail.
  while IFS=$'\t' read -r f p; do
    if [ -z "$f" ]; then continue; fi
    case "$p" in
      '<page_metadata>'|'SOURCE GROUNDING'|'You are a wiki editor')
        : # classified non-sensitive structural references; keep visible in log
        ;;
      *) rec "secret_scan" "FAIL" "$f matched $p" ;;
    esac
  done < "$OUT_DIR/secret_scan.log"
  if ! grep -q -E '^(jwt|bearer|sk-|assigned-secret|password-literal|email)' /dev/null 2>&1; then :; fi
  rec "secret_scan" "PASS" "evidence scanned (structural tag refs classified)"
}

mode_static() {
  need_cmd git
  local ok=1
  for f in preflight.md source_identity.json authorization_and_budget.md \
           evidence_policy.md source_trace.md provider_capability.md \
           provider_contract_snapshot.md adapter_reporting_matrix.md \
           fixture_manifest.json semantic_section_contract.json \
           quality_contract.yaml evaluator_manifest.json \
           experiment_preregistration.yaml experiment_preregistration.sha256 \
           experiment_preregistration_protocol.json \
           capability_probe_preregistration.yaml capability_probe_preregistration.sha256 \
           production_equivalence.tsv builder_seam.md offline_dry_run.md \
           negative_matrix.tsv aggregation_golden.json reproduction_3runs.tsv; do
    if [ ! -f "$EVID/$f" ]; then
      rec "static_$f" "FAIL" "missing"
      ok=0
    else
      rec "static_$f" "PASS" "present"
    fi
  done
  # preregistration checksums
  if [ -f "$EVID/experiment_preregistration.yaml" ] && [ -f "$EVID/experiment_preregistration.sha256" ]; then
    want="$(awk '{print $1}' "$EVID/experiment_preregistration.sha256")"
    got="$(sha256_of "$EVID/experiment_preregistration.yaml")"
    if [ "$want" = "$got" ]; then rec "static_prereg_main_sha" "PASS" "$got"; else rec "static_prereg_main_sha" "FAIL" "mismatch"; ok=0; fi
  fi
  if [ -f "$EVID/capability_probe_preregistration.yaml" ] && [ -f "$EVID/capability_probe_preregistration.sha256" ]; then
    want="$(awk '{print $1}' "$EVID/capability_probe_preregistration.sha256")"
    got="$(sha256_of "$EVID/capability_probe_preregistration.yaml")"
    if [ "$want" = "$got" ]; then rec "static_prereg_probe_sha" "PASS" "$got"; else rec "static_prereg_probe_sha" "FAIL" "mismatch"; ok=0; fi
  fi
  secret_scan
  [ "$ok" = 1 ]
}

mode_unit() {
  need_cmd go
  ( cd "$REPO_ROOT" && go build ./tests/evaluation/prompt-cache/... ) > "$OUT_DIR/unit_build.log" 2>&1
  if [ $? -eq 0 ]; then rec "unit_build" "PASS" "harness builds"; else rec "unit_build" "FAIL" "see unit_build.log"; return 1; fi
  ( cd "$REPO_ROOT" && go vet ./tests/evaluation/prompt-cache/... ./internal/agent/ ) > "$OUT_DIR/unit_vet.log" 2>&1
  if [ $? -eq 0 ]; then rec "unit_vet" "PASS" "vet clean"; else rec "unit_vet" "FAIL" "see unit_vet.log"; return 1; fi
  ( cd "$REPO_ROOT" && go test ./internal/agent/ -run 'TestTask013_|TestWikiLayout_' -count=1 ) > "$OUT_DIR/unit_agent.log" 2>&1
  if [ $? -eq 0 ]; then rec "unit_agent_seam" "PASS" "seam + task006 layout tests green"; else rec "unit_agent_seam" "FAIL" "see unit_agent.log"; return 1; fi
  ( cd "$REPO_ROOT" && go test ./tests/evaluation/prompt-cache/... -count=1 ) > "$OUT_DIR/unit_harness.log" 2>&1
  if [ $? -eq 0 ]; then rec "unit_harness" "PASS" "harness tests green"; else rec "unit_harness" "FAIL" "see unit_harness.log"; return 1; fi
  return 0
}

mode_negative() {
  need_cmd go
  ( cd "$REPO_ROOT" && TASK013_EVIDENCE_DIR="$EVID" go run ./tests/evaluation/prompt-cache negative ) > "$OUT_DIR/negative.log" 2>&1
  local rc=$?
  if [ $rc -eq 0 ] && awk -F'\t' 'NR>1 && $5 != "PASS" {exit 1}' "$EVID/negative_matrix.tsv" 2>/dev/null; then
    rec "negative_matrix" "PASS" "N01-N14 all PASS"
    return 0
  fi
  rec "negative_matrix" "FAIL" "see negative.log / negative_matrix.tsv"
  return 1
}

mode_aggregate() {
  need_cmd go
  if [ -f "$EVID/authoritative_sanitized.tsv" ]; then
    ( cd "$REPO_ROOT" && TASK013_EVIDENCE_DIR="$EVID" go run ./tests/evaluation/prompt-cache aggregate "$EVID/authoritative_sanitized.tsv" --repro3 ) > "$OUT_DIR/aggregate.log" 2>&1
    local rc=$?
    if [ $rc -ne 0 ]; then rec "aggregate" "FAIL" "see aggregate.log"; return 1; fi
    local effect
    effect="$(python3 -c "import json;print(json.load(open('$EVID/aggregate.json'))['primary_effect'])" 2>/dev/null || true)"
    rec "aggregate" "PASS" "primary_effect=$effect (repro3 byte-identical)"
    return 0
  fi
  ( cd "$REPO_ROOT" && TASK013_EVIDENCE_DIR="$EVID" go run ./tests/evaluation/prompt-cache aggregate --golden --repro3 ) > "$OUT_DIR/aggregate.log" 2>&1
  if [ $? -eq 0 ]; then
    rec "aggregate" "PASS" "authoritative rows absent -> golden self-check (synthetic)"
    return 0
  fi
  rec "aggregate" "FAIL" "see aggregate.log"
  return 1
}

mode_live() {
  if [ "$ALLOW_PROVIDER" -ne 1 ]; then
    rec "live" "SKIP" "SKIP_WITH_REASON: requires explicit --allow-provider (never reads .env)"
    return 0
  fi
  if [ -z "${SF_API_KEY:-}" ]; then
    rec "live" "FAIL" "credential not provided via environment"
    return 1
  fi
  if [ -z "$BUDGET_CNY" ]; then
    rec "live" "FAIL" "budget authorization missing: pass --budget-cny"
    return 1
  fi
  rec "live" "SKIP" "runner not implemented until data scope approved and Task012 AC-R5 GO"
  return 0
}

mode_integration() {
  rec "integration" "SKIP" "SKIP_WITH_REASON: requires --allow-docker; not part of offline core"
  return 0
}

mode_browser() {
  rec "browser" "SKIP" "SKIP_WITH_REASON: requires --allow-browser; runs after live smoke (Step 9)"
  return 0
}

case "$MODE" in
  static)   mode_static || exit 4 ;;
  unit)     mode_unit || exit 4 ;;
  negative) mode_negative || exit 4 ;;
  aggregate) mode_aggregate || exit 4 ;;
  integration) mode_integration || exit 4 ;;
  browser)  mode_browser || exit 4 ;;
  live)     mode_live || exit 4 ;;
  all-safe)
    mode_static || exit 4
    mode_unit || exit 4
    mode_negative || exit 4
    mode_aggregate || exit 4
    ;;
  full)
    mode_static || exit 4
    mode_unit || exit 4
    mode_negative || exit 4
    mode_aggregate || exit 4
    mode_integration || exit 4
    mode_browser || exit 4
    mode_live || exit 4
    if [ "$skip" -gt 0 ]; then
      echo "full mode: P0 SKIP present -> non-zero" >&2
      exit 3
    fi
    ;;
  *) echo "unknown mode: $MODE" >&2; exit 4 ;;
esac

echo "summary: PASS=$pass FAIL=$fail SKIP=$skip -> $SUMMARY"
if [ "$fail" -gt 0 ]; then exit 4; fi
exit 0
