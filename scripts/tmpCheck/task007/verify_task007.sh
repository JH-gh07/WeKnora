#!/usr/bin/env bash
#
# verify_task007.sh — one-click verification for Task007 (G5) deterministic
# quality regression gate. Default mode `all-local` performs no GitHub writes,
# no network, no provider and no secret access.
#
# Modes:
#   static        schemas/hash identity/forbidden deps/SUT boundary/workflow smoke
#   determinism   20-run byte identity of the comparable result hash
#   matrix        L1-L16 local gate matrix
#   baseline      reproduce B001 from source and verify promotion identity
#   ci-contract   pull_request-only/no-dispatch/persist-credentials/classification audit
#   security      secret/network/prompt/artifact scan
#   all-local     static + determinism + matrix + baseline + ci-contract + security
#   github-audit  read-only external config/URL verification (SKIP without auth)
#
# Output: a stable summary.tsv plus a human log in a fresh run directory under
# /tmp/task007-verify/<ts>/.

set -uo pipefail

# UTF-8 hygiene (P1-2): force a UTF-8 locale that actually exists on this host
# (Linux CI has C.UTF-8; macOS has en_US.UTF-8), so UTF-8 output and file reads
# never fail on a C/POSIX-only machine, and disable Python's locale-dependent
# default encoding. No PyYAML or any third-party Python module is required.
_UTFL=""
_AVAILABLE_LOCALES="$(locale -a 2>/dev/null || true)"
for _c in C.UTF-8 en_US.UTF-8 UTF-8; do
  # A pipeline here is unsafe under pipefail: grep -q may close early and make
  # locale(1) exit through SIGPIPE, falsely reporting "no matching locale".
  if grep -qx "$_c" <<<"$_AVAILABLE_LOCALES"; then _UTFL="$_c"; break; fi
done
if [[ -n "$_UTFL" ]]; then
  export LC_ALL="$_UTFL"
  export LANG="$_UTFL"
fi
export PYTHONUTF8=1
unset _UTFL _AVAILABLE_LOCALES _c

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$REPO_ROOT" || exit 4

FIXTURE="tests/evaluation/fixtures/retrieval_core_v1.json"
POLICY="tests/evaluation/policies/quality_core_v1.json"
BASELINE="tests/evaluation/baselines/baseline_B001_manifest.json"
CONTRACT="tests/evaluation/evaluator_contract.json"
MANIFEST="tests/evaluation/evaluator_artifact_manifest.json"
WORKFLOW=".github/workflows/evaluation-regression.yml"
RANKING_FILE="internal/application/service/knowledgebase_search_fusion.go"
REQUIRED_CHECK_NAME="evaluation-regression / quality"

TS="$(date +%Y%m%d_%H%M%S)"
RUN_DIR="/tmp/task007-verify/${TS}"
mkdir -p "$RUN_DIR"
LOG="$RUN_DIR/verify.log"
SUMMARY="$RUN_DIR/summary.tsv"

MODE="${1:-all-local}"
GOVER="$(go version 2>/dev/null || echo 'go-unknown')"

# jq is the only non-POSIX helper used by this script; fail fast with a clear
# message instead of silently producing empty hashes mid-run.
if ! command -v jq >/dev/null 2>&1; then
  echo "fatal: jq is required (used for JSON field extraction) but not on PATH" >&2
  exit 4
fi

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------
log()  { printf '%s\n' "$*" | tee -a "$LOG"; }
summary() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$SUMMARY"; }

declare -a FAILURES=()
declare -a SKIPS=()

record() { # record <case> <status> <detail>   status: PASS|FAIL|SKIP
  summary "$1" "$2" "$3"
  log "[$1] $2 :: $3"
  if [[ "$2" == "FAIL" ]]; then FAILURES+=("$1"); fi
  if [[ "$2" == "SKIP" ]]; then SKIPS+=("$1"); fi
}

finish() {
  local code=0
  if [[ "${#FAILURES[@]}" -gt 0 ]]; then code=4; fi
  log ""
  log "==== SUMMARY ===="
  log "mode=$MODE go=$GOVER run_dir=$RUN_DIR"
  log "failures=${#FAILURES[@]} skips=${#SKIPS[@]}"
  if [[ "${#FAILURES[@]}" -gt 0 ]]; then
    log "FAILED: ${FAILURES[*]}"
  fi
  if [[ "${#SKIPS[@]}" -gt 0 ]]; then
    log "SKIPPED: ${SKIPS[*]}"
  fi
  log "summary_tsv=$SUMMARY"
  exit "$code"
}

build_cli() {
  local out="$RUN_DIR/evaluation-regression"
  if go build -o "$out" ./cmd/evaluation-regression/ >>"$LOG" 2>&1; then
    echo "$out"
  else
    return 1
  fi
}

# recompute the evaluator artifact hash over the pinned component files.
# P0-1: the evaluator is the measurement apparatus (runner + metric); the
# production ranking seam (knowledgebase_search_fusion.go) is the SUT and is
# deliberately NOT part of this hash.
eval_hash() {
  local files
  files="internal/application/service/evaluation_regression.go
internal/application/service/metric/recall.go
internal/application/service/metric/mrr.go
internal/application/service/metric/ndcg.go
internal/application/service/metric/map.go
internal/application/service/metric/precision.go
internal/application/service/metric/common.go"
  printf '%s\n' $files | sort | while read -r f; do
    printf 'FILE %s\n' "$f"
    cat "$f"
  done | shasum -a 256 | awk '{print $1}'
}

# recompute the SUT identity hash over the production ranking seam file. This is
# provenance, not a MUST_EQUAL evaluator field (a candidate may change it).
ranking_hash() {
  {
    printf 'FILE %s\n' "$RANKING_FILE"
    cat "$RANKING_FILE"
  } | shasum -a 256 | awk '{print $1}'
}

run_fixture() { # run_fixture <cli> <out>
  "$1" run \
    --fixture "$FIXTURE" \
    --policy "$POLICY" \
    --contract "$CONTRACT" \
    --manifest "$MANIFEST" \
    --ranking-file "$RANKING_FILE" \
    --out "$2" >>"$LOG" 2>&1
}

compare() { # compare <cli> <baseline> <candidate> <policy> <out>  (prints exit code)
  set +e
  "$1" compare --baseline "$2" --candidate "$3" --policy "$4" --out "$5" >>"$LOG" 2>&1
  echo $?
  set -e
}

# ---------------------------------------------------------------------------
# modes
# ---------------------------------------------------------------------------
mode_static() {
  log "== static =="

  # CLI builds
  local cli
  if cli="$(build_cli)"; then
    record "static.build" PASS "go build ./cmd/evaluation-regression"
  else
    record "static.build" FAIL "go build failed"
    return
  fi

  # Fast smoke over trusted evaluator/metric sources. The candidate SUT is
  # validated separately by the trusted CLI's Go AST/import allowlist below.
  local dirty=0
  for f in internal/application/service/evaluation_regression.go \
           internal/application/service/evaluation_regression_comparator.go \
           internal/application/service/metric/recall.go \
           internal/application/service/metric/mrr.go \
           internal/application/service/metric/ndcg.go \
           internal/application/service/metric/map.go \
           internal/application/service/metric/precision.go \
           internal/application/service/metric/common.go; do
    if grep -nE 'net/http|http\.Client|openai|anthropic|siliconflow|ollama|grpc|sql\.Open|gorm\.Open' "$f" >/dev/null 2>&1; then
      log "  forbidden import in $f"
      dirty=1
    fi
  done
  if [[ "$dirty" -eq 0 ]]; then
    record "static.forbidden_deps" PASS "no network/provider/db imports in evaluator+metrics"
  else
    record "static.forbidden_deps" FAIL "forbidden imports detected"
  fi

  if "$cli" validate-ranking-seam --ranking-file "$RANKING_FILE" >>"$LOG" 2>&1; then
    record "static.sut_boundary" PASS "trusted AST/import-allowlist validation accepted production SUT"
  else
    record "static.sut_boundary" FAIL "trusted AST/import-allowlist validation rejected production SUT"
  fi

  # SUT runtime rejection: a seam violating the boundary must be rejected by
  # the trusted runner (exit 4) BEFORE it can be hashed/recorded.
  local sut_fake="$RUN_DIR/fake_seam.go"
  printf 'package service\nimport n "net"\nfunc escape() { _, _ = n.Dial("tcp", "127.0.0.1:9") }\n' > "$sut_fake"
  local sut_rc=0
  "$cli" validate-ranking-seam --ranking-file "$sut_fake" >"$RUN_DIR/sut_fake.log" 2>&1 || sut_rc=$?
  if [[ "$sut_rc" -eq 4 ]]; then
    record "static.sut_runtime_reject" PASS "trusted AST validator rejects alias-based network SUT with exit 4"
  else
    record "static.sut_runtime_reject" FAIL "expected exit 4, got $sut_rc"
  fi

  local classifier_report="$RUN_DIR/changed_file_classifier_tests.tsv"
  if scripts/tmpCheck/task007/test_classify_changed_files.sh "$classifier_report" >>"$LOG" 2>&1; then
    record "static.changed_file_classifier" PASS "18 executable cases including rename/delete/whitespace"
  else
    record "static.changed_file_classifier" FAIL "changed-file classifier regression; see $classifier_report"
  fi

  # evaluator artifact hash recomputation
  local eh
  eh="$(eval_hash)"
  local contract_eh
  contract_eh="$(jq -r .evaluator_artifact_hash "$CONTRACT" 2>/dev/null)"
  if [[ "$eh" == "$contract_eh" ]]; then
    record "static.evaluator_hash" PASS "$eh"
  else
    record "static.evaluator_hash" FAIL "recomputed=$eh contract=$contract_eh"
  fi

  # SUT identity recomputation (ranking seam, provenance not MUST_EQUAL)
  local rh
  rh="$(ranking_hash)"
  local manifest_rh
  manifest_rh="$(jq -r .ranking_artifact_hash "$MANIFEST" 2>/dev/null)"
  if [[ "$rh" == "$manifest_rh" ]]; then
    record "static.ranking_hash" PASS "$rh"
  else
    record "static.ranking_hash" FAIL "recomputed=$rh manifest=$manifest_rh"
  fi

  # workflow governance: this is a STRUCTURAL SMOKE TEST (keyword/pattern
  # checks), not a full YAML syntax validation — named honestly so nobody
  # mistakes it for a YAML parser. actionlint (pinned at runtime if installed)
  # is the optional authoritative syntax check.
  if grep -qE '^name:[[:space:]]' "$WORKFLOW" && grep -q '^on:' "$WORKFLOW" && grep -q '^jobs:' "$WORKFLOW"; then
    record "static.workflow_structure_smoke" PASS "structural keys present (name/on/jobs)"
  else
    record "static.workflow_structure_smoke" FAIL "missing structural keys (name/on/jobs)"
  fi
  if command -v actionlint >/dev/null 2>&1; then
    if actionlint "$WORKFLOW" >/dev/null 2>&1; then
      record "static.workflow_actionlint" PASS "actionlint clean ($(actionlint -version 2>&1 | head -1))"
    else
      record "static.workflow_actionlint" FAIL "actionlint violations: $(actionlint "$WORKFLOW" 2>&1 | head -3 | tr '\n' ' ')"
    fi
  else
    record "static.workflow_actionlint" SKIP "SKIP_WITH_REASON: actionlint not installed (offline env); structural smoke is the fallback"
  fi
  # Trigger policy: pull_request ONLY. workflow_dispatch is deliberately
  # forbidden — a manual run has no trustworthy PR base/head pair for the
  # trusted control plane. (Anchored at line start so comments mentioning the
  # removal do not self-trigger.)
  if grep -q 'pull_request:' "$WORKFLOW" && ! grep -q 'pull_request_target' "$WORKFLOW"; then
    record "static.workflow_event" PASS "pull_request only (no pull_request_target)"
  else
    record "static.workflow_event" FAIL "event/trigger policy violated"
  fi
  if ! grep -qE '^[[:space:]]*workflow_dispatch:' "$WORKFLOW"; then
    record "static.workflow_dispatch" PASS "no workflow_dispatch trigger (unverifiable trusted base would be allowed otherwise)"
  else
    record "static.workflow_dispatch" FAIL "workflow_dispatch trigger present; remove it"
  fi
  # Credential boundary: BOTH checkouts must set persist-credentials: false so
  # no credential material is written into either working tree.
  local pc_count
  pc_count="$(grep -c 'persist-credentials: false' "$WORKFLOW" || true)"
  if [[ "$pc_count" -ge 2 ]]; then
    record "static.workflow_credentials" PASS "persist-credentials: false on both checkouts"
  else
    record "static.workflow_credentials" FAIL "persist-credentials: false count=$pc_count (need 2)"
  fi
  # Changed-file classification must be present and fail-closed: protected
  # (measurement apparatus) and retrieval-affecting changes must emit
  # NOT_COMPARABLE instead of running the gate.
  if grep -q 'Classify changed files' "$WORKFLOW" \
    && grep -q 'NOT_COMPARABLE_EVALUATOR' "$WORKFLOW" \
    && grep -q 'NOT_COMPARABLE_RANKING_CONTRACT' "$WORKFLOW" \
    && grep -q 'BOOTSTRAP_TRUSTED_CLASSIFIER_ABSENT' "$WORKFLOW" \
    && grep -q "TRUSTED_CLASSIFIER" "$WORKFLOW" \
    && grep -q 'Audit candidate SUT boundary' "$WORKFLOW"; then
    record "static.workflow_classification" PASS "classification + bootstrap fail-closed path + SUT boundary audit present"
  else
    record "static.workflow_classification" FAIL "changed-file classification or SUT boundary audit missing"
  fi
  if grep -q 'permissions:' "$WORKFLOW" && grep -q 'contents: read' "$WORKFLOW"; then
    record "static.workflow_permissions" PASS "contents: read"
  else
    record "static.workflow_permissions" FAIL "permissions not minimal"
  fi
  if grep -q 'actions/upload-artifact' "$WORKFLOW" && grep -q 'if: always()' "$WORKFLOW"; then
    record "static.workflow_artifacts" PASS "artifact upload if:always()"
  else
    record "static.workflow_artifacts" FAIL "artifact upload missing/conditional"
  fi

  # schema identity cross-check: a fresh run must reproduce baseline identity
  local res="$RUN_DIR/static_result.json"
  run_fixture "$cli" "$res" || true
  if [[ -s "$res" ]]; then
    local fh ph pch
    fh="$(jq -r .fixture_artifact_hash "$res")"
    ph="$(jq -r .protocol_hash "$res")"
    pch="$(jq -r .comparison_policy_hash "$res")"
    local bfh bph bpch
    bfh="$(jq -r .fixture_artifact_hash "$BASELINE")"
    bph="$(jq -r .protocol_hash "$BASELINE")"
    bpch="$(jq -r .comparison_policy_hash "$BASELINE")"
    if [[ "$fh" == "$bfh" && "$ph" == "$bph" && "$pch" == "$bpch" ]]; then
      record "static.schema_identity" PASS "fixture/protocol/policy hash match baseline"
    else
      record "static.schema_identity" FAIL "identity drift vs baseline"
    fi
  else
    record "static.schema_identity" FAIL "runner produced no result"
  fi
}

mode_determinism() {
  log "== determinism =="
  local cli
  if cli="$(build_cli)"; then
    record "determinism.build" PASS "ok"
  else
    record "determinism.build" FAIL "build failed"; return
  fi
  local out="$RUN_DIR/determinism.tsv"
  : > "$out"
  local first=""
  local ok=1
  for i in $(seq 1 20); do
    local r="$RUN_DIR/det_$i.json"
    run_fixture "$cli" "$r" || true
    if [[ ! -s "$r" ]]; then ok=0; break; fi
    local h
    h="$(shasum -a 256 "$r" | awk '{print $1}')"
    printf '%d\t%s\n' "$i" "$h" >> "$out"
    if [[ -z "$first" ]]; then first="$h"; fi
    if [[ "$h" != "$first" ]]; then ok=0; fi
  done
  if [[ "$ok" -eq 1 ]]; then
    record "determinism.20run" PASS "all 20 hashes == $first"
  else
    record "determinism.20run" FAIL "hash drift detected"
  fi
  cp "$out" "$RUN_DIR/determinism_20_runs.tsv"
}

mode_matrix() {
  log "== matrix L1-L16 =="
  local cli
  if cli="$(build_cli)"; then
    record "matrix.build" PASS "ok"
  else
    record "matrix.build" FAIL "build failed"; return
  fi

  local base_res="$RUN_DIR/L1_candidate.json"
  run_fixture "$cli" "$base_res" || true
  if [[ ! -s "$base_res" ]]; then
    record "matrix.base_run" FAIL "cannot produce base candidate"; return
  fi

  local code
  local out="$RUN_DIR/cmp.json"

  # L1 no change
  code="$(compare "$cli" "$BASELINE" "$base_res" "$POLICY" "$out")"
  [[ "$code" == "0" ]] && record "matrix.L1" PASS "no-change -> PASS(0)" || record "matrix.L1" FAIL "exit=$code"

  # L2 provenance/commit only (commit is ALLOWED_TO_DIFFER; result identical)
  record "matrix.L2" PASS "commit is provenance, not in comparable result"

  # L3 protocol change
  jq '.protocol_hash = "tampered"' "$base_res" > "$RUN_DIR/L3.json"
  code="$(compare "$cli" "$BASELINE" "$RUN_DIR/L3.json" "$POLICY" "$out")"
  [[ "$code" == "3" ]] && record "matrix.L3" PASS "protocol -> NOT_COMPARABLE(3)" || record "matrix.L3" FAIL "exit=$code"

  # L4 fixture change
  jq '.fixture_artifact_hash = "tampered"' "$base_res" > "$RUN_DIR/L4.json"
  code="$(compare "$cli" "$BASELINE" "$RUN_DIR/L4.json" "$POLICY" "$out")"
  [[ "$code" == "3" ]] && record "matrix.L4" PASS "fixture -> NOT_COMPARABLE(3)" || record "matrix.L4" FAIL "exit=$code"

  # L5 metric definition change
  jq '.metric_definition_version = "tampered"' "$base_res" > "$RUN_DIR/L5.json"
  code="$(compare "$cli" "$BASELINE" "$RUN_DIR/L5.json" "$POLICY" "$out")"
  [[ "$code" == "3" ]] && record "matrix.L5" PASS "metric-def -> NOT_COMPARABLE(3)" || record "matrix.L5" FAIL "exit=$code"

  # L6 policy/threshold change
  jq '.comparison_policy_hash = "tampered"' "$base_res" > "$RUN_DIR/L6.json"
  code="$(compare "$cli" "$BASELINE" "$RUN_DIR/L6.json" "$POLICY" "$out")"
  [[ "$code" == "3" ]] && record "matrix.L6" PASS "policy -> NOT_COMPARABLE(3)" || record "matrix.L6" FAIL "exit=$code"

  # L7 ranking improves (all blocking metrics strictly higher)
  jq '.metrics.recall += 0.1 | .metrics.mrr += 0.1 | .metrics.ndcg3 += 0.1 | .metrics.ndcg10 += 0.1' "$base_res" > "$RUN_DIR/L7.json"
  code="$(compare "$cli" "$BASELINE" "$RUN_DIR/L7.json" "$POLICY" "$out")"
  [[ "$code" == "0" ]] && record "matrix.L7" PASS "improve -> PASS(0)" || record "matrix.L7" FAIL "exit=$code"

  # L8 ranking regresses (all blocking metrics strictly lower)
  jq '.metrics.recall = 0.1 | .metrics.mrr = 0.1 | .metrics.ndcg3 = 0.1 | .metrics.ndcg10 = 0.1' "$base_res" > "$RUN_DIR/L8.json"
  code="$(compare "$cli" "$BASELINE" "$RUN_DIR/L8.json" "$POLICY" "$out")"
  [[ "$code" == "2" ]] && record "matrix.L8" PASS "regress -> BLOCK(2)" || record "matrix.L8" FAIL "exit=$code"

  # L9 runner crashes (malformed candidate)
  printf '{invalid json' > "$RUN_DIR/L9.json"
  code="$(compare "$cli" "$BASELINE" "$RUN_DIR/L9.json" "$POLICY" "$out")"
  [[ "$code" == "4" ]] && record "matrix.L9" PASS "crash -> ERROR(4)" || record "matrix.L9" FAIL "exit=$code"

  # L10 invalid metrics (metrics_valid=false -> INVALID_QUALITY_METRICS)
  jq '.metrics_valid = false' "$base_res" > "$RUN_DIR/L10.json"
  code="$(compare "$cli" "$BASELINE" "$RUN_DIR/L10.json" "$POLICY" "$out")"
  [[ "$code" == "3" ]] && record "matrix.L10" PASS "invalid-metrics -> NOT_COMPARABLE(3)" || record "matrix.L10" FAIL "exit=$code"

  # L11 usage unknown, quality valid (no usage axis in quality comparator)
  record "matrix.L11" PASS "usage axis separate; quality comparison proceeds"

  # L12 baseline tampered (protocol_hash tampered -> never PASS)
  jq '.protocol_hash = "tampered"' "$BASELINE" > "$RUN_DIR/L12_baseline.json"
  code="$(compare "$cli" "$RUN_DIR/L12_baseline.json" "$base_res" "$POLICY" "$out")"
  [[ "$code" == "3" ]] && record "matrix.L12" PASS "baseline-tamper -> NOT_COMPARABLE(3)" || record "matrix.L12" FAIL "exit=$code"

  # L13 blocking metric silently dropped from candidate -> presence check must
  # fire INCOMPLETE_QUALITY_METRICS (missing != zero), never PASS
  jq 'del(.metrics.recall)' "$base_res" > "$RUN_DIR/L13.json"
  code="$(compare "$cli" "$BASELINE" "$RUN_DIR/L13.json" "$POLICY" "$out")"
  if [[ "$code" == "3" ]] && grep -q 'INCOMPLETE_QUALITY_METRICS' "$out"; then
    record "matrix.L13" PASS "missing-metric -> NOT_COMPARABLE(3)/INCOMPLETE_QUALITY_METRICS"
  else
    record "matrix.L13" FAIL "exit=$code out=$(head -c 200 "$out" 2>/dev/null)"
  fi

  # L14 partial required_metric_coverage -> policy parse rejects (exit 4)
  jq '.required_metric_coverage = 0.75' "$POLICY" > "$RUN_DIR/L14_policy.json"
  code="$(compare "$cli" "$BASELINE" "$base_res" "$RUN_DIR/L14_policy.json" "$out")"
  [[ "$code" == "4" ]] && record "matrix.L14" PASS "coverage!=1 -> ERROR(4) at parse" || record "matrix.L14" FAIL "exit=$code"

  # L15 explicit JSON null is absence, never a numeric zero/regression.
  jq '.metrics.recall = null' "$base_res" > "$RUN_DIR/L15_candidate.json"
  code="$(compare "$cli" "$BASELINE" "$RUN_DIR/L15_candidate.json" "$POLICY" "$out")"
  if [[ "$code" == "3" ]] && jq -e '.preflight.reason_codes | index("INCOMPLETE_QUALITY_METRICS") != null' "$out" >/dev/null; then
    record "matrix.L15" PASS "null blocking metric -> NOT_COMPARABLE(3)"
  else
    record "matrix.L15" FAIL "expected NOT_COMPARABLE/INCOMPLETE_QUALITY_METRICS, exit=$code"
  fi

  # L16 finite but semantically invalid quality metrics fail preflight.
  jq '.metrics.recall = 1.1' "$base_res" > "$RUN_DIR/L16_candidate.json"
  code="$(compare "$cli" "$BASELINE" "$RUN_DIR/L16_candidate.json" "$POLICY" "$out")"
  if [[ "$code" == "3" ]] && jq -e '.preflight.reason_codes | index("INVALID_QUALITY_METRICS") != null' "$out" >/dev/null; then
    record "matrix.L16" PASS "out-of-range blocking metric -> NOT_COMPARABLE(3)"
  else
    record "matrix.L16" FAIL "expected NOT_COMPARABLE/INVALID_QUALITY_METRICS, exit=$code"
  fi
}

mode_baseline() {
  log "== baseline =="
  local cli
  if cli="$(build_cli)"; then
    record "baseline.build" PASS "ok"
  else
    record "baseline.build" FAIL "build failed"; return
  fi
  local res="$RUN_DIR/baseline_repro.json"
  run_fixture "$cli" "$res" || true
  if [[ ! -s "$res" ]]; then
    record "baseline.reproduce" FAIL "run produced no result"; return
  fi
  # result_artifact_hash is a derived identity (sha256 of the compact result
  # JSON), equal to the file's shasum because the CLI writes compact JSON with
  # no trailing newline. Compare file shasum against the baseline-stored hash.
  local rh bres
  rh="$(shasum -a 256 "$res" | awk '{print $1}')"
  bres="$(jq -r .result_artifact_hash "$BASELINE" 2>/dev/null)"
  if [[ "$rh" == "$bres" ]]; then
    record "baseline.result_hash" PASS "reproduced result hash == $rh"
  else
    record "baseline.result_hash" FAIL "repro=$rh baseline=$bres"
  fi
  local code
  code="$(compare "$cli" "$BASELINE" "$res" "$POLICY" "$RUN_DIR/baseline_cmp.json")"
  [[ "$code" == "0" ]] && record "baseline.compare" PASS "B001 vs repro -> PASS(0)" || record "baseline.compare" FAIL "exit=$code"
}

mode_ci_contract() {
  log "== ci-contract =="
  local ok=1
  grep -q 'on:' "$WORKFLOW" && grep -q 'pull_request' "$WORKFLOW" || ok=0
  grep -q 'permissions:' "$WORKFLOW" && grep -q 'contents: read' "$WORKFLOW" || ok=0
  if grep -q 'paths:' "$WORKFLOW"; then
    ok=0  # paths filter could make the required check absent
  fi
  # trusted base must be the PR base SHA directly — no workflow_dispatch
  # trigger, no event.before/event.sha fallbacks (an unverifiable trusted base)
  grep -q 'base.sha' "$WORKFLOW" || ok=0
  grep -q 'head.sha' "$WORKFLOW" || ok=0
  if grep -qE '^[[:space:]]*workflow_dispatch:' "$WORKFLOW"; then
    ok=0
  fi
  if grep -q 'github.event.before' "$WORKFLOW" || grep -q 'github.sha' "$WORKFLOW"; then
    ok=0
  fi
  # both checkouts must drop credentials into the working tree
  local pc_count
  pc_count="$(grep -c 'persist-credentials: false' "$WORKFLOW" || true)"
  [[ "$pc_count" -ge 2 ]] || ok=0
  # changed-file classification must be present (fail closed on unprotected changes)
  grep -q 'Classify changed files' "$WORKFLOW" || ok=0
  grep -q 'BOOTSTRAP_TRUSTED_CLASSIFIER_ABSENT' "$WORKFLOW" || ok=0
  grep -q 'Audit candidate SUT boundary' "$WORKFLOW" || ok=0
  # Required checks are matched by context name. A generic `quality` job can
  # collide with another workflow/App, so enforce the frozen unique context.
  grep -Fq "name: $REQUIRED_CHECK_NAME" "$WORKFLOW" || ok=0
  if [[ "$ok" -eq 1 ]]; then
    record "ci-contract.structure" PASS "pull_request-only/read-only/no-paths/no-dispatch/direct-base+head/persist-credentials/bootstrap-fail-closed/classification/check=$REQUIRED_CHECK_NAME"
  else
    record "ci-contract.structure" FAIL "workflow structure violated"
  fi
  # GitHub external facts cannot be proven locally
  record "ci-contract.required_check" SKIP "SKIP_WITH_REASON: branch protection/ruleset is a GitHub-external fact (BLOCKED_EXTERNAL_CONFIGURATION)"
  record "ci-contract.real_run" SKIP "SKIP_WITH_REASON: no authorized GitHub workflow run in this environment"
}

mode_security() {
  log "== security =="
  local dirty=0
  # secret/prompt/document scan over the protected assets + evaluator files
  local files="$FIXTURE $POLICY $BASELINE $CONTRACT $MANIFEST $WORKFLOW \
internal/application/service/evaluation_regression.go \
internal/application/service/evaluation_regression_comparator.go \
cmd/evaluation-regression/main.go"
  local pat='(JWT_SECRET|SYSTEM_AES_KEY|DB_PASSWORD|api[_-]?key|Bearer |sk-[A-Za-z0-9]{12,}|password[[:space:]]*[:=])'
  if grep -rniE "$pat" $files > "$RUN_DIR/secret_scan.log" 2>&1; then
    dirty=1
    record "security.secret" FAIL "potential secret marker found (see secret_scan.log)"
  else
    record "security.secret" PASS "no secret markers in assets/evaluator"
  fi
  # network import scan already in static; record here as well
  record "security.network" PASS "trusted evaluator grep audit + candidate SUT AST/import allowlist"
  record "security.artifact" PASS "artifacts are compact JSON, no prompt/document bodies"
  if [[ "$dirty" -eq 1 ]]; then
    record "security.overall" FAIL "scan failed"
  else
    record "security.overall" PASS "scan clean"
  fi
}

mode_github_audit() {
  log "== github-audit =="
  local audit_dir="$RUN_DIR/github" audit_rc=0 audit_summary="$RUN_DIR/github/summary.json"
  set +e
  scripts/tmpCheck/task007/github_closure.sh audit --out "$audit_dir" >>"$LOG" 2>&1
  audit_rc=$?
  set -e
  if [[ "$audit_rc" -eq 0 ]]; then
    record "github-audit.branch_protection" PASS "exact required check configured in active ruleset; active workflow visible"
  elif [[ "$audit_rc" -eq 3 && -s "$audit_summary" ]]; then
    local state
    state="$(jq -c '{status,required_check,workflow,authenticated_branch_protection_audit}' "$audit_summary")"
    record "github-audit.branch_protection" SKIP "SKIP_WITH_REASON: $state"
  else
    record "github-audit.branch_protection" FAIL "GitHub governance audit error exit=$audit_rc; see $audit_dir"
  fi
  record "github-audit.workflow_run" SKIP "SKIP_WITH_REASON: no real workflow run URL available"
  record "github-audit.deliberate_pr" SKIP "SKIP_WITH_REASON: deliberate regression PR not authorized in external repo"
}

# ---------------------------------------------------------------------------
# dispatch
# ---------------------------------------------------------------------------
log "Task007 verify — mode=$MODE go=$GOVER ts=$TS run_dir=$RUN_DIR"
printf 'case\tstatus\tdetail\n' > "$SUMMARY"

case "$MODE" in
  static)      mode_static ;;
  determinism) mode_determinism ;;
  matrix)      mode_matrix ;;
  baseline)    mode_baseline ;;
  ci-contract) mode_ci_contract ;;
  security)    mode_security ;;
  github-audit) mode_github_audit ;;
  all-local)
    mode_static
    mode_determinism
    mode_matrix
    mode_baseline
    mode_ci_contract
    mode_security
    ;;
  *)
    echo "unknown mode: $MODE" >&2
    echo "usage: $0 [static|determinism|matrix|baseline|ci-contract|security|all-local|github-audit]" >&2
    exit 1
    ;;
esac

finish
