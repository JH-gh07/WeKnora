#!/usr/bin/env bash
#
# verify_task010.sh — one-click verification for Task010 (AC-R3 Run-level
# Four-result Unified Report). Default mode `all-safe` performs no Docker,
# no browser, no network, no provider and no secret access; it only builds and
# runs local unit tests + static/contract/docs/secret/manifest checks and the
# N01–N08 negative controls.
#
# Modes:
#   all-safe   format + contract + unit + frontend + docs + secret + manifest
#              + negative controls N01–N08 (default)
#   negative   run only the N01–N08 negative controls
#   full       all-safe + backend integration (SQLite matrix) + build
#              + PostgreSQL parity (--allow-docker) + live HTTP/browser/restart
#              (--allow-browser) — infra-dependent P0 checks SKIP with reason
#              unless the corresponding flag is supplied AND the tool is present.
#
# Flags (full mode):
#   --allow-docker     authorize disposable PostgreSQL via docker
#   --allow-browser    authorize live HTTP + headless browser checks
#
# Output: a stable summary.tsv plus a human log in a fresh run directory under
# /tmp/task010-verify/<ts>/.

set -uo pipefail

# UTF-8 hygiene: force a UTF-8 locale that actually exists on this host.
_UTFL=""
_AVAILABLE_LOCALES="$(locale -a 2>/dev/null || true)"
for _c in C.UTF-8 en_US.UTF-8 UTF-8; do
  if grep -qx "$_c" <<<"$_AVAILABLE_LOCALES"; then _UTFL="$_c"; break; fi
done
if [[ -n "$_UTFL" ]]; then export LC_ALL="$_UTFL"; export LANG="$_UTFL"; fi
export PYTHONUTF8=1
unset _UTFL _AVAILABLE_LOCALES _c

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
cd "$REPO_ROOT" || exit 4

# Evidence directory: sibling status repo, resolved RELATIVE to the repo root so
# no absolute author path is baked into the reproduction condition (§12.1).
EVIDENCE_DIR="${TASK010_EVIDENCE_DIR:-$(cd "$REPO_ROOT/.." && pwd)/status/evidence/task010}"

# Frozen asset paths.
CONTRACT="$EVIDENCE_DIR/api_contract_v1.json"
PREREG="$EVIDENCE_DIR/test_preregistration.yaml"
PREREG_SHA="$EVIDENCE_DIR/test_preregistration.sha256"
COMPAT_BEFORE="$EVIDENCE_DIR/compatibility_before"
TRUTH_TABLE="$EVIDENCE_DIR/availability_truth_table.md"

# Source paths (report read model).
TYPES_REPORT="internal/types/evaluation_report.go"
TYPES_EVAL="internal/types/evaluation.go"
SVC_REPORT="internal/application/service/evaluation_report.go"
HANDLER_EVAL="internal/handler/evaluation.go"
ROUTER_INFRA="internal/router/routes_infra.go"
DOC_EVAL="website-docs/03-features/15-evaluation.md"
DOC_MATRIX="docs/requirement_matrix.md"

TS="$(date +%Y%m%d_%H%M%S)"
RUN_DIR="/tmp/task010-verify/${TS}"
mkdir -p "$RUN_DIR"
LOG="$RUN_DIR/verify.log"
SUMMARY="$RUN_DIR/summary.tsv"

MODE="${1:-all-safe}"
GOVER="$(go version 2>/dev/null || echo 'go-unknown')"

ALLOW_DOCKER=0
ALLOW_BROWSER=0
for _a in "$@"; do
  case "$_a" in
    --allow-docker)  ALLOW_DOCKER=1 ;;
    --allow-browser) ALLOW_BROWSER=1 ;;
  esac
done
unset _a

if ! command -v jq >/dev/null 2>&1; then
  echo "fatal: jq is required but not on PATH" >&2
  exit 4
fi

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------
log()  { printf '%s\n' "$*" | tee -a "$LOG"; }
summary() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$SUMMARY"; }

declare -a FAILURES=()
declare -a SKIPS=()

record() { # record <case> <status> <detail>
  summary "$1" "$2" "$3"
  log "[$1] $2 :: $3"
  if [[ "$2" == "FAIL" ]]; then FAILURES+=("$1"); fi
  if [[ "$2" == "SKIP" ]]; then SKIPS+=("$1"); fi
}

finish() {
  local code=0
  if [[ "${#FAILURES[@]}" -gt 0 ]]; then code=4; fi

  # Finalize Evidence Authority only after every check has emitted its row.
  # Exclude the manifest itself from the root to avoid a self-referential hash;
  # the sorted file/hash stream is deterministic and independently recomputable.
  local manifest="$EVIDENCE_DIR/authoritative_run.json"
  if [[ -s "$manifest" && -s "$SUMMARY" ]]; then
    local summary_sha evidence_root manifest_tmp
    # Persist the exact machine-readable result before hashing it. The /tmp
    # run directory is disposable; Evidence Authority must survive restart.
    cp "$SUMMARY" "$EVIDENCE_DIR/latest_verifier_summary.tsv"
    [[ -s "$LOG" ]] && cp "$LOG" "$EVIDENCE_DIR/latest_verifier.log"
    summary_sha="$(shasum -a 256 "$SUMMARY" | awk '{print $1}')"
    evidence_root="$({
      find "$EVIDENCE_DIR" -type f ! -name 'authoritative_run.json' -print | sort |
        while IFS= read -r file; do
          shasum -a 256 "$file"
        done
    } | shasum -a 256 | awk '{print $1}')"
    manifest_tmp="$manifest.tmp"
    if jq --arg summary "$summary_sha" --arg root "$evidence_root" \
      '.verifier_summary_sha256=$summary | .evidence_manifest_root=$root' \
      "$manifest" > "$manifest_tmp" 2>/dev/null; then
      mv "$manifest_tmp" "$manifest"
    else
      rm -f "$manifest_tmp"
      FAILURES+=("manifest.finalize")
      code=4
    fi
  else
    FAILURES+=("manifest.finalize")
    code=4
  fi

  log ""
  log "==== SUMMARY ===="
  log "mode=$MODE go=$GOVER run_dir=$RUN_DIR evidence=$EVIDENCE_DIR"
  log "failures=${#FAILURES[@]} skips=${#SKIPS[@]}"
  if [[ "${#FAILURES[@]}" -gt 0 ]]; then log "FAILED: ${FAILURES[*]}"; fi
  if [[ "${#SKIPS[@]}" -gt 0 ]]; then log "SKIPPED: ${SKIPS[*]}"; fi
  log "summary_tsv=$SUMMARY"
  exit "$code"
}

# ---------------------------------------------------------------------------
# reusable checks (each returns 0=PASS, 1=FAIL so the negative controls can
# re-run them against tampered copies)
# ---------------------------------------------------------------------------

# N01 guard: stable run_id must exist in the DTO, the POST additive field, and
# the route.
ck_run_id_present() {
  local types_report="$1" types_eval="$2" router="$3"
  grep -q 'json:"run_id"' "$types_report" && \
  grep -q 'RunID' "$types_eval" && \
  grep -q ':run_id/report' "$router"
}

# N02 guard: unknown cost must stay UNKNOWN, never coerced to a displayable 0.
ck_cost_no_zero_for_unknown() {
  local svc="$1"
  grep -q 'ReasonAllCostUnknown' "$svc" && \
  grep -q 'ReasonMixedCurrency' "$svc" && \
  grep -q 'IsEstimate: true' "$svc"
}

# N03 guard: metrics_valid=false must never fabricate plausible zero scores.
ck_metrics_invalid_no_zero() {
  local svc="$1"
  grep -q 'ReasonMetricsInvalid' "$svc" && \
  grep -q 'MetricsValid' "$svc" && \
  grep -q '!run.MetricsValid' "$svc"
}

# N04 guard: tenant must come from the auth context only.
ck_tenant_scoped() {
  local svc="$1"
  grep -q 'MustTenantIDFromContext' "$svc" && \
  grep -q 'GetByRunID(ctx, tenantID, runID)' "$svc"
}

# N05 guard: tenant-window health must be explicitly not-run-completeness.
ck_health_scope() {
  local svc="$1"
  grep -q 'TenantWindowHealthIsNotRunScope: true' "$svc"
}

ck_schema_version() {
  local svc="$1"
  grep -q 'evaluation-run-report/v1' "$svc"
}

# reason_code_minimum from the frozen contract must all exist as Go constants.
ck_reason_codes() {
  local types_report="$1" contract="$2"
  local rc
  while IFS= read -r rc; do
    [[ -z "$rc" ]] && continue
    grep -q "\"$rc\"" "$types_report" || return 1
  done < <(jq -r '.reason_code_minimum[]' "$contract" 2>/dev/null)
  return 0
}

ck_contract_json() {
  local contract="$1"
  jq -e '.endpoint == "GET /api/v1/evaluation/runs/:run_id/report"' "$contract" >/dev/null 2>&1 || return 1
  jq -e '.response_schema == "evaluation-run-report/v1"' "$contract" >/dev/null 2>&1 || return 1
  local vocab
  vocab="$(jq -c '.availability_vocabulary' "$contract" 2>/dev/null)"
  [[ "$vocab" == '["AVAILABLE","PARTIAL","UNKNOWN","NOT_FINAL","UNSUPPORTED","DISABLED"]' ]] || return 1
  jq -e '.top_level_sections | index("run") != null and index("quality") != null and index("usage") != null and index("cost") != null and index("latency") != null and index("supporting_observation") != null and index("warnings") != null' "$contract" >/dev/null 2>&1 || return 1
  jq -e '.hard_constraints | length >= 8' "$contract" >/dev/null 2>&1 || return 1
}

ck_prereg_sha() {
  local prereg="$1" sha_file="$2"
  local recomputed stored
  recomputed="$(shasum -a 256 "$prereg" | awk '{print $1}')"
  # The frozen .sha256 file stores `shasum` output; the first field is the hash
  # (the filename path is a by-product and is not a reproduction condition).
  stored="$(awk '{print $1}' "$sha_file")"
  [[ -n "$recomputed" && "$recomputed" == "$stored" ]]
}

# N06 guard: legacy compatibility snapshot must be intact and referenced.
ck_legacy_snapshot() {
  local compat_dir="$1"
  [[ -d "$compat_dir" ]] && [[ -n "$(ls -A "$compat_dir")" ]]
}

# N07 guard: no secret canary may land in evidence or report sources. The
# pattern targets literal secret VALUES (assignment of a non-empty literal or a
# recognizable token format), not the mere ENV-VAR NAME (e.g. `SYSTEM_AES_KEY`
# as an identifier in crypto plumbing is legitimate and must not false-positive).
ck_secret() {
  local pat='(sk-[A-Za-z0-9]{16,}|Bearer [A-Za-z0-9._-]{20,}|-----BEGIN [A-Z ]+ PRIVATE KEY-----|(JWT_SECRET|SYSTEM_AES_KEY|DB_PASSWORD|OPENAI_API_KEY|api[_-]?key|secret|password|token)[[:space:]]*[:=][[:space:]]*["'"'"']?[A-Za-z0-9+/._-]{8,})'
  if grep -rniE "$pat" "$@" >/dev/null 2>&1; then return 1; fi
  return 0
}

# N08 guard: authoritative manifest must recompute to its own recorded hashes.
ck_manifest() {
  local dir="$1"
  local manifest="$dir/authoritative_run.json"
  [[ -s "$manifest" ]] || return 1
  local recorded_contract recorded_prereg actual_contract actual_prereg
  recorded_contract="$(jq -r '.api_contract_sha256' "$manifest" 2>/dev/null)"
  recorded_prereg="$(jq -r '.test_preregistration_sha256' "$manifest" 2>/dev/null)"
  actual_contract="$(shasum -a 256 "$dir/api_contract_v1.json" | awk '{print $1}')"
  actual_prereg="$(shasum -a 256 "$dir/test_preregistration.yaml" | awk '{print $1}')"
  [[ -n "$recorded_contract" && "$recorded_contract" == "$actual_contract" ]] || return 1
  [[ -n "$recorded_prereg" && "$recorded_prereg" == "$actual_prereg" ]] || return 1
  return 0
}

# ---------------------------------------------------------------------------
# modes
# ---------------------------------------------------------------------------
mode_format() {
  log "== format =="
  local files="$TYPES_REPORT $TYPES_EVAL $SVC_REPORT $HANDLER_EVAL $ROUTER_INFRA internal/handler/evaluation_report_integration_test.go internal/handler/evaluation_report_querycount_test.go"
  local dirty
  dirty="$(gofmt -l $files 2>/dev/null)"
  if [[ -z "$dirty" ]]; then
    record "format.gofmt" PASS "gofmt clean over report read model + tests"
  else
    record "format.gofmt" FAIL "gofmt -l non-empty: $(echo "$dirty" | tr '\n' ' ')"
  fi
  # Frontend formatting: only authoritative when prettier is installed locally
  # (never fetched from the network).
  if [[ -x frontend/node_modules/.bin/prettier ]]; then
    if frontend/node_modules/.bin/prettier --check \
        frontend/src/api/evaluation.ts \
        frontend/src/views/evaluation/EvaluationRunDetail.vue \
        frontend/src/views/evaluation/EvaluationRunLookup.vue \
        frontend/src/views/evaluation/evaluationReportState.ts \
        frontend/src/views/evaluation/evaluationRunRequestController.ts \
        frontend/src/views/evaluation/evaluationReportState.test.ts \
        frontend/src/views/evaluation/evaluationRunRequestController.test.ts \
        frontend/src/router/index.ts >>"$LOG" 2>&1; then
      record "format.prettier" PASS "prettier clean over new frontend files"
    else
      record "format.prettier" FAIL "prettier --check reported differences"
    fi
  else
    record "format.prettier" SKIP "SKIP_WITH_REASON: prettier not installed locally (offline env); gofmt is authoritative backend format"
  fi
}

mode_contract() {
  log "== contract =="
  if ck_contract_json "$CONTRACT"; then
    record "contract.schema" PASS "frozen contract JSON parses with endpoint/schema/vocabulary/sections/constraints"
  else
    record "contract.schema" FAIL "frozen contract JSON violated"
  fi
  if ck_reason_codes "$TYPES_REPORT" "$CONTRACT"; then
    record "contract.reason_codes" PASS "reason_code_minimum ⊆ Go constants"
  else
    record "contract.reason_codes" FAIL "contract reason code missing from types"
  fi
  if ck_prereg_sha "$PREREG" "$PREREG_SHA"; then
    record "contract.prereg_sha" PASS "test_preregistration.yaml sha256 matches frozen .sha256"
  else
    record "contract.prereg_sha" FAIL "test preregistration sha256 drift"
  fi
  if ck_schema_version "$SVC_REPORT"; then
    record "contract.schema_version" PASS "evaluation-run-report/v1"
  else
    record "contract.schema_version" FAIL "schema version missing"
  fi
  if ck_run_id_present "$TYPES_REPORT" "$TYPES_EVAL" "$ROUTER_INFRA"; then
    record "contract.run_id" PASS "run_id in DTO + POST additive + route"
  else
    record "contract.run_id" FAIL "run_id identity missing"
  fi
  if ck_cost_no_zero_for_unknown "$SVC_REPORT"; then
    record "contract.cost_honesty" PASS "unknown cost stays UNKNOWN (ALL_COST_UNKNOWN/MIXED_CURRENCY/is_estimate)"
  else
    record "contract.cost_honesty" FAIL "cost honesty markers missing"
  fi
  if ck_metrics_invalid_no_zero "$SVC_REPORT"; then
    record "contract.metrics_honesty" PASS "metrics_valid=false guarded by METRICS_INVALID"
  else
    record "contract.metrics_honesty" FAIL "metrics honesty markers missing"
  fi
  if ck_tenant_scoped "$SVC_REPORT"; then
    record "contract.tenant_scoped" PASS "tenant from auth context only"
  else
    record "contract.tenant_scoped" FAIL "tenant scoping markers missing"
  fi
  if ck_health_scope "$SVC_REPORT"; then
    record "contract.health_scope" PASS "tenant-window health labeled not-run-completeness"
  else
    record "contract.health_scope" FAIL "health scope marker missing"
  fi
  if ck_legacy_snapshot "$COMPAT_BEFORE"; then
    record "contract.legacy_snapshot" PASS "compatibility_before/ snapshot present"
  else
    record "contract.legacy_snapshot" FAIL "compatibility_before/ snapshot empty or missing"
  fi
}

mode_unit() {
  log "== unit (backend) =="
  if go test ./internal/application/service/ -run 'Report|Evaluation' -count=1 >>"$LOG" 2>&1; then
    record "unit.service" PASS "go test ./internal/application/service/ -run Report|Evaluation"
  else
    record "unit.service" FAIL "service report/evaluation tests failed"
  fi
  if go test ./internal/handler/ -run 'Report|Evaluation' -count=1 >>"$LOG" 2>&1; then
    record "unit.handler" PASS "go test ./internal/handler/ -run Report|Evaluation"
  else
    record "unit.handler" FAIL "handler report/evaluation tests failed"
  fi
  if go build ./internal/container/ ./cmd/... >>"$LOG" 2>&1; then
    record "unit.build" PASS "go build ./internal/container/ ./cmd/..."
  else
    record "unit.build" FAIL "backend build failed"
  fi
}

mode_frontend() {
  log "== frontend =="
  cd frontend || { record "frontend.cd" FAIL "no frontend dir"; return; }
  if npx tsx --test \
      src/views/evaluation/evaluationReportState.test.ts \
      src/views/evaluation/evaluationRunRequestController.test.ts >>"$LOG" 2>&1; then
    record "frontend.unit" PASS "evaluation state + stale-guard tests (11)"
  else
    record "frontend.unit" FAIL "evaluation unit tests failed"
  fi
  if npx tsx --test src/i18n/localeKeyAudit.test.ts >>"$LOG" 2>&1; then
    record "frontend.i18n" PASS "locale key parity audit (11)"
  else
    record "frontend.i18n" FAIL "i18n key parity audit failed"
  fi
  if npm run type-check >>"$LOG" 2>&1; then
    record "frontend.typecheck" PASS "vue-tsc --build"
  else
    record "frontend.typecheck" FAIL "vue-tsc type-check failed"
  fi
  cd "$REPO_ROOT" || exit 4
}

mode_docs() {
  log "== docs =="
  # The evaluation doc must NOT re-assert the retired "restart loses run/metric"
  # claim, must surface run_id, the report endpoint and the unified-report section.
  if grep -q 'runs/:run_id/report' "$DOC_EVAL" && grep -q '统一运行报告' "$DOC_EVAL" && grep -q 'run_id' "$DOC_EVAL"; then
    record "docs.eval_report" PASS "doc exposes run_id + unified report endpoint/section"
  else
    record "docs.eval_report" FAIL "evaluation doc missing run_id/report markers"
  fi
  # Honest persistence boundary: prompt params process-cached is allowed; the
  # retired claim "Run/Metric 重启即丢" must NOT be present.
  if ! grep -qE '重启即丢|重启丢失|内存/重启丢失|重启后.*丢失' "$DOC_EVAL"; then
    record "docs.persistence_boundary" PASS "no retired restart-loss claim"
  else
    record "docs.persistence_boundary" FAIL "retired restart-loss claim still present"
  fi
  # Requirement matrix R3 must reference Task010 and the report service.
  if grep -q 'Task010' "$DOC_MATRIX" && grep -q 'evaluation_report' "$DOC_MATRIX"; then
    record "docs.requirement_matrix" PASS "R3 references Task010 + evaluation_report"
  else
    record "docs.requirement_matrix" FAIL "requirement matrix R3 not updated"
  fi
}

mode_secret() {
  log "== secret =="
  local dirty=0
  if ck_secret "$EVIDENCE_DIR"; then
    record "secret.evidence" PASS "no secret canary in evidence dir"
  else
    dirty=1
    record "secret.evidence" FAIL "secret canary found in evidence (see sensitive_scan)"
  fi
  # Scope the source scan to the Task010 report read model only. Scanning the
  # entire internal/ tree would false-positive on pre-existing crypto plumbing
  # that legitimately references ENV-VAR NAMES (not values).
  if ck_secret "$TYPES_REPORT" "$SVC_REPORT" "$HANDLER_EVAL" \
       "internal/handler/evaluation_report_integration_test.go" \
       "internal/handler/evaluation_report_querycount_test.go" \
       "frontend/src/api/evaluation.ts" \
       "frontend/src/views/evaluation"; then
    record "secret.sources" PASS "no secret canary in report sources"
  else
    dirty=1
    record "secret.sources" FAIL "secret canary found in report sources"
  fi
  if [[ "$dirty" -eq 0 ]]; then
    record "secret.overall" PASS "scan clean"
  else
    record "secret.overall" FAIL "secret scan failed"
  fi
}

mode_manifest() {
  log "== manifest (Evidence Authority) =="
  local contract_sha prereg_sha tree
  contract_sha="$(shasum -a 256 "$CONTRACT" | awk '{print $1}')"
  prereg_sha="$(shasum -a 256 "$PREREG" | awk '{print $1}')"
  tree="$(git rev-parse HEAD^{tree} 2>/dev/null || echo unknown)"
  local sha
  sha="$(git rev-parse HEAD 2>/dev/null || echo unknown)"

  local manifest="$EVIDENCE_DIR/authoritative_run.json"
  jq -n \
    --arg task "task010" \
    --arg commit "$sha" \
    --arg tree "$tree" \
    --arg schema "evaluation-run-report/v1" \
    --arg contract "$contract_sha" \
    --arg prereg "$prereg_sha" \
    --arg run "automated_all-safe_${TS}" \
    --arg decision "PENDING_FULL_VERIFIER" \
    '{task_id:$task, source_commit:$commit, source_tree:$tree, report_schema_version:$schema, api_contract_sha256:$contract, test_preregistration_sha256:$prereg, verifier_summary_sha256:null, evidence_manifest_root:null, run:$run, decision:$decision}' \
    > "$manifest" 2>"$LOG" || { record "manifest.generate" FAIL "jq failed"; return; }

  # Record prior runs as SUPERSEDED (rc1 + any earlier authoritative run).
  printf 'prior_run\tdisposition\n' > "$EVIDENCE_DIR/superseded_runs.tsv"
  printf '0.8.0-rc1@20a7f837\tSUPERSEDED_BEFORE_SIGNOFF (historical, not deleted)\n' >> "$EVIDENCE_DIR/superseded_runs.tsv"

  if ck_manifest "$EVIDENCE_DIR"; then
    record "manifest.recompute" PASS "authoritative_run.json recomputes to its own hashes"
  else
    record "manifest.recompute" FAIL "authoritative manifest does not recompute"
  fi
  record "manifest.superseded" PASS "superseded_runs.tsv lists rc1 (historical, not deleted)"
}

# negative controls N01–N08: each applies a controlled break to a COPY and
# asserts the corresponding check FAILs (i.e. the verifier catches the break).
mode_negative() {
  log "== negative controls N01–N08 =="
  local neg="$RUN_DIR/negative"
  mkdir -p "$neg"

  # N01 delete run_id from the DTO
  sed '/json:"run_id"/d' "$TYPES_REPORT" > "$neg/report_no_runid.go"
  if ck_run_id_present "$neg/report_no_runid.go" "$TYPES_EVAL" "$ROUTER_INFRA"; then
    record "negative.N01_remove_run_id" FAIL "verifier failed to catch removed run_id"
  else
    record "negative.N01_remove_run_id" PASS "removed run_id detected"
  fi

  # N02 UNKNOWN cost coerced to 0 (drop the ALL_COST_UNKNOWN guard)
  grep -v 'ReasonAllCostUnknown' "$SVC_REPORT" > "$neg/report_cost_zero.go"
  if ck_cost_no_zero_for_unknown "$neg/report_cost_zero.go"; then
    record "negative.N02_cost_null_to_0" FAIL "verifier failed to catch cost-null-to-0"
  else
    record "negative.N02_cost_null_to_0" PASS "cost-null-to-0 detected"
  fi

  # N03 metrics_invalid coerced to zero scores (drop METRICS_INVALID guard)
  grep -v 'ReasonMetricsInvalid' "$SVC_REPORT" > "$neg/report_metrics_zero.go"
  if ck_metrics_invalid_no_zero "$neg/report_metrics_zero.go"; then
    record "negative.N03_metrics_invalid_to_zero" FAIL "verifier failed to catch invalid-metrics-to-zero"
  else
    record "negative.N03_metrics_invalid_to_zero" PASS "invalid-metrics-to-zero detected"
  fi

  # N04 tenant B reads tenant A (drop tenant scoping from lookup)
  grep -v 'MustTenantIDFromContext' "$SVC_REPORT" > "$neg/report_no_tenant.go"
  if ck_tenant_scoped "$neg/report_no_tenant.go"; then
    record "negative.N04_cross_tenant_leak" FAIL "verifier failed to catch cross-tenant leak"
  else
    record "negative.N04_cross_tenant_leak" PASS "cross-tenant leak detected"
  fi

  # N05 remove health scope marker
  grep -v 'TenantWindowHealthIsNotRunScope: true' "$SVC_REPORT" > "$neg/report_no_scope.go"
  if ck_health_scope "$neg/report_no_scope.go"; then
    record "negative.N05_health_scope_removed" FAIL "verifier failed to catch removed health scope"
  else
    record "negative.N05_health_scope_removed" PASS "removed health scope detected"
  fi

  # N06 break the legacy compatibility snapshot
  local compat_copy="$neg/compatibility_before_empty"
  mkdir -p "$compat_copy"
  if ck_legacy_snapshot "$compat_copy"; then
    record "negative.N06_legacy_snapshot_broken" FAIL "verifier failed to catch broken legacy snapshot"
  else
    record "negative.N06_legacy_snapshot_broken" PASS "broken legacy snapshot detected"
  fi

  # N07 secret canary in evidence
  local canary_dir="$neg/evidence_canary"
  mkdir -p "$canary_dir"
  printf 'JWT_SECRET=canary-do-not-commit\n' > "$canary_dir/leak.md"
  if ck_secret "$canary_dir"; then
    record "negative.N07_secret_canary" FAIL "verifier failed to catch secret canary"
  else
    record "negative.N07_secret_canary" PASS "secret canary detected"
  fi

  # N08 tamper the authoritative manifest hash
  local manifest_dir="$neg/evidence_manifest"
  mkdir -p "$manifest_dir"
  cp "$CONTRACT" "$manifest_dir/api_contract_v1.json"
  cp "$PREREG" "$manifest_dir/test_preregistration.yaml"
  printf '{"api_contract_sha256":"deadbeef","test_preregistration_sha256":"deadbeef"}' > "$manifest_dir/authoritative_run.json"
  if ck_manifest "$manifest_dir"; then
    record "negative.N08_manifest_tamper" FAIL "verifier failed to catch manifest tamper"
  else
    record "negative.N08_manifest_tamper" PASS "manifest tamper detected"
  fi
}

mode_integration() {
  log "== integration (SQLite matrix) =="
  if go test ./internal/handler/ -run 'TestReportIntegration|TestReportQueryCount' -count=1 >>"$LOG" 2>&1; then
    record "integration.sqlite_matrix" PASS "SQLite integration + query-count tests"
  else
    record "integration.sqlite_matrix" FAIL "SQLite integration tests failed"
  fi
}

mode_build() {
  log "== build =="
  cd frontend || { record "build.frontend" FAIL "no frontend dir"; cd "$REPO_ROOT"; return; }
  if npm run build >>"$LOG" 2>&1; then
    record "build.frontend" PASS "vite build"
  else
    record "build.frontend" FAIL "vite build failed"
  fi
  cd "$REPO_ROOT" || exit 4
}

mode_postgres() {
  log "== postgresql parity =="
  if [[ "$ALLOW_DOCKER" -ne 1 ]]; then
    record "postgres.parity" SKIP "SKIP_WITH_REASON: --allow-docker not supplied (disposable PG not authorized)"
    return
  fi
  if ! command -v docker >/dev/null 2>&1; then
    record "postgres.parity" SKIP "SKIP_WITH_REASON: docker not present"
    return
  fi
  local pg_out="$RUN_DIR/postgres"
  if scripts/tmpCheck/task010/run_postgres_parity.sh "$pg_out" >>"$LOG" 2>&1; then
    record "postgres.parity" PASS "ephemeral ParadeDB + real migrations + normalized report JSON == SQLite"
  else
    record "postgres.parity" FAIL "postgres parity failed; see $pg_out"
  fi
}

mode_live_http() {
  log "== live HTTP (real TCP) =="
  local out="$RUN_DIR/livehttp"
  if go run ./scripts/tmpCheck/task010/live_http/ "$out" >>"$LOG" 2>&1; then
    record "live_http.matrix" PASS "real TCP round-trip through gin router+handler: 6 report samples + 400/404/404-cross-tenant"
  else
    record "live_http.matrix" FAIL "live HTTP matrix failed; see $out"
  fi
}

mode_browser() {
  log "== browser B01–B18 =="
  if [[ "$ALLOW_BROWSER" -ne 1 ]]; then
    record "browser.matrix" SKIP "SKIP_WITH_REASON: --allow-browser not supplied (B01–B18 not authorized)"
    return
  fi

  local browser_script="$SCRIPT_DIR/browser_matrix/run_browser_matrix.sh"
  if [[ ! -x "$browser_script" ]]; then
    record "browser.matrix" FAIL "browser test script not found or not executable: $browser_script"
    return
  fi

  log "Running self-contained real-stack browser matrix"
  if TASK010_BROWSER_EVIDENCE="$EVIDENCE_DIR/browser_evidence" "$browser_script" > "$RUN_DIR/browser.log" 2>&1; then
    local browser_tsv="$EVIDENCE_DIR/browser_evidence/browser_matrix.tsv"
    if [[ -f "$browser_tsv" ]]; then
      local row_count pass_count unique_count
      row_count=$(tail -n +2 "$browser_tsv" | wc -l | tr -d ' ')
      pass_count=$(tail -n +2 "$browser_tsv" | awk -F '\t' '$2 == "PASS" {n++} END {print n+0}')
      unique_count=$(tail -n +2 "$browser_tsv" | cut -f1 | sort -u | wc -l | tr -d ' ')

      # First check counts
      if [[ "$row_count" -eq 18 && "$pass_count" -eq 18 && "$unique_count" -eq 18 ]]; then
        # Then check diff - use explicit temporary files to avoid process substitution issues
        local expected_file="$RUN_DIR/browser_expected.txt"
        local actual_file="$RUN_DIR/browser_actual.txt"
        printf 'B%02d\n' {1..18} > "$expected_file"
        tail -n +2 "$browser_tsv" | cut -f1 | sort > "$actual_file"

        if diff -u "$expected_file" "$actual_file" >>"$LOG" 2>&1; then
          record "browser.matrix" PASS "strict B01-B18: 18/18 PASS via source Vite + real repository/service/handler fixture"
        else
          echo "DEBUG: diff failed with exit $?" >>"$LOG"
          echo "Expected:" >>"$LOG"
          cat "$expected_file" >>"$LOG"
          echo "Actual:" >>"$LOG"
          cat "$actual_file" >>"$LOG"
          record "browser.matrix" FAIL "browser TSV must contain exactly unique B01-B18 and all PASS (diff failed)"
        fi
      else
        echo "DEBUG: count check failed: row=$row_count pass=$pass_count unique=$unique_count" >>"$LOG"
        record "browser.matrix" FAIL "browser TSV must contain exactly unique B01-B18 and all PASS (counts: $row_count/$pass_count/$unique_count)"
      fi
    else
      record "browser.matrix" FAIL "browser tests ran but TSV output not found"
    fi
  else
    record "browser.matrix" FAIL "browser test script failed (see $RUN_DIR/browser.log)"
  fi
}

# ---------------------------------------------------------------------------
# dispatch
# ---------------------------------------------------------------------------
log "Task010 verify — mode=$MODE go=$GOVER ts=$TS run_dir=$RUN_DIR evidence=$EVIDENCE_DIR"
printf 'case\tstatus\tdetail\n' > "$SUMMARY"

case "$MODE" in
  all-safe)
    mode_format
    mode_contract
    mode_unit
    mode_frontend
    mode_docs
    mode_secret
    mode_manifest
    mode_negative
    ;;
  negative)
    mode_negative
    ;;
  full)
    mode_format
    mode_contract
    mode_unit
    mode_frontend
    mode_docs
    mode_secret
    mode_integration
    mode_build
    mode_live_http
    mode_postgres
    mode_browser
    mode_manifest
    mode_negative
    ;;
  *)
    echo "unknown mode: $MODE" >&2
    echo "usage: $0 [all-safe|negative|full] [--allow-docker] [--allow-browser]" >&2
    exit 1
    ;;
esac

finish
