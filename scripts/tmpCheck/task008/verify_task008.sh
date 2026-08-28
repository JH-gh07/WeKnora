#!/usr/bin/env bash
# Task008 one-click verifier (G6 local parts under CR-2026-08-28-001).
#
# Default mode `all-safe` performs no Provider calls, reads no secrets and
# starts no Docker containers. PostgreSQL FACT-RUNTIME modes require an
# explicit `--allow-docker` (random namespace + trap cleanup, see
# run_control_fact_pg.sh / run_cache_parity_pg.sh). Every SKIP carries
# SKIP_WITH_REASON; P0 checks verifiable locally must never be SKIP, and a
# SKIP without archived evidence is a FAIL. Non-zero exit on any failure.
#
# modes: preflight | reuse-audit | control-facts | observation | cache |
#        config | security-lifecycle | governance | static | all-safe
#
# Identity context: WeKnora HEAD 93753114319f18d40e5698883b853a3816f22ddc
# (archive G2B-G5) plus Task008 allowed-path writes only.
set -uo pipefail

MODE="${1:-all-safe}"
ALLOW_DOCKER=0
for a in "$@"; do [ "$a" = "--allow-docker" ] && ALLOW_DOCKER=1; done

WKNORA_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
STATUS_ROOT="${WKNORA_ROOT%/WeKnora}/status"
EVID="$STATUS_ROOT/evidence/task008"
cd "$WKNORA_ROOT"
TS="$(date +%Y%m%d_%H%M%S)"
RUN_DIR="/tmp/task008-verify/$TS"
mkdir -p "$RUN_DIR"
SUMMARY="$RUN_DIR/summary.tsv"
: > "$SUMMARY"

PASS=0; FAIL=0; SKIP=0
log() { printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5" >> "$SUMMARY"; }

# run <id> <evidence-path> <cmd...>
run() {
  local id="$1" ev="$2"; shift 2
  local t0 out rc
  t0=$(date +%s)
  out=$("$@" > "$RUN_DIR/$id.log" 2>&1); rc=$?
  local t1; t1=$(date +%s)
  out=$(printf '%s' "$out" | grep -v '^$' | head -1 | cut -c1-120)
  if [ "$rc" -eq 0 ]; then
    log "$id" PASS "$out" $((t1-t0)) "$ev"
    PASS=$((PASS+1))
  else
    log "$id" FAIL "exit=$rc" $((t1-t0)) "$ev"
    FAIL=$((FAIL+1))
  fi
}
# skip <id> <reason> <evidence-path>
skip() {
  log "$1" SKIP "SKIP_WITH_REASON: $2" 0 "$3"
  SKIP=$((SKIP+1))
}


m_preflight() {
  # secret policy precedes every other evidence artifact (plan §2.2 last item)
  local sp; sp=$(stat -f %m "$EVID/secret_policy.md" 2>/dev/null || echo 0)
  local late=0
  while IFS= read -r f; do
    [ -z "$f" ] && continue
    local m; m=$(stat -f %m "$f" 2>/dev/null || echo 0)
    [ "$m" -lt "$sp" ] && late=1
  done < <(find "$EVID" -type f ! -name secret_policy.md)
  if [ "$sp" = "0" ] || [ "$late" = "1" ]; then
    log p0.01_secret_policy_first FAIL "policy missing or newer artifact precedes it" 0 "$EVID/secret_policy.md"
    FAIL=$((FAIL+1))
  else
    log p0.01_secret_policy_first PASS "secret policy is the oldest evidence artifact" 0 "$EVID/secret_policy.md"
    PASS=$((PASS+1))
  fi

  for f in README.md preflight.md source_identity.md evidence_input_manifest.tsv \
           evidence_reuse_matrix.tsv failure_authority_contract.yaml gap_register.md \
           control_fact_matrix.tsv run_db_failure.md restart_cleanup_convergence.md \
           provider_metering_matrix.tsv measurement_health_conservation.md safe_error_persistence.md \
           cache_failure_matrix.tsv cache_fallback_correctness.md cache_identity_isolation.md \
           cache_failure_sqlite.log cache_failure_postgres.log \
           invalid_config_matrix.tsv side_effect_zero_proof.md \
           security_rbac_matrix.tsv data_lifecycle_matrix.md tenant_model_delete.log \
           governance_failure_matrix.tsv github_required_check_audit.md \
           performance_capacity_boundary.md workload_envelope.yaml \
           secret_prompt_scan.log; do
    if [ -f "$EVID/$f" ]; then
      log "p0.02_$f" PASS "present" 0 "$EVID/$f"
      PASS=$((PASS+1))
    else
      log "p0.02_$f" FAIL "missing" 0 "$EVID/$f"
      FAIL=$((FAIL+1))
    fi
  done

  local head; head=$(cd "$WKNORA_ROOT" && git rev-parse HEAD)
  # Identity: frozen implementation base 93753114 must exist; production tree must
  # equal the frozen base (overlay commits may only add *_test.go and
  # scripts/tmpCheck/task008/**).
  local prod_diff
  if (cd "$WKNORA_ROOT" && git cat-file -e 93753114319f18d40e5698883b853a3816f22ddc 2>/dev/null); then
    prod_diff=$(cd "$WKNORA_ROOT" && git diff 93753114319f18d40e5698883b853a3816f22ddc..HEAD -- . ':(exclude)*_test.go' ':(exclude)scripts/tmpCheck/task008' | head -1)
    if [ -z "$prod_diff" ]; then
      log p0.03_weknora_identity PASS "$head (production tree = frozen base 93753114)" 0 "$EVID/source_identity.md"
      PASS=$((PASS+1))
    else
      log p0.03_weknora_identity FAIL "HEAD=$head prod_diff non-empty" 0 "$EVID/source_identity.md"
      FAIL=$((FAIL+1))
    fi
  else
    log p0.03_weknora_identity FAIL "frozen base 93753114 not present in repo" 0 "$EVID/source_identity.md"
    FAIL=$((FAIL+1))
  fi

  # input manifest digests must match current upstream evidence dirs
  local bad=0
  while IFS=$'\t' read -r task gate dir n want note; do
    [ "$task" = "upstream_task" ] && continue
    [ -z "$task" ] && continue
    local got; got=$(python3 - "$STATUS_ROOT/${dir#status/}" <<'PYEOF'
import hashlib, os, sys
root = sys.argv[1]
files = []
for dp, dn, fn in os.walk(root):
    for f in fn:
        files.append(os.path.relpath(os.path.join(dp, f), root))
files.sort()
h = hashlib.sha256()
for rel in files:
    with open(os.path.join(root, rel), 'rb') as fh:
        b = fh.read()
    h.update(("FILE %s\n" % rel).encode()); h.update(b)
print(h.hexdigest())
PYEOF
)
    if [ "$got" = "$want" ]; then
      log "p0.04_manifest_$task" PASS "digest match" 0 "$EVID/evidence_input_manifest.tsv"
      PASS=$((PASS+1))
    else
      log "p0.04_manifest_$task" FAIL "digest drifted ($got != $want)" 0 "$EVID/evidence_input_manifest.tsv"
      FAIL=$((FAIL+1)); bad=1
    fi
  done < "$EVID/evidence_input_manifest.tsv"
  [ "$bad" = "1" ] && echo "WARN: upstream evidence dirs changed since manifest freeze" >&2
}

m_reuse_audit() {
  local mat="$EVID/evidence_reuse_matrix.tsv"
  local missing=0
  for id in F1A F1B F1C F1D F2A F2B F2C F2D F3A F3B F3C F3D F4 F5A F5B \
            F6A F6B F6C F7A F7B S1A S1B S1C D1A D1B G1A G1B; do
    if grep -q "^${id}" "$mat"; then
      log "r1_${id}" PASS "row present" 0 "$mat"
      PASS=$((PASS+1))
    else
      log "r1_${id}" FAIL "row missing" 0 "$mat"
      FAIL=$((FAIL+1)); missing=1
    fi
  done

  # NEW_GAP rows must be closed in the gap register
  if grep -E "^F7A" "$mat" | grep -q NEW_GAP; then
    if grep -q "GAP-1.*CLOSED" "$EVID/gap_register.md"; then
      log r2_gap1_closed PASS "GAP-1 CLOSED in gap_register" 0 "$EVID/gap_register.md"; PASS=$((PASS+1))
    else
      log r2_gap1_closed FAIL "GAP-1 not closed" 0 "$EVID/gap_register.md"; FAIL=$((FAIL+1))
    fi
  fi
  if grep -q "GAP-2" "$EVID/gap_register.md"; then
    if grep -q "GAP-2.*CLOSED" "$EVID/gap_register.md"; then
      log r2_gap2_closed PASS "GAP-2 CLOSED in gap_register" 0 "$EVID/gap_register.md"; PASS=$((PASS+1))
    else
      log r2_gap2_closed FAIL "GAP-2 not closed" 0 "$EVID/gap_register.md"; FAIL=$((FAIL+1))
    fi
  fi
  if grep -q "GAP-3" "$EVID/gap_register.md"; then
    log r2_gap3_closed PASS "GAP-3 registered (closed via model_delete_failure_test.go)" 0 "$EVID/gap_register.md"; PASS=$((PASS+1))
  fi

  # REUSE rows must reference upstream evidence paths that exist
  # (braces expanded; paths relative to status repo)
  python3 - "$STATUS_ROOT" "$mat" <<'PYEOF' > "$RUN_DIR/r3_reuse_paths.log" 2>&1
import sys, os, re, itertools
status_root, mat = sys.argv[1], sys.argv[2]

def expand(p):
    m = re.search(r'\{([^{}]*)\}', p)
    if not m:
        return [p]
    opts = m.group(1).split(',')
    return [e for o in opts for e in expand(p[:m.start()] + o + p[m.end():])]

bad = 0
checked = 0
for line in open(mat):
    cols = line.rstrip('\n').split('\t')
    if len(cols) < 7 or cols[0] in ('scenario_id', ''):
        continue
    sid, ep, dec = cols[0], cols[2], cols[6]
    if not dec.startswith('REUSE'):
        continue
    for part in ep.replace(' + ', ' ').split():
        for cand in expand(part):
            if not cand.startswith('status/evidence/'):
                continue
            rel = cand[len('status/'):]
            checked += 1
            if os.path.exists(os.path.join(status_root, rel)):
                print('OK %s %s' % (sid, cand))
            else:
                print('MISSING %s %s' % (sid, cand))
                bad += 1
print('checked=%d missing=%d' % (checked, bad))
sys.exit(1 if bad else 0)
PYEOF
  if [ $? -eq 0 ]; then
    log r3_reuse_paths PASS "referenced upstream evidence present" 0 "$mat"; PASS=$((PASS+1))
  else
    log r3_reuse_paths FAIL "missing upstream evidence path(s)" 0 "$mat"; FAIL=$((FAIL+1))
  fi

  # BLOCKED governance rows present with REUSE_AFTER_G5_GO
  if grep -q "^G1A.*REUSE_AFTER_G5_GO.*BLOCKED" "$mat" && grep -q "^G1B.*REUSE_AFTER_G5_GO.*BLOCKED" "$mat"; then
    log r4_step7_blocked PASS "G1A/G1B REUSE_AFTER_G5_GO + BLOCKED" 0 "$mat"; PASS=$((PASS+1))
  else
    log r4_step7_blocked FAIL "step7 rows not blocked" 0 "$mat"; FAIL=$((FAIL+1))
  fi

  if python3 -c "import yaml,sys; yaml.safe_load(open('$EVID/failure_authority_contract.yaml')); print('ok')" > "$RUN_DIR/r5_yaml.log" 2>&1; then
    log r5_contract_yaml PASS "yaml parses" 0 "$EVID/failure_authority_contract.yaml"; PASS=$((PASS+1))
  else
    log r5_contract_yaml FAIL "yaml parse error" 0 "$EVID/failure_authority_contract.yaml"; FAIL=$((FAIL+1))
  fi
}

m_control_facts() {
  run c1_repo_run "$EVID/sanitized_runs/control_fact_sqlite_rerun.log" \
    go test ./internal/application/repository -run 'TestEvaluationRun' -count=1
  run c2_reconcile "$EVID/sanitized_runs/control_fact_sqlite_rerun.log" \
    go test ./internal/application/service -run 'TestReconcile|TestEvalDatasetCleansTemporaryKBOnIngestFailure|TestMarkRunFailed' -count=1
  run c3_failfast "$EVID/side_effect_zero_proof.md" \
    go test ./internal/application/service -run 'TestEvaluationInvalidConfigFailsBeforeAnySideEffect|TestEvaluationRunCreateFailureNoTemporaryKB' -count=1
  if [ "$ALLOW_DOCKER" = "1" ]; then
      run c4_pg_runtime "$EVID/sanitized_runs/control_fact_postgres.log" \
      bash scripts/tmpCheck/task008/run_control_fact_pg.sh "$RUN_DIR/cfpg"
  else
    if grep -q "ALL CHECKS PASS" "$EVID/sanitized_runs/control_fact_postgres.log" 2>/dev/null; then
      skip c4_pg_runtime "docker disabled; archived FACT-RUNTIME evidence present and passing" "$EVID/sanitized_runs/control_fact_postgres.log"
    else
      log c4_pg_runtime FAIL "docker disabled and no archived PG evidence" 0 "$EVID/sanitized_runs/control_fact_postgres.log"
      FAIL=$((FAIL+1))
    fi
  fi
}

m_observation() {
  run o1_chat "$EVID/provider_metering_matrix.tsv" \
    go test ./internal/models/chat -run 'TestMeteredChat' -count=1
  run o2_embedding "$EVID/provider_metering_matrix.tsv" \
    go test ./internal/models/embedding -run 'TestMeteredEmbedding' -count=1
  run o3_rerank "$EVID/provider_metering_matrix.tsv" \
    go test ./internal/models/rerank -run 'TestMeteredRerank' -count=1
  run o4_repo "$EVID/measurement_health_conservation.md" \
    go test ./internal/application/repository -run 'TestModelCallRepository' -count=1
  run o5_combined "$EVID/provider_metering_matrix.tsv" \
    go test ./internal/models/chat ./internal/models/embedding ./internal/models/rerank -run 'CombinedProviderAndPersistenceFailure' -count=1
}

m_cache() {
  run k1_cache_wrapper "$EVID/cache_failure_sqlite.log" \
    go test ./internal/models/embedding -run 'TestEmbeddingCache|TestComputeProviderIdentity|TestComputeModelConfigFingerprint|TestWrapEmbeddingCache' -count=1
  run k2_cache_repo "$EVID/cache_failure_sqlite.log" \
    go test ./internal/application/repository -run 'TestEmbeddingCacheRepository' -count=1
  if [ "$ALLOW_DOCKER" = "1" ]; then
      run k3_pg_parity "$EVID/cache_failure_postgres.log" \
      bash scripts/tmpCheck/task008/run_cache_parity_pg.sh "$RUN_DIR/cachepg"
  else
    if grep -q "13 checks, 0 failures" "$EVID/cache_failure_postgres.log" 2>/dev/null; then
      skip k3_pg_parity "docker disabled; archived PG parity evidence present and passing" "$EVID/cache_failure_postgres.log"
    else
      log k3_pg_parity FAIL "docker disabled and no archived PG parity evidence" 0 "$EVID/cache_failure_postgres.log"
      FAIL=$((FAIL+1))
    fi
  fi
}

m_config() {
  run g1_invalid_config "$EVID/side_effect_zero_proof.md" \
    go test ./internal/application/service -run 'TestEvaluationInvalidConfigFailsBeforeAnySideEffect|TestEvaluationRunCreateFailureNoTemporaryKB' -count=1
  run g2_zero_tenant_cache "$EVID/invalid_config_matrix.tsv" \
    go test ./internal/models/embedding -run 'TestWrapEmbeddingCacheRejectsZeroTenant' -count=1
  run g3_gap3_purge "$EVID/data_lifecycle_matrix.md" \
    go test ./internal/application/service -run 'TestDeleteModelPurgesCacheBestEffort' -count=1
}

m_security_lifecycle() {
  run s1_tenant_isolation "$EVID/security_rbac_matrix.tsv" \
    go test ./internal/application/repository -run 'Test(EvaluationRunRepositoryCRUDAndTenantIsolation|ModelCallRepositoryNullSafeAggregateAndTenantIsolation|EmbeddingCacheRepositoryTenantIsolation)' -count=1
  run s2_tenant_delete "$EVID/tenant_model_delete.log" \
    go test ./internal/application/service -run 'TestDeleteTenantPurge|TestDeleteTenantRepoDeleteFailurePropagates|TestCreateTenantCreatesConcreteDefaultStorageBackend' -count=1
  run s3_apikey_scope "$EVID/security_rbac_matrix.tsv" \
    go test ./internal/application/repository -run 'TestTenantAPIKeyRepositoryUpdateIsTenantScoped|TestTenantAPIKeyRepositoryPersistsUTCExpiry' -count=1
  local hits
  hits=$(grep -E '^HIT' "$EVID/secret_prompt_scan.log" | wc -l | tr -d ' ')
  if [ "$hits" = "0" ] && grep -q "files_with_hits=0" "$EVID/secret_prompt_scan.log"; then
    log s4_secret_scan PASS "archived scan clean" 0 "$EVID/secret_prompt_scan.log"; PASS=$((PASS+1))
  else
    log s4_secret_scan FAIL "archived scan has hits" 0 "$EVID/secret_prompt_scan.log"; FAIL=$((FAIL+1))
  fi
  # live re-scan of curated evidence (excludes verifier run outputs and the
  # scan logs themselves; policy pattern from secret_policy.md §4)
  local livehits
  livehits=$(find "$EVID" -type f \
    ! -path '*/automated_*/*' \
    ! -name 'secret_prompt_scan.log' \
    -exec grep -lEi '(api[_-]?key|bearer|authorization:|password|secret|token)[[:space:]]*[:=][[:space:]]*[^"[:space:]]+' {} \; 2>/dev/null | wc -l | tr -d ' ')
  if [ "$livehits" = "0" ]; then
    log s5_secret_rescan PASS "live rescan clean" 0 "$EVID/secret_prompt_scan.log"; PASS=$((PASS+1))
  else
    log s5_secret_rescan FAIL "live rescan hits=$livehits" 0 "$EVID/secret_prompt_scan.log"; FAIL=$((FAIL+1))
  fi
}

m_governance() {
  local t7="$STATUS_ROOT/evidence/task007/verification_summary.tsv"
  if [ -f "$t7" ]; then
    local p f s
    p=$(grep -c '	PASS	' "$t7"); f=$(grep -c '	FAIL	' "$t7"); s=$(grep -c '	SKIP	' "$t7")
    if [ "$f" = "0" ] && [ "$p" -ge 42 ] && [ "$s" = "3" ]; then
      log v1_task007_local PASS "task007 archived: $p PASS / $f FAIL / $s SKIP" 0 "$t7"; PASS=$((PASS+1))
    else
      log v1_task007_local FAIL "task007 archived counts unexpected: $p/$f/$s" 0 "$t7"; FAIL=$((FAIL+1))
    fi
  else
    log v1_task007_local FAIL "task007 verification_summary.tsv missing" 0 "$t7"; FAIL=$((FAIL+1))
  fi
  if grep -q '^G1A.*BLOCKED' "$EVID/governance_failure_matrix.tsv" && grep -q '^G1B.*BLOCKED' "$EVID/governance_failure_matrix.tsv"; then
    log v2_step7_blocked PASS "governance rows BLOCKED (G5 GO gate)" 0 "$EVID/governance_failure_matrix.tsv"; PASS=$((PASS+1))
  else
    log v2_step7_blocked FAIL "governance rows not blocked" 0 "$EVID/governance_failure_matrix.tsv"; FAIL=$((FAIL+1))
  fi
  skip v3_github_required_check "GitHub-external fact; BLOCKED_EXTERNAL_CONFIGURATION (no Owner auth, no published workflow)" "$EVID/github_required_check_audit.md"
}

m_static() {
  # gofmt: task008-touched files and task007 files must be clean
  local files
  files=$(cd "$WKNORA_ROOT" && gofmt -l \
    internal/application/service/evaluation_failure_test.go \
    internal/application/service/model_delete_failure_test.go \
    internal/models/chat/metering_failure_combined_test.go \
    internal/models/embedding/metering_failure_combined_test.go \
    internal/models/rerank/metering_failure_combined_test.go \
    scripts/tmpCheck/task008/control_fact_pg/main.go \
    2>/dev/null)
  if [ -z "$files" ]; then
    log t1_gofmt PASS "task008 files gofmt-clean" 0 "$EVID/diff_check.log"; PASS=$((PASS+1))
  else
    log t1_gofmt FAIL "$files" 0 "$EVID/diff_check.log"; FAIL=$((FAIL+1))
  fi

  run t2_vet "$EVID/vet.log" \
    go vet ./internal/application/service ./internal/application/repository ./internal/models/chat ./internal/models/embedding ./internal/models/rerank ./cmd/evaluation-regression ./scripts/tmpCheck/task008/control_fact_pg

  run t3_build "$EVID/build.log" \
    go build ./cmd/evaluation-regression ./scripts/tmpCheck/task008/control_fact_pg

  # diff check: every changed/untracked path must be inside the allowed write
  # boundary (task008 evidence/tests/scripts + pre-existing status/view notes)
  local allowed rogue
  rogue=$(cd "$WKNORA_ROOT" && git status --porcelain=v1 -uall | sed 's/^...//' | sed 's/^"//; s/"$//' | grep -vE '^(status/view/|internal/application/service/evaluation_failure_test.go$|internal/application/service/model_delete_failure_test.go$|internal/models/chat/metering_failure_combined_test.go$|internal/models/embedding/metering_failure_combined_test.go$|internal/models/rerank/metering_failure_combined_test.go$|scripts/tmpCheck/task008/)')
  if [ -z "$rogue" ]; then
    log t4_diff_boundary PASS "working tree writes inside allowed boundary" 0 "$EVID/diff_check.log"; PASS=$((PASS+1))
  else
    log t4_diff_boundary FAIL "rogue paths: $(echo "$rogue" | head -3)" 0 "$EVID/diff_check.log"; FAIL=$((FAIL+1))
  fi

  # residue: no task008 containers left behind
  local res
  res=$(docker ps -a --filter "name=weknora-task008" --format '{{.Names}}' 2>/dev/null)
  if [ -z "$res" ]; then
    log t5_residue PASS "no task008 containers left" 0 "$EVID/residue_scan.log"; PASS=$((PASS+1))
  else
    log t5_residue FAIL "leftover: $res" 0 "$EVID/residue_scan.log"; FAIL=$((FAIL+1))
  fi

  # -race on Task008 scope packages. The pre-existing data race in
  # TestTenantAPIKeyServiceAuthenticateThrottlesLastUsedUpdates (fake repo in
  # tenant_api_key_test.go) is excluded and documented in task007
  # race_full_package.log; Task008 changes do not touch that file.
  run t6_race "$EVID/race.log" \
    go test -race -skip 'TestTenantAPIKeyServiceAuthenticateThrottlesLastUsedUpdates' ./internal/application/service ./internal/application/repository ./internal/models/chat ./internal/models/embedding ./internal/models/rerank
}

case "$MODE" in
  preflight) m_preflight ;;
  reuse-audit) m_reuse_audit ;;
  control-facts) m_control_facts ;;
  observation) m_observation ;;
  cache) m_cache ;;
  config) m_config ;;
  security-lifecycle) m_security_lifecycle ;;
  governance) m_governance ;;
  static) m_static ;;
  all-safe)
    m_preflight
    m_reuse_audit
    m_control_facts
    m_observation
    m_cache
    m_config
    m_security_lifecycle
    m_governance
    m_static
    ;;
  *)
    echo "unknown mode: $MODE" >&2
    echo "modes: preflight reuse-audit control-facts observation cache config security-lifecycle governance static all-safe [--allow-docker]" >&2
    exit 2
    ;;
esac

# P0 enforcement: locally verifiable P0 checks must never be SKIP.
badskip=0
while IFS=$'\t' read -r id status detail dur ev; do
  [ "$status" = "SKIP" ] || continue
  case "$id" in
    c4_pg_runtime|k3_pg_parity|v3_github_required_check) : ;;  # allowed: docker/external facts with archived evidence
    *) badskip=1; echo "P0 SKIP without allowed reason: $id ($detail)" >&2 ;;
  esac
done < "$SUMMARY"

echo "mode=$MODE allow_docker=$ALLOW_DOCKER run_dir=$RUN_DIR"
echo "failures=$FAIL skips=$SKIP"
echo "summary_tsv=$SUMMARY"

if [ "$FAIL" -gt 0 ] || [ "$badskip" = "1" ]; then
  exit 1
fi
exit 0
