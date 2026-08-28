#!/usr/bin/env bash
# Task006 one-click verifier (offline-safe by default).
# Modes: static | unit | audit | provider-probe | provider-ab | all-safe
# Provider modes require an explicit --allow-provider flag and never print secrets.
set -uo pipefail

# ---- UTF-8 hygiene: Chinese repo paths must never raise UnicodeDecodeError ----
# PYTHONUTF8=1 forces Python's filesystem/stdio encoding to UTF-8, but site.py still
# reads .pth files with encoding="locale" (ascii under LC_ALL=C) and can crash on a
# non-ASCII .pth path. So we must ALSO select a real UTF-8 locale for the whole run.
export PYTHONUTF8=1
export PYTHONIOENCODING=utf-8
_locales="$(locale -a 2>/dev/null || true)"
for _loc in C.UTF-8 en_US.UTF-8 en_US.utf8 zh_CN.UTF-8 zh_CN.utf8; do
  # here-string + grep -qx (not a pipe) avoids the SIGPIPE/pipefail trap that
  # would make "locale -a | grep -q" exit non-zero on early match.
  if grep -qx "$_loc" <<< "$_locales"; then
    export LC_ALL="$_loc"
    export LANG="$_loc"
    break
  fi
done
unset _loc _locales

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
EVID="$REPO_ROOT/../status/evidence/task006"
RAW="$REPO_ROOT/../status/raw/task006"
MODE="${1:-static}"
ALLOW_PROVIDER=0
[ "${2:-}" = "--allow-provider" ] && ALLOW_PROVIDER=1

RUN_TS="$(date -u +%Y%m%d_%H%M%S)"
OUT_DIR="$EVID/automated_${MODE}_${RUN_TS}"
mkdir -p "$OUT_DIR"
SUMMARY="$OUT_DIR/summary.tsv"
{
  printf 'check\tstatus\tdetail\n'
} > "$SUMMARY"

# All verifier scratch state must stay outside the real worktree/index. Cleanup is
# idempotent so normal exit, Ctrl-C, and TERM all leave only the evidence run.
REVERSE_TMP=""
cleanup_scratch() {
  if [ -n "$REVERSE_TMP" ] && [ -d "$REVERSE_TMP" ]; then
    rm -rf -- "$REVERSE_TMP"
  fi
  REVERSE_TMP=""
}
on_signal() {
  local code="$1"
  cleanup_scratch
  trap - EXIT INT TERM
  exit "$code"
}
trap cleanup_scratch EXIT
trap 'on_signal 130' INT
trap 'on_signal 143' TERM

pass=0; fail=0; skip=0
rec() { # name status detail
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$SUMMARY"
  case "$2" in PASS) pass=$((pass+1));; FAIL) fail=$((fail+1));; SKIP) skip=$((skip+1));; esac
}

started=$(date +%s)

# ---- secret scan (structural patterns only; no live literal hardcoded) ----
secret_scan() {
  local hits=0
  local pats=(
    'eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}'
    '(?i)bearer[[:space:]]+[A-Za-z0-9._-]{16,}'
    'sk-[A-Za-z0-9]{16,}'
    '(?i)(api[_-]?key|app[_-]?secret|jwt[_-]?secret|system[_-]?aes[_-]?key|db[_-]?password|secret)[[:space:]]*[:=][[:space:]]*["'"'"']?[A-Za-z0-9+/._-]{12,}'
    '(?i)password[[:space:]]*[:=][[:space:]]*["'"'"'][^"'"'"']{4,}["'"'"']'
    '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
  )
  : > "$OUT_DIR/secret_scan.log"
  while IFS= read -r -d '' f; do
    for p in "${pats[@]}"; do
      if grep -Eq "$p" "$f" 2>/dev/null; then
        snippet="$(grep -Eo "$p" "$f" | head -1 | cut -c1-48)"
        echo "PATTERN  ${f#$EVID/}  :: ${snippet}..." >> "$OUT_DIR/secret_scan.log"
        hits=$((hits+1))
      fi
    done
  done < <(find "$EVID" -type f \( -name '*.md' -o -name '*.yaml' -o -name '*.log' -o -name '*.tsv' -o -name '*.json' -o -name '*.sha256' -o -name '*.sh' \) -print0 2>/dev/null)
  echo "$hits"
}

# ---- residue scan ----
residue_scan() {
  local issues=0
  : > "$OUT_DIR/residue_scan.log"
  if [ -d "$RAW" ]; then
    local n
    n="$(find "$RAW" -type f 2>/dev/null | wc -l | tr -d ' ')"
    if [ "$n" -gt 0 ]; then echo "RAW_FILES $n" >> "$OUT_DIR/residue_scan.log"; issues=$((issues+n)); fi
  fi
  local bin
  bin="$(find "$EVID" -type f -size +1M 2>/dev/null | wc -l | tr -d ' ')"
  if [ "$bin" -gt 0 ]; then echo "LARGE_FILES $bin" >> "$OUT_DIR/residue_scan.log"; issues=$((issues+bin)); fi
  local roots=""
  roots="$(find "$REPO_ROOT" -maxdepth 1 -type f \( -name 'server' -o -name 'repo_parity' \) 2>/dev/null)"
  if [ -n "$roots" ]; then echo "$roots" >> "$OUT_DIR/residue_scan.log"; issues=$((issues+$(echo "$roots" | wc -l | tr -d ' '))); fi
  echo "$issues"
}

run_static() {
  local h
  h="$(secret_scan)"
  if [ "$h" -eq 0 ]; then rec secret_scan PASS "0 finding"; cp "$OUT_DIR/secret_scan.log" "$EVID/secret_prompt_scan.log"; else rec secret_scan FAIL "$h finding(s)"; fi
  local r
  r="$(residue_scan)"
  if [ "$r" -eq 0 ]; then rec residue_scan PASS "no residue"; cp "$OUT_DIR/residue_scan.log" "$EVID/residue_scan.log"; else rec residue_scan FAIL "$r residue"; fi
  if git -C "$REPO_ROOT" diff --check >/dev/null 2>&1; then rec git_diff_check PASS "exit 0"; else rec git_diff_check FAIL "whitespace error"; fi
}

run_identity() {
  local tracked
  tracked="$(git -C "$REPO_ROOT" diff | shasum -a 256 | cut -d' ' -f1)"
  if [ "$tracked" = "31f9775fd9e3f05a1bdcce643d1573f5a2e1fa919a993a48227bf1e96152a5be" ]; then
    rec identity_tracked PASS "completion pinned (R8 row; base 4c872af1 frozen)"
  else
    rec identity_tracked FAIL "tracked hash drift: $tracked"
  fi

  local untracked
  untracked="$(git -C "$REPO_ROOT" -c core.quotepath=false ls-files --others --exclude-standard | sort | grep -v 'scripts/tmpCheck/task006/verify_task006.sh' | while IFS= read -r f; do printf 'FILE %s\n' "$f"; cat "$f"; done | shasum -a 256 | cut -d' ' -f1)"
  if [ "$untracked" = "63a84a9d8f1cd96ec26d929f0601ea370ca17bdb4c8c4d0c7e2d302d5171e176" ]; then
    rec identity_untracked PASS "harness/source untracked == base 63a84a9d (excl. verifier)"
  else
    rec identity_untracked FAIL "untracked drift: $untracked"
  fi

  local prereg want
  prereg="$(shasum -a 256 "$EVID/experiment_preregistration.yaml" | cut -d' ' -f1)"
  want="$(tr -d '[:space:]' < "$EVID/experiment_preregistration.sha256")"
  if [ "$prereg" = "$want" ]; then
    rec preregistration_checksum PASS "yaml == .sha256 ($want)"
  else
    rec preregistration_checksum FAIL "preregistration drift: $prereg != $want"
  fi

  if python3 - "$REPO_ROOT" "$EVID/fixture_manifest.json" "$OUT_DIR/fixture_file_hashes.log" <<'PY'
import json, hashlib, sys
repo, manifest_path, log_path = sys.argv[1], sys.argv[2], sys.argv[3]
m = json.load(open(manifest_path))
entries = m.get("per_file_sha256", {})
ok = True
lines = []
for path in sorted(entries):
    want = entries[path]
    try:
        data = open(repo + "/" + path, "rb").read()
    except FileNotFoundError:
        lines.append("DRIFT %s want=%s got=MISSING" % (path, want)); ok = False; continue
    got = hashlib.sha256(data).hexdigest()
    if got != want:
        lines.append("DRIFT %s want=%s got=%s" % (path, want, got)); ok = False
    else:
        lines.append("OK %s" % path)
open(log_path, "w").write("\n".join(lines) + "\n")
sys.exit(0 if ok else 1)
PY
  then
    rec fixture_file_hashes PASS "per-file sha256 all match"
  else
    rec fixture_file_hashes FAIL "fixture drift (see fixture_file_hashes.log)"
  fi

  local evh want_evh
  evh="$( (cd "$REPO_ROOT" && printf '%s\n' internal/agent/prompts_wiki.go internal/types/prompt_instructions.go internal/agent/prompts_wiki_layout_test.go internal/agent/legacy_wiki_modify_prompt_test.go internal/models/chat/prompt_cache.go | sort | while IFS= read -r f; do printf 'FILE %s\n' "$f"; cat "$f"; done) | shasum -a 256 | cut -d' ' -f1 )"
  want_evh="$(grep -oE 'evaluator_artifact_hash:[[:space:]]*[0-9a-f]{64}' "$EVID/experiment_preregistration_amendment_001.yaml" | grep -oE '[0-9a-f]{64}' | head -1)"
  if [ -n "$want_evh" ] && [ "$evh" = "$want_evh" ]; then
    rec evaluator_artifact_hash PASS "$evh"
  else
    rec evaluator_artifact_hash FAIL "evaluator drift: got $evh want $want_evh"
  fi

  # Exact, non-mutating reverse-patch. A temporary index stages the current
  # tracked worktree, then replaces only requirement_matrix.md with an R8-reverted
  # blob stored in a temporary object database. The real worktree, real index and
  # real object database are never modified.
  local tracked_rev git_dir common_dir real_objects temp_index temp_objects temp_matrix matrix_blob matrix_mode
  REVERSE_TMP="$(mktemp -d "$OUT_DIR/reverse_patch.XXXXXX")" || {
    rec completion_reverse_patch FAIL "could not allocate isolated reverse-patch workspace"
    return
  }
  temp_index="$REVERSE_TMP/index"
  temp_objects="$REVERSE_TMP/objects"
  temp_matrix="$REVERSE_TMP/requirement_matrix.md"
  git_dir="$(git -C "$REPO_ROOT" rev-parse --absolute-git-dir 2>/dev/null)" || {
    rec completion_reverse_patch FAIL "could not resolve git directory"
    cleanup_scratch
    return
  }
  common_dir="$(git -C "$REPO_ROOT" rev-parse --git-common-dir 2>/dev/null)" || {
    rec completion_reverse_patch FAIL "could not resolve git common directory"
    cleanup_scratch
    return
  }
  case "$common_dir" in
    /*) ;;
    *) common_dir="$REPO_ROOT/$common_dir" ;;
  esac
  real_objects="$common_dir/objects"

  if ! mkdir -p "$temp_objects" \
    || ! cp "$git_dir/index" "$temp_index" \
    || ! cp "$REPO_ROOT/docs/requirement_matrix.md" "$temp_matrix"; then
    rec completion_reverse_patch FAIL "could not initialize isolated reverse-patch workspace"
    cleanup_scratch
    return
  fi

  if ! python3 - "$temp_matrix" <<'PY'
import sys
path = sys.argv[1]
data = open(path, encoding="utf-8").read().split("\n")
done = False
for i, line in enumerate(data):
    if line.startswith("| R8 |"):
        cells = line.split("|")
        # header: | ID | 官方要求 | 工程问题 | 实现 | 验证 | 最终证据 | Priority | Owner | Status | Code Location | Open Risk |
        # split("|") yields a leading empty element, so Status is index 9 (Owner=8).
        cells[9] = " NOT_STARTED "
        data[i] = "|".join(cells)
        done = True
        break
if not done:
    sys.stderr.write("R8 row not found\n")
    sys.exit(2)
open(path, "w", encoding="utf-8").write("\n".join(data))
PY
  then
    rec completion_reverse_patch FAIL "could not locate R8 row for reverse-patch"
    cleanup_scratch
    return
  fi

  matrix_mode="$(git -C "$REPO_ROOT" ls-files -s docs/requirement_matrix.md | awk 'NR == 1 {print $1}')"
  if [ -z "$matrix_mode" ] \
    || ! GIT_INDEX_FILE="$temp_index" GIT_OBJECT_DIRECTORY="$temp_objects" GIT_ALTERNATE_OBJECT_DIRECTORIES="$real_objects" git -C "$REPO_ROOT" add -u \
    || ! matrix_blob="$(GIT_INDEX_FILE="$temp_index" GIT_OBJECT_DIRECTORY="$temp_objects" GIT_ALTERNATE_OBJECT_DIRECTORIES="$real_objects" git -C "$REPO_ROOT" hash-object -w "$temp_matrix")" \
    || ! GIT_INDEX_FILE="$temp_index" GIT_OBJECT_DIRECTORY="$temp_objects" GIT_ALTERNATE_OBJECT_DIRECTORIES="$real_objects" git -C "$REPO_ROOT" update-index --cacheinfo "$matrix_mode,$matrix_blob,docs/requirement_matrix.md"; then
    rec completion_reverse_patch FAIL "could not build isolated synthetic index"
    cleanup_scratch
    return
  fi

  tracked_rev="$(GIT_INDEX_FILE="$temp_index" GIT_OBJECT_DIRECTORY="$temp_objects" GIT_ALTERNATE_OBJECT_DIRECTORIES="$real_objects" git -C "$REPO_ROOT" diff --cached | shasum -a 256 | cut -d' ' -f1)"
  cleanup_scratch
  if [ "$tracked_rev" = "4c872af13e99557bf59bd834017e989a9c987fce77492990d1a1ce5bba2011b2" ]; then
    rec completion_reverse_patch PASS "isolated R8 revert reproduces base 4c872af1"
  else
    rec completion_reverse_patch FAIL "isolated R8 revert != base: $tracked_rev"
  fi
}

run_unit() {
  local repro="$OUT_DIR/layout_repro"
  mkdir -p "$repro"
  if TASK006_EVIDENCE_DIR="$repro" go test ./internal/agent/ -run TestWikiLayout -count=1 >/dev/null 2>&1; then
    rec unit_layout PASS "9 layout tests"
    if [ -f "$repro/layout_stability_raw.tsv" ] && diff -q "$repro/layout_stability_raw.tsv" "$EVID/layout_stability_raw.tsv" >/dev/null 2>&1; then
      rec layout_output_reproduction PASS "tsv byte-identical to committed"
    else
      rec layout_output_reproduction FAIL "layout_stability_raw.tsv drift"
    fi
  else
    rec unit_layout FAIL "layout harness"
    rec layout_output_reproduction FAIL "unit layout failed"
  fi
  if go test ./internal/models/chat/ -count=1 >/dev/null 2>&1; then rec unit_usage PASS "chat usage"; else rec unit_usage FAIL "chat usage"; fi
  if go test ./internal/application/repository/ -run ModelCall -count=1 >/dev/null 2>&1; then rec unit_denominator_repo PASS "model_call"; else rec unit_denominator_repo FAIL "model_call"; fi
  if go test ./internal/application/service/ -run 'ModelUsage|AwaitWikiPromptWarmup|CoalescesIdentical' -count=1 >/dev/null 2>&1; then rec unit_denominator_service PASS "model_usage/warmup"; else rec unit_denominator_service FAIL "model_usage/warmup"; fi
}

run_audit() {
  local required=(README.md preflight.md source_identity.md source_trace.md prompt_coverage_matrix.md
    provider_capability_matrix.md retry_streaming_audit.md experiment_preregistration.yaml
    experiment_preregistration.sha256 experiment_preregistration_amendment_001.yaml fixture_manifest.json layout_contract.md layout_stability_raw.tsv
    layout_stability_report.md usage_contract_regression.log provider_probe.md provider_probe_sanitized.json
    provider_ab_report.md quality_report.md measurement_completeness.md alternative_explanations.md
    production_change_review.md regression.log race.log vet.log secret_prompt_scan.log residue_scan.log
    commands.log task006_review.md)
  local missing=0
  for f in "${required[@]}"; do
    if [ ! -f "$EVID/$f" ]; then rec audit_file_MISSING FAIL "$f"; missing=$((missing+1)); fi
  done
  if [ "$missing" -eq 0 ]; then rec audit_files PASS "all evidence files present"; else rec audit_files FAIL "$missing missing"; fi
  local forbidden
  forbidden="$(grep -rEl '(cache_)?hit_?rate[[:space:]]*[:=][[:space:]]*[0-9]' "$EVID" 2>/dev/null | wc -l | tr -d ' ')"
  if [ "$forbidden" -eq 0 ]; then rec audit_forbidden PASS "no numeric hit-rate claim"; else rec audit_forbidden FAIL "$forbidden file(s) publish numeric hit-rate"; fi
  if grep -q "4c872af13e99557bf59bd834017e989a9c987fce77492990d1a1ce5bba2011b2" "$EVID/source_identity.md"; then rec audit_identity PASS "frozen identity referenced"; else rec audit_identity FAIL "identity not referenced"; fi
}

case "$MODE" in
  static) run_static; run_identity ;;
  unit) run_unit ;;
  audit) run_audit ;;
  provider-probe)
    if [ "$ALLOW_PROVIDER" -eq 1 ]; then rec provider_probe SKIP "allow-provider set but no approved profile/budget; no external call made"; else rec provider_probe SKIP "offline (no --allow-provider)"; fi ;;
  provider-ab)
    if [ "$ALLOW_PROVIDER" -eq 1 ]; then rec provider_ab SKIP "allow-provider set but Step 5 = UNSUPPORTED"; else rec provider_ab SKIP "offline (no --allow-provider)"; fi ;;
  all-safe) run_static; run_identity; run_unit; run_audit ;;
  *) echo "unknown mode: $MODE" >&2; exit 2 ;;
esac

elapsed=$(( $(date +%s) - started ))
{
  echo "# Task006 verify_task006.sh mode=$MODE allow_provider=$ALLOW_PROVIDER"
  echo "# run: $(date -u +%Y-%m-%dT%H:%M:%SZ)  elapsed=${elapsed}s"
  echo "# base: WeKnora@55b8c99 tracked=4c872af13e99557bf59bd834017e989a9c987fce77492990d1a1ce5bba2011b2 untracked=63a84a9d8f1cd96ec26d929f0601ea370ca17bdb4c8c4d0c7e2d302d5171e176"
  echo "# completion: tracked=31f9775fd9e3f05a1bdcce643d1573f5a2e1fa919a993a48227bf1e96152a5be"
  echo "# exit: $fail failures, $skip skipped, $pass passed"
} >> "$SUMMARY"

echo "mode=$MODE allow_provider=$ALLOW_PROVIDER pass=$pass fail=$fail skip=$skip elapsed=${elapsed}s out=$OUT_DIR"
exit $fail
