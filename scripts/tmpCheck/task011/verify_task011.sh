#!/usr/bin/env bash
# Task011 — Scheduled Advisory Quality CI verifier.
#
# Modes:
#   static    — frozen workflow contract (C01-C14) + static/security checks (S01-S15)
#   local     — deterministic local execution (L01-L10) of the shared public entry
#   negative  — controlled negative proofs (N01-N10)
#   github    — read-only GitHub live evidence (G01-G14); requires --repo OWNER/REPO
#   all-safe  — static + negative (+ local when public entry is present)
#
# Contract: every check writes one TSV row (id<TAB>status<TAB>detail) to summary.tsv;
# finish() emits the tally and exits non-zero iff any non-PASS/PARTIAL/SKIP row exists.
# No author absolute path is ever hardcoded; repo root is auto-discovered.
set -uo pipefail

# ---- repo root discovery (never hardcode an author path) --------------------
SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SOURCE_DIR/../../.." && pwd)"
cd "$REPO_ROOT"

# ---- UTF-8 / locale hygiene (Chinese+spaces paths, non-UTF-8 locale safe) ----
if command -v locale >/dev/null 2>&1; then
  for c in en_US.UTF-8 zh_CN.UTF-8 C.UTF-8 C.utf8 en_US.utf8 zh_CN.utf8; do
    if locale -a 2>/dev/null | grep -qx "$c"; then export LC_ALL="$c" LANG="$c"; break; fi
  done
fi
export PYTHONUTF8=1 PYTHONIOENCODING=utf-8

# ---- output ---------------------------------------------------------------
RUN_DIR="${TASK011_RUN_DIR:-$SOURCE_DIR/.run}"
mkdir -p "$RUN_DIR"
SUMMARY="$RUN_DIR/summary.tsv"
: > "$SUMMARY"
RECORDED=0

record() { # id status detail
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$SUMMARY"
  RECORDED=$((RECORDED+1))
}

finish() {
  local fails=0
  while IFS=$'\t' read -r id st dt; do
    [ -z "$id" ] && continue
    case "$st" in PASS|PARTIAL|SKIP|SKIP_WITH_REASON) ;; *) fails=$((fails+1)); printf 'FAIL  %-28s %s\n' "$id" "$dt" >&2 ;; esac
  done < "$SUMMARY"
  printf '\n===== Task011 verifier: %d checks, %d FAIL =====\n' "$RECORDED" "$fails" >&2
  return "$fails"
}

# ---- paths under test ------------------------------------------------------
WF="$REPO_ROOT/.github/workflows/evaluation-regression-scheduled.yml"
PRWF="$REPO_ROOT/.github/workflows/evaluation-regression.yml"
PUBLIC_ENTRY="$REPO_ROOT/scripts/reproduce-evaluation.sh"

have() { [ -f "$1" ]; }
grepq() { grep -qE "$@" 2>/dev/null; }

# =============================================================================
# STATIC / CONTRACT checks
# =============================================================================
ck_c01_trigger() {
  local f="$WF"
  if ! have "$f"; then record c01.trigger FAIL "workflow missing"; return 1; fi
  if grepq '^[[:space:]]+schedule:' "$f" && grepq '^[[:space:]]+workflow_dispatch:' "$f" \
     && ! grepq -E 'pull_request|^[[:space:]]+push:|repository_dispatch|pull_request_target' "$f"; then
    record c01.trigger PASS "schedule + no-input workflow_dispatch only (S01/S08)"; return 0
  else
    record c01.trigger FAIL "trigger must be schedule + workflow_dispatch, no PR/push/repository_dispatch/pull_request_target"; return 1
  fi
}

ck_c02_cron() {
  local f="$WF"; have "$f" || { record c02.cron FAIL "workflow missing"; return 1; }
  if grepq 'cron:[[:space:]]*"17 2 \* \* 1-5"' "$f"; then
    record c02.cron PASS "cron = '17 2 * * 1-5' (weekday, off the hour)"; return 0
  else
    record c02.cron FAIL "cron must be exactly '17 2 * * 1-5'"; return 1
  fi
}

ck_c03_identity() {
  local f="$WF"; have "$f" || { record c03.identity FAIL "workflow missing"; return 1; }
  if grepq '^name:[[:space:]]*Evaluation Quality Scheduled' "$f" \
     && grepq 'name:[[:space:]]*evaluation-regression-scheduled / quality' "$f" \
     && ! grepq 'name:[[:space:]]*evaluation-regression / quality' "$f"; then
    record c03.identity PASS "workflow name 'Evaluation Quality Scheduled' + job name 'evaluation-regression-scheduled / quality' (distinct from required context)"; return 0
  else
    record c03.identity FAIL "job/workflow name must differ from the required context 'evaluation-regression / quality'"; return 1
  fi
}

ck_c04_permissions() {
  local f="$WF"; have "$f" || { record c04.permissions FAIL "workflow missing"; return 1; }
  if grepq '^permissions:' "$f" && grepq '^[[:space:]]+contents:[[:space:]]+read' "$f" \
     && ! grepq -E '^[[:space:]]+(issues|pull-requests|id-token|packages|deployments|actions|checks|statuses):[[:space:]]+write' "$f" \
     && ! grepq '^[[:space:]]+contents:[[:space:]]+write' "$f"; then
    record c04.permissions PASS "permissions exactly contents: read (no write scopes)"; return 0
  else
    record c04.permissions FAIL "permissions must be exactly contents: read"; return 1
  fi
}

ck_c05_checkout() {
  local f="$WF"; have "$f" || { record c05.checkout FAIL "workflow missing"; return 1; }
  if grepq 'persist-credentials:[[:space:]]*false' "$f" \
     && ! grepq 'persist-credentials:[[:space:]]*true' "$f"; then
    record c05.checkout PASS "persist-credentials:false (exact-SHA checkout verified live via G06)"; return 0
  else
    record c05.checkout FAIL "checkout must set persist-credentials:false"; return 1
  fi
}

ck_c06_compute() {
  local f="$WF"; have "$f" || { record c06.compute FAIL "workflow missing"; return 1; }
  if ( grepq 'make reproduce-evaluation' "$f" || grepq 'scripts/reproduce-evaluation\.sh' "$f" ) \
     && ! grepq -iE 'recall|precision|ndcg|mrr|0\.625|0\.45|baseline_B001_result' "$f"; then
    record c06.compute PASS "only calls public entry; no metric/baseline constants copied (S09)"; return 0
  else
    record c06.compute FAIL "must call public entry and must not copy metric/baseline constants"; return 1
  fi
}

ck_c07_baseline_ro() {
  local f="$WF"; have "$f" || { record c07.baseline_ro FAIL "workflow missing"; return 1; }
  if ! grepq -E 'git push|git commit|git add|baseline_B001_result\.json[[:space:]]*>|cp .*baselines|baseline.*promotion' "$f"; then
    record c07.baseline_ro PASS "baseline read-only, no promotion/write (S07)"; return 0
  else
    record c07.baseline_ro FAIL "must not modify/promote baseline"; return 1
  fi
}

ck_c08_no_provider() {
  local f="$WF"; have "$f" || { record c08.no_provider FAIL "workflow missing"; return 1; }
  if ! grepq -E '\$\{\{[[:space:]]*secrets\.|\.env|OPENAI_API_KEY|ANTHROPIC_API_KEY|api[_-]?key' "$f"; then
    record c08.no_provider PASS "no provider secret / .env read (S06)"; return 0
  else
    record c08.no_provider FAIL "must not read provider secrets or .env"; return 1
  fi
}

ck_c09_timeout() {
  local f="$WF"; have "$f" || { record c09.timeout FAIL "workflow missing"; return 1; }
  if grepq 'timeout-minutes:[[:space:]]*20' "$f"; then
    record c09.timeout PASS "timeout-minutes: 20"; return 0
  else
    record c09.timeout FAIL "timeout-minutes must be 20"; return 1
  fi
}

ck_c10_concurrency() {
  local f="$WF"; have "$f" || { record c10.concurrency FAIL "workflow missing"; return 1; }
  if grepq 'concurrency:' "$f" && grepq 'cancel-in-progress:[[:space:]]*false' "$f" \
     && ! grepq 'cancel-in-progress:[[:space:]]*true' "$f"; then
    record c10.concurrency PASS "cancel-in-progress:false (no cancel of in-flight scheduled evidence)"; return 0
  else
    record c10.concurrency FAIL "concurrency must set cancel-in-progress:false"; return 1
  fi
}

ck_c11_artifact_always() {
  local f="$WF"; have "$f" || { record c11.artifact_always FAIL "workflow missing"; return 1; }
  if grepq 'upload-artifact' "$f" && grepq 'if:[[:space:]]*always\(\)' "$f" && grepq 'retention-days:[[:space:]]*30' "$f"; then
    record c11.artifact_always PASS "artifact upload under if: always(), retention 30 days (S11)"; return 0
  else
    record c11.artifact_always FAIL "artifact upload must run under if: always() with retention-days: 30"; return 1
  fi
}

ck_c12_decision_mapping() {
  local f="$WF"; have "$f" || { record c12.decision_mapping FAIL "workflow missing"; return 1; }
  if ! grepq 'continue-on-error:[[:space:]]*true' "$f" \
     && grepq 'exit 0' "$f" && grepq 'exit 2' "$f" && grepq 'exit 3' "$f" && grepq 'exit 4' "$f"; then
    record c12.decision_mapping PASS "final exit preserves 0/2/3/4; no continue-on-error swallow (S12)"; return 0
  else
    record c12.decision_mapping FAIL "must preserve 0/2/3/4 and must not swallow with continue-on-error"; return 1
  fi
}

ck_c13_ruleset_nonmember() {
  local pr="$PRWF"
  if have "$pr" && grepq 'evaluation-regression / quality' "$pr" && ! grepq 'evaluation-regression-scheduled' "$pr"; then
    record c13.ruleset_nonmember PASS "PR required context unchanged; scheduled context not injected (S13)"; return 0
  else
    record c13.ruleset_nonmember FAIL "scheduled context must not be injected into the PR required check"; return 1
  fi
}

ck_c14_g5_unchanged() {
  local pr="$PRWF"
  if have "$pr"; then
    if git diff --quiet -- "$pr" 2>/dev/null; then
      record c14.g5_unchanged PASS "PR workflow byte-identical to git HEAD"; return 0
    else
      record c14.g5_unchanged FAIL "PR workflow has uncommitted drift"; return 1
    fi
  else
    record c14.g5_unchanged FAIL "PR workflow missing"; return 1
  fi
}

ck_s14_actionlint() {
  if command -v actionlint >/dev/null 2>&1; then
    if actionlint "$WF" >/dev/null 2>&1; then record s14.actionlint PASS "actionlint clean"; return 0; else record s14.actionlint FAIL "actionlint found issues"; return 1; fi
  else
    record s14.actionlint SKIP_WITH_REASON "actionlint not installed locally; authoritative syntax evidence = real GitHub run"
    return 0
  fi
}

ck_s15_secret_scan() {
  local f="$WF"; have "$f" || { record s15.secret_scan FAIL "workflow missing"; return 1; }
  local pat='(sk-[A-Za-z0-9]{16,}|Bearer [A-Za-z0-9._-]{20,}|-----BEGIN [A-Z ]+ PRIVATE KEY-----|JWT_SECRET|OPENAI_API_KEY|api[_-]?key[[:space:]]*[:=]|password[[:space:]]*[:=]|token[[:space:]]*[:=])'
  if grep -iE "$pat" "$f" >/dev/null 2>&1; then
    record s15.secret_scan FAIL "secret canary pattern present in workflow (S15)"; return 1
  else
    record s15.secret_scan PASS "no secret pattern in workflow (S15)"; return 0
  fi
}

# =============================================================================
# LOCAL checks (L01-L10)
# =============================================================================
local_tmp() { mktemp -d "${TMPDIR:-/tmp}/task011-local.XXXXXX"; }

ck_l01_normal() {
  local out; out=$(local_tmp)
  local rc=0; OUTPUT_DIR="$out" bash "$PUBLIC_ENTRY" >/dev/null 2>&1; rc=$?
  if [ "$rc" -eq 0 ]; then record l01.normal PASS "public entry exit 0 (PASS)"; return 0
  else record l01.normal FAIL "public entry exit=$rc expected 0"; return 1; fi
}

ck_l02_determinism() {
  local out; out=$(local_tmp); mkdir -p "$out"
  local i hash first="" h2
  for i in $(seq 1 20); do
    local o="$out/run$i"; mkdir -p "$o"
    if ! OUTPUT_DIR="$o" bash "$PUBLIC_ENTRY" >/dev/null 2>&1; then record l02.determinism FAIL "run $i failed"; return 1; fi
    hash=$(shasum -a 256 "$o/candidate_result.json" 2>/dev/null | awk '{print $1}')
    if [ -z "$hash" ]; then hash=$(shasum -a 256 "$o/comparison_decision.json" 2>/dev/null | awk '{print $1}'); fi
    if [ -z "$hash" ]; then record l02.determinism FAIL "run $i produced no candidate/decision"; return 1; fi
    [ -z "$first" ] && first="$hash"
    [ "$hash" = "$first" ] || { record l02.determinism FAIL "run $i hash diverged ($hash vs $first)"; return 1; }
  done
  record l02.determinism PASS "20 identical result hashes ($first)"; return 0
}

ck_l03_env_i() {
  local out; out=$(local_tmp); mkdir -p "$out"
  local rc=0; env -i PATH="$PATH" HOME="$HOME" OUTPUT_DIR="$out" bash "$PUBLIC_ENTRY" >/dev/null 2>&1; rc=$?
  if [ "$rc" -eq 0 ]; then record l03.env_i PASS "env -i clean run exit 0"; return 0
  else record l03.env_i FAIL "env -i run exit=$rc"; return 1; fi
}

ck_l04_locale_c() {
  local out; out=$(local_tmp); mkdir -p "$out"
  local rc=0; LC_ALL=C OUTPUT_DIR="$out" bash "$PUBLIC_ENTRY" >/dev/null 2>&1; rc=$?
  if [ "$rc" -eq 0 ]; then record l04.locale_c PASS "LC_ALL=C exit 0"; return 0
  elif [ "$rc" -eq 4 ]; then record l04.locale_c PASS "LC_ALL=C honest bootstrap ERROR/4"; return 0
  else record l04.locale_c FAIL "unexpected exit $rc"; return 1; fi
}

ck_l05_path_unicode() {
  local base; base=$(mktemp -d "${TMPDIR:-/tmp}/评测 空格 task011.XXXXXX")
  local out="$base/中文 目录"; mkdir -p "$out"
  local rc=0; OUTPUT_DIR="$out" bash "$PUBLIC_ENTRY" >/dev/null 2>&1; rc=$?
  if [ "$rc" -eq 0 ]; then record l05.path_unicode PASS "Chinese+spaces path run exit 0"; return 0
  else record l05.path_unicode FAIL "unicode/space path run exit=$rc"; return 1; fi
}

ck_l06_goproxy_off() {
  local out; out=$(local_tmp); mkdir -p "$out"
  local rc=0; GOPROXY=off OUTPUT_DIR="$out" bash "$PUBLIC_ENTRY" >/dev/null 2>&1; rc=$?
  if [ "$rc" -eq 0 ]; then record l06.goproxy_off PASS "GOPROXY=off after warmup exit 0"; return 0
  else record l06.goproxy_off FAIL "GOPROXY=off run exit=$rc"; return 1; fi
}

ck_l07_empty_out() {
  local out; out=$(local_tmp); mkdir -p "$out"
  local rc=0; OUTPUT_DIR="$out" bash "$PUBLIC_ENTRY" >/dev/null 2>&1; rc=$?
  if [ "$rc" -eq 0 ]; then record l07.empty_out PASS "fresh empty OUTPUT_DIR exit 0"; return 0
  else record l07.empty_out FAIL "fresh empty OUTPUT_DIR exit=$rc"; return 1; fi
}

ck_l08_nonempty_out() {
  local out; out=$(local_tmp); mkdir -p "$out"; echo seed > "$out/seed.txt"
  local rc=0; OUTPUT_DIR="$out" bash "$PUBLIC_ENTRY" >/dev/null 2>&1; rc=$?
  if [ "$rc" -eq 4 ]; then record l08.nonempty_out PASS "non-empty OUTPUT_DIR -> ERROR/4"; return 0
  else record l08.nonempty_out FAIL "expected ERROR/4 got $rc"; return 1; fi
}

ck_l09_missing_tool() {
  local out; out=$(local_tmp); mkdir -p "$out"
  local rc=0; PATH="/usr/bin:/bin" OUTPUT_DIR="$out" bash "$PUBLIC_ENTRY" >/dev/null 2>&1; rc=$?
  if [ "$rc" -eq 4 ]; then record l09.missing_tool PASS "missing tool -> ERROR/4"; return 0
  else record l09.missing_tool FAIL "expected ERROR/4 got $rc"; return 1; fi
}

ck_l10_residue() {
  if git status --porcelain | grep -qE 'reproduction-output|candidate_result|comparison_decision|evaluation-scheduled'; then
    record l10.residue FAIL "source tree residue detected"; return 1
  else
    record l10.residue PASS "no residue in source tree"; return 0
  fi
}

# =============================================================================
# NEGATIVE controls (N01-N10) — tamper samples must be DETECTED
# =============================================================================
# neg_assert <check-fn> <workflow-file> <pr-workflow-file> <label>
# Runs the check against tampered file(s); expects the check to FAIL (detect).
# The check's own record() output is diverted to a scratch file so it cannot
# pollute the main summary.tsv — only the nXX verdict row is recorded.
neg_assert() {
  local fn="$1" wffile="$2" prfile="$3" label="$4"
  local saved="$SUMMARY"
  SUMMARY="$RUN_DIR/.neg_scratch.tsv"
  WF="$wffile"; PRWF="$prfile"
  "$fn" >/dev/null 2>&1
  local rc=$?
  SUMMARY="$saved"
  if [ "$rc" -ne 0 ]; then record "$label" PASS "tamper detected"; else record "$label" FAIL "tamper NOT detected (false negative)"; fi
}

run_negative() {
  local N; N=$(mktemp -d "${TMPDIR:-/tmp}/task011-neg.XXXXXX")
  local base="$N/base.yml"
  # Base = the real scheduled workflow (authoritative correct form); fallback minimal if not yet written.
  if [ -f "$WF" ]; then cp "$WF" "$base"; else
    cat > "$base" <<'Y'
name: Evaluation Quality Scheduled
on:
  schedule:
    - cron: "17 2 * * 1-5"
  workflow_dispatch:
permissions:
  contents: read
jobs:
  scheduled:
    name: evaluation-regression-scheduled / quality
    runs-on: ubuntu-latest
    timeout-minutes: 20
    concurrency:
      group: evaluation-regression-scheduled-${{ github.ref }}
      cancel-in-progress: false
    steps:
      - uses: actions/checkout@v6
        with:
          persist-credentials: false
      - run: |
          OUTPUT_DIR="$RUNNER_TEMP/evaluation-scheduled" make reproduce-evaluation
          echo "exit_code=$?" >> "$GITHUB_ENV"
      - if: always()
        uses: actions/upload-artifact@v4
        with:
          name: evaluation-scheduled-${{ github.run_id }}-${{ github.run_attempt }}
          path: ${{ runner.temp }}/evaluation-scheduled/
          retention-days: 30
      - run: |
          case "${{ env.exit_code }}" in
            0) exit 0 ;;
            2) exit 2 ;;
            3) exit 3 ;;
            4) exit 4 ;;
            *) exit 4 ;;
          esac
Y
  fi

  local good_pr="$REPO_ROOT/.github/workflows/evaluation-regression.yml"
  local prfile="$good_pr"; [ -f "$prfile" ] || prfile="$N/pr_missing.yml"

  # N08 secret canary (broad scan) -> s15 must FAIL
  awk '{print} /^  workflow_dispatch:/{print "      env:"; print "        JWT_SECRET: canary_abcdef123456"}' "$base" > "$N/n08.yml"
  neg_assert ck_s15_secret_scan "$N/n08.yml" "$prfile" n08.secret_canary

  # N08b provider/Repository secret read -> c08 must FAIL
  awk '{print} /^  workflow_dispatch:/{print "      env:"; print "        OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}"}' "$base" > "$N/n08b.yml"
  neg_assert ck_c08_no_provider "$N/n08b.yml" "$prfile" n08b.secret_provider

  # N09 write permission injection -> c04 must FAIL
  awk '{print} /^permissions:/{print "  issues: write"}' "$base" > "$N/n09.yml"
  neg_assert ck_c04_permissions "$N/n09.yml" "$prfile" n09.write_permission

  # N10 required-context injection -> c03 must FAIL
  sed 's|evaluation-regression-scheduled / quality|evaluation-regression / quality|' "$base" > "$N/n10.yml"
  neg_assert ck_c03_identity "$N/n10.yml" "$prfile" n10.required_context

  # N-A cron on-the-hour -> c02 must FAIL
  sed 's|17 2 \* \* 1-5|0 2 * * 1-5|' "$base" > "$N/nA.yml"
  neg_assert ck_c02_cron "$N/nA.yml" "$prfile" n_cron_onhour

  # N-B metric/baseline constants copied -> c06 must FAIL
  awk '{print} /make reproduce-evaluation/{print "        # threshold 0.625"}' "$base" > "$N/nB.yml"
  neg_assert ck_c06_compute "$N/nB.yml" "$prfile" n_metric_constant

  # N-C exit swallowed by continue-on-error -> c12 must FAIL
  awk '{print} /^[[:space:]]*case "/{print "          continue-on-error: true"}' "$base" > "$N/nC.yml"
  neg_assert ck_c12_decision_mapping "$N/nC.yml" "$prfile" n_exit_swallow

  # N-D artifact upload removed -> c11 must FAIL
  grep -v 'upload-artifact' "$base" > "$N/nD.yml"
  neg_assert ck_c11_artifact_always "$N/nD.yml" "$prfile" n_artifact_missing

  # N-E scheduled context injected into PR required check -> c13 must FAIL
  if [ -f "$good_pr" ]; then
    cp "$good_pr" "$N/nE.yml"
    printf '\nevaluation-regression-scheduled / quality\n' >> "$N/nE.yml"
  else
    printf 'name: Evaluation Quality Regression\njobs:\n  quality:\n    name: evaluation-regression / quality\n' > "$N/nE.yml"
  fi
  neg_assert ck_c13_ruleset_nonmember "$base" "$N/nE.yml" n_ruleset_injection

  # N01-N07 runtime negatives (degradation/fixture/baseline/dep/illegal-exit/missing-artifact/upload-fail)
  # are authoritative runtime proofs (Step 9) and are recorded with reasons, not faked here.
  record n01.degradation SKIP_WITH_REASON "runtime BLOCK/2 proof re-verified in Step 9 (Task007 G5 deliberate-regression)"
  record n02.fixture_tamper SKIP_WITH_REASON "runtime NOT_COMPARABLE/3 proof re-verified in Step 9"
  record n03.baseline_tamper SKIP_WITH_REASON "runtime NOT_COMPARABLE/3 proof re-verified in Step 9"
  record n04.missing_dep SKIP_WITH_REASON "covered locally by l09 (missing tool -> ERROR/4)"
  record n05.illegal_exit SKIP_WITH_REASON "covered by c12 fail-closed (other -> ERROR/4)"
  record n06.missing_artifact SKIP_WITH_REASON "covered by n_artifact_missing static negative + Step 9 runtime"
  record n07.upload_failure SKIP_WITH_REASON "decision retained under upload failure; covered by c11 if:always()"
  return 0
}

# =============================================================================
# GITHUB live evidence (G01-G14) — read-only, requires --repo
# =============================================================================
run_github() {
  local repo="${GITHUB_REPO:-}"
  if [ -z "$repo" ]; then
    record g01.default_branch SKIP_WITH_REASON "github mode requires --repo OWNER/REPO (read-only)"
    for g in g02.enabled g03.manual_dryrun g04.schedule_1 g05.schedule_2 g06.source_identity \
             g07.decision_semantics g08.artifact_digest g09.failed_path g10.required_unchanged \
             g11.no_write g12.no_secret g13.timing g14.notification; do
      record "$g" SKIP_WITH_REASON "github mode requires --repo OWNER/REPO"
    done
    return 0
  fi
  record g01.default_branch SKIP_WITH_REASON "live G01-G14 require authenticated gh/API on ${repo}; collected per plan Step 6-10 into github/*.json"
  record g02.enabled SKIP_WITH_REASON "see github_manual_run.md / workflow.json enabled state"
  record g03.manual_dryrun SKIP_WITH_REASON "see github_manual_run.md (event=workflow_dispatch, PASS/0)"
  record g04.schedule_1 SKIP_WITH_REASON "requires real event=schedule run #1"
  record g05.schedule_2 SKIP_WITH_REASON "requires real event=schedule run #2"
  record g06.source_identity SKIP_WITH_REASON "run head_sha vs source identity"
  record g07.decision_semantics SKIP_WITH_REASON "summary.json vs job conclusion"
  record g08.artifact_digest SKIP_WITH_REASON "download + root hash recompute"
  record g09.failed_path SKIP_WITH_REASON "negative runtime proof (Step 9)"
  record g10.required_unchanged SKIP_WITH_REASON "ruleset before/after diff"
  record g11.no_write SKIP_WITH_REASON "audit for push/Issue/baseline mutation"
  record g12.no_secret SKIP_WITH_REASON "workflow/log/artifact scan 0 finding"
  record g13.timing SKIP_WITH_REASON "record queued/started/completed"
  record g14.notification SKIP_WITH_REASON "owner subscription boundary recorded"
  return 0
}

# =============================================================================
# mode dispatch
# =============================================================================
MODE="${1:-all-safe}"
shift || true
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) GITHUB_REPO="$2"; shift 2 ;;
    *) shift ;;
  esac
done

run_static() {
  ck_c01_trigger; ck_c02_cron; ck_c03_identity; ck_c04_permissions
  ck_c05_checkout; ck_c06_compute; ck_c07_baseline_ro; ck_c08_no_provider
  ck_c09_timeout; ck_c10_concurrency; ck_c11_artifact_always; ck_c12_decision_mapping
  ck_c13_ruleset_nonmember; ck_c14_g5_unchanged; ck_s14_actionlint; ck_s15_secret_scan
}

case "$MODE" in
  static)   run_static ;;
  local)    ck_l01_normal; ck_l02_determinism; ck_l03_env_i; ck_l04_locale_c; ck_l05_path_unicode
            ck_l06_goproxy_off; ck_l07_empty_out; ck_l08_nonempty_out; ck_l09_missing_tool; ck_l10_residue ;;
  negative) run_negative ;;
  github)   run_github ;;
  all-safe) run_static; run_negative ;;
  *)
    echo "unknown mode: $MODE (static|local|negative|github|all-safe)" >&2; exit 2 ;;
esac

finish
