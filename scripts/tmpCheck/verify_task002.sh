#!/usr/bin/env bash

# Task002 (Gate G1) one-command verifier.
#
# Exit codes:
#   0: every requested gate passed
#   1: at least one requested gate failed
#   2: no failure, but at least one requested gate was blocked by prerequisites

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
STATUS_ROOT="$(cd "$REPO_ROOT/.." && pwd)/status"
MODE="all"
ALLOW_DOCKER=0
ALLOW_RESTART=0
START_STACK=0
KEEP_TEMP=0
FAILURES=0
BLOCKED=0
PASSES=0
REPORT_DIR="${TASK002_REPORT_DIR:-/tmp/task002-check-$(date +%Y%m%d-%H%M%S)}"
SUMMARY="$REPORT_DIR/summary.tsv"
TMP_DIR=""
PG_CONTAINER=""

usage() {
  cat <<'EOF'
Usage:
  ./scripts/tmpCheck/verify_task002.sh [all|static|postgres|live] [options]

Options:
  --allow-docker    Allow creation of an isolated temporary PostgreSQL container.
  --allow-restart   Allow live checks to restart/kill the Compose app service.
  --start-stack     Build/start the Compose app before live checks.
  --keep-temp       Keep temporary HTTP responses for diagnosis.
  -h, --help        Show this help.

Live authentication (choose one):
  TASK002_API_KEY                         sent as X-API-Key
  TASK002_JWT                             sent as Authorization: Bearer ...
  TASK002_EMAIL + TASK002_PASSWORD        login without printing credentials/token

Optional live settings:
  TASK002_BASE_URL=http://127.0.0.1:8080
  TASK002_DATASET_ID=default
  TASK002_TIMEOUT_SECONDS=900

Recommended full command:
  TASK002_API_KEY='...' ./scripts/tmpCheck/verify_task002.sh all \
    --allow-docker --allow-restart --start-stack
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    all|static|postgres|live) MODE="$1" ;;
    --allow-docker) ALLOW_DOCKER=1 ;;
    --allow-restart) ALLOW_RESTART=1 ;;
    --start-stack) START_STACK=1 ;;
    --keep-temp) KEEP_TEMP=1 ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; usage >&2; exit 1 ;;
  esac
  shift
done

mkdir -p "$REPORT_DIR"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/task002-check.XXXXXX")"
printf 'gate\tstatus\tdetail\n' > "$SUMMARY"

cleanup() {
  if [ -n "$PG_CONTAINER" ]; then
    docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
  fi
  if [ "$KEEP_TEMP" -eq 0 ] && [ -n "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  else
    printf 'Temporary files retained at %s\n' "$TMP_DIR"
  fi
}
trap cleanup EXIT INT TERM

record() {
  local gate="$1" status="$2" detail="$3"
  printf '%s\t%s\t%s\n' "$gate" "$status" "$detail" >> "$SUMMARY"
  case "$status" in
    PASS) PASSES=$((PASSES + 1)); printf '[PASS] %s - %s\n' "$gate" "$detail" ;;
    FAIL) FAILURES=$((FAILURES + 1)); printf '[FAIL] %s - %s\n' "$gate" "$detail" >&2 ;;
    BLOCKED) BLOCKED=$((BLOCKED + 1)); printf '[BLOCKED] %s - %s\n' "$gate" "$detail" ;;
  esac
}

run_logged() {
  local gate="$1" logfile="$2"
  shift 2
  if "$@" >"$REPORT_DIR/$logfile" 2>&1; then
    record "$gate" PASS "see $logfile"
    return 0
  fi
  record "$gate" FAIL "see $logfile"
  return 1
}

require_tools() {
  local missing=""
  local tool
  for tool in "$@"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      missing="$missing $tool"
    fi
  done
  if [ -n "$missing" ]; then
    record "tools" FAIL "missing:$missing"
    return 1
  fi
  record "tools" PASS "required commands available"
}

# Docker Desktop can leave its CLI socket in a half-open state where
# `docker info` never returns. Bound the probe so a one-command verifier never
# hangs forever merely because the daemon is stopped or unhealthy.
docker_ready() {
  docker info >/dev/null 2>&1 &
  local pid=$! i
  for i in $(seq 1 15); do
    if ! kill -0 "$pid" >/dev/null 2>&1; then
      wait "$pid"
      return $?
    fi
    sleep 1
  done
  kill "$pid" >/dev/null 2>&1 || true
  wait "$pid" >/dev/null 2>&1 || true
  return 1
}

check_evidence() {
  local evidence="$STATUS_ROOT/evidence/task002"
  local required="README.md baseline_identity.md schema_feasibility.md resource_locator_decision.md protocol_provenance_spec.yaml protocol_hash_tests.txt migration_postgres.log migration_sqlite.log repository_tests.log t1_normal_completion.md t2_interrupted_run.md t3_same_protocol.md t4_protocol_change.md t5_commit_change.md restart_reconciliation.md tenant_isolation.md concurrent_runs.md api_contract_decision.md concurrency_fix.md task002_review.md"
  local missing="" file
  for file in $required; do
    [ -f "$evidence/$file" ] || missing="$missing $file"
  done
  [ -f "$evidence/sanitized_runs/completed_run.json" ] || missing="$missing sanitized_runs/completed_run.json"
  [ -f "$evidence/sanitized_runs/interrupted_run.json" ] || missing="$missing sanitized_runs/interrupted_run.json"
  if [ -n "$missing" ]; then
    record "evidence-manifest" FAIL "missing:$missing"
  else
    record "evidence-manifest" PASS "required evidence files present"
  fi

  if jq empty "$evidence/sanitized_runs/completed_run.json" \
      "$evidence/sanitized_runs/interrupted_run.json" >"$REPORT_DIR/evidence-json.log" 2>&1; then
    record "evidence-json" PASS "sanitized run JSON is valid"
  else
    record "evidence-json" FAIL "invalid JSON; see evidence-json.log"
  fi

  if rg -n -i '(Bearer[[:space:]]+[A-Za-z0-9._-]{16,}|sk-[A-Za-z0-9_-]{16,}|(password|passwd|secret|api[_ -]?key)[[:space:]]*[:=][[:space:]]*[^[:space:]]+)' "$evidence" \
      >"$REPORT_DIR/evidence-secret-scan.log" 2>&1; then
    record "evidence-secret-scan" FAIL "credential-shaped content found; see evidence-secret-scan.log"
  else
    record "evidence-secret-scan" PASS "no credential-shaped content found"
  fi

  if jq -r '.. | strings | select(length > 500)' "$evidence"/sanitized_runs/*.json \
      | sed -n '1,5p' >"$REPORT_DIR/evidence-long-strings.log" && [ ! -s "$REPORT_DIR/evidence-long-strings.log" ]; then
    record "evidence-prompt-scan" PASS "no JSON string longer than 500 characters"
  else
    record "evidence-prompt-scan" FAIL "long JSON strings require manual prompt review; see evidence-long-strings.log"
  fi

  : >"$REPORT_DIR/markdown-fences.log"
  while IFS= read -r -d '' file; do
    local fence_count
    fence_count="$(awk '/^[[:space:]]*(```+|~~~+)/ { count++ } END { print count + 0 }' "$file")"
    if [ $((fence_count % 2)) -ne 0 ]; then
      printf '%s: odd number of Markdown fence lines (%s)\n' "$file" "$fence_count" \
        >>"$REPORT_DIR/markdown-fences.log"
    fi
  done < <(find "$evidence" -type f -name '*.md' -print0)
  if [ ! -s "$REPORT_DIR/markdown-fences.log" ]; then
    record "markdown-fences" PASS "all evidence Markdown fences are balanced"
  else
    record "markdown-fences" FAIL "see markdown-fences.log"
  fi
}

check_contract_tests_exist() {
  local missing="" test
  for test in \
    TestEvaluationRunListReconciliationCandidates \
    TestReconcileInterruptedRunsPreservesTerminalStates \
    TestReconcileTemporaryResourceKeepsKBIDOnDeleteFailure \
    TestMarkRunFailedPersistsSafeGenericMessage; do
    rg -q "func $test" internal/application || missing="$missing $test"
  done
  # These two regression tests close the final recovery/security review. Until
  # they exist, the verifier refuses to call the implementation frozen.
  for test in \
    TestReconcileTemporaryResourceDoesNotMarkDoneOnFinderError \
    TestReconciliationClearsPersistFailedAfterSuccessfulRewrite; do
    rg -q "func $test" internal/application || missing="$missing $test"
  done
  if [ -n "$missing" ]; then
    record "regression-contract" FAIL "missing:$missing"
  else
    record "regression-contract" PASS "required crash/recovery regression tests exist"
  fi
}

run_static() {
  printf '\n== Static / deterministic gates ==\n'
  require_tools go git rg jq awk find || return
  cd "$REPO_ROOT" || exit 1

  run_logged "protocol-t3-t5" "protocol-tests.log" \
    go test ./internal/application/service -run 'TestEvaluationProtocol' -count=1
  run_logged "repository-tenant-restart" "repository-tests.log" \
    go test ./internal/application/repository -run 'TestEvaluationRun|TestTemporaryKnowledgeBaseFinder' -count=1
  run_logged "sqlite-migration" "sqlite-migration.log" \
    go test ./internal/database -run TestSQLiteMigrationsCreateEvaluationRuns -count=1
  run_logged "task002-race" "task002-race.log" \
    go test -race ./internal/application/service -run 'TestEvalDatasetConcurrentProgressHasNoDataRace|TestReconcileInterruptedRuns|TestReconcileTemporaryResource|TestMarkRunFailed|TestEvalDatasetCleansTemporaryKBOnIngestFailure' -count=1
  run_logged "go-vet" "go-vet.log" \
    go vet ./internal/application/service ./internal/application/repository ./internal/types ./internal/container ./cmd/server
  run_logged "server-link" "server-build.log" \
    go build -ldflags='-s -w' -o "$REPORT_DIR/weknora-server-check" ./cmd/server

  check_contract_tests_exist
  check_evidence

  if rg -n 'list RUNNING for single-worker reconciliation|sanitizeErrorMessage' \
      "$STATUS_ROOT/todo/task002_persistent_evaluation.md" "$STATUS_ROOT/evidence/task002" \
      >"$REPORT_DIR/stale-terms.log" 2>&1; then
    record "documentation-consistency" FAIL "stale Task002 terminology found; see stale-terms.log"
  else
    record "documentation-consistency" PASS "no known stale Task002 terminology"
  fi
}

run_postgres() {
  printf '\n== Isolated PostgreSQL migration gate ==\n'
  if [ "$ALLOW_DOCKER" -ne 1 ]; then
    record "postgres-live" BLOCKED "rerun with --allow-docker"
    return
  fi
  if ! command -v docker >/dev/null 2>&1 || ! docker_ready; then
    record "postgres-live" BLOCKED "Docker daemon unavailable"
    return
  fi

  local image="${TASK002_POSTGRES_IMAGE:-paradedb/paradedb:v0.22.2-pg17}"
  PG_CONTAINER="task002-pg-$PPID-$$"
  if ! docker run -d --rm --name "$PG_CONTAINER" \
      -e POSTGRES_USER=task002 -e POSTGRES_PASSWORD=task002-local-only \
      -e POSTGRES_DB=task002 "$image" >"$REPORT_DIR/postgres-container.log" 2>&1; then
    record "postgres-container" FAIL "could not start $image; see postgres-container.log"
    return
  fi

  local ready=0 i
  for i in $(seq 1 60); do
    if docker exec "$PG_CONTAINER" pg_isready -U task002 -d task002 >/dev/null 2>&1; then ready=1; break; fi
    sleep 1
  done
  if [ "$ready" -ne 1 ]; then
    record "postgres-container" FAIL "temporary PostgreSQL did not become ready"
    return
  fi
  record "postgres-container" PASS "isolated PostgreSQL ready"

  if docker exec -i "$PG_CONTAINER" psql -v ON_ERROR_STOP=1 -U task002 -d task002 \
      < "$REPO_ROOT/migrations/versioned/000090_evaluation_runs.up.sql" \
      >"$REPORT_DIR/postgres-migration.log" 2>&1 && \
     docker exec -i "$PG_CONTAINER" psql -v ON_ERROR_STOP=1 -U task002 -d task002 \
      < "$REPO_ROOT/migrations/versioned/000090_evaluation_runs.up.sql" \
      >>"$REPORT_DIR/postgres-migration.log" 2>&1; then
    record "postgres-up-idempotent" PASS "fresh apply and repeat apply succeeded"
  else
    record "postgres-up-idempotent" FAIL "see postgres-migration.log"
    return
  fi

  local count
  count="$(docker exec "$PG_CONTAINER" psql -At -U task002 -d task002 -c \
    "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='evaluation_runs';" 2>/dev/null || true)"
  if [ "$count" = "27" ]; then
    record "postgres-schema" PASS "evaluation_runs has 27 columns"
  else
    record "postgres-schema" FAIL "expected 27 columns, got ${count:-unknown}"
  fi

  if docker exec -i "$PG_CONTAINER" psql -v ON_ERROR_STOP=1 -U task002 -d task002 \
      < "$REPO_ROOT/migrations/versioned/000090_evaluation_runs.down.sql" \
      >"$REPORT_DIR/postgres-down.log" 2>&1; then
    record "postgres-down" PASS "down migration succeeded"
  else
    record "postgres-down" FAIL "see postgres-down.log"
  fi
}

compose() {
  docker compose -f "$REPO_ROOT/docker-compose.yml" "$@"
}

wait_health() {
  local base="$1" timeout="$2" start now
  start="$(date +%s)"
  while :; do
    if curl -fsS "$base/health" >/dev/null 2>&1; then return 0; fi
    now="$(date +%s)"
    [ $((now - start)) -ge "$timeout" ] && return 1
    sleep 2
  done
}

prepare_auth() {
  AUTH_ARGS=()
  if [ -n "${TASK002_API_KEY:-}" ]; then
    AUTH_ARGS=(-H "X-API-Key: $TASK002_API_KEY")
    return 0
  fi
  if [ -n "${TASK002_JWT:-}" ]; then
    AUTH_ARGS=(-H "Authorization: Bearer $TASK002_JWT")
    return 0
  fi
  if [ -n "${TASK002_EMAIL:-}" ] && [ -n "${TASK002_PASSWORD:-}" ]; then
    local login="$TMP_DIR/login.json" token
    jq -n --arg email "$TASK002_EMAIL" --arg password "$TASK002_PASSWORD" \
      '{email:$email,password:$password}' \
      | curl -fsS -X POST -H 'Content-Type: application/json' --data-binary @- \
          "$BASE_URL/api/v1/auth/login" -o "$login" || return 1
    token="$(jq -r '.token // .data.token // empty' "$login")"
    [ -n "$token" ] || return 1
    AUTH_ARGS=(-H "Authorization: Bearer $token")
    return 0
  fi
  return 1
}

post_evaluation() {
  local output="$1"
  jq -n --arg dataset "$DATASET_ID" '{dataset_id:$dataset}' \
    | curl -fsS -X POST "${AUTH_ARGS[@]}" -H 'Content-Type: application/json' \
        --data-binary @- "$BASE_URL/api/v1/evaluation" -o "$output"
}

get_evaluation() {
  local task_id="$1" output="$2"
  curl -fsS -G "${AUTH_ARGS[@]}" --data-urlencode "task_id=$task_id" \
    "$BASE_URL/api/v1/evaluation" -o "$output"
}

run_live() {
  printf '\n== Live HTTP restart gates ==\n'
  if [ "$ALLOW_RESTART" -ne 1 ]; then
    record "live-http" BLOCKED "rerun with --allow-restart; this gate kills/restarts Compose app"
    return
  fi
  if ! command -v docker >/dev/null 2>&1 || ! docker_ready; then
    record "live-http" BLOCKED "Docker daemon unavailable"
    return
  fi
  if ! docker compose version >/dev/null 2>&1; then
    record "live-http" BLOCKED "Docker Compose v2 unavailable"
    return
  fi

  BASE_URL="${TASK002_BASE_URL:-http://127.0.0.1:${APP_PORT:-8080}}"
  DATASET_ID="${TASK002_DATASET_ID:-default}"
  local timeout="${TASK002_TIMEOUT_SECONDS:-900}"
  AUTH_ARGS=()

  if [ "$START_STACK" -eq 1 ]; then
    if ! compose up -d --build app >"$REPORT_DIR/compose-up.log" 2>&1; then
      record "compose-up" FAIL "see compose-up.log"
      return
    fi
  fi
  if ! wait_health "$BASE_URL" 180; then
    record "app-health" BLOCKED "app is not healthy at $BASE_URL"
    return
  fi
  record "app-health" PASS "$BASE_URL/health"

  if ! prepare_auth; then
    record "live-auth" BLOCKED "set TASK002_API_KEY, TASK002_JWT, or TASK002_EMAIL/PASSWORD"
    return
  fi
  record "live-auth" PASS "credentials accepted without being written to report"

  local post="$TMP_DIR/t1-post.json" get="$TMP_DIR/t1-get.json" task_id status start now
  if ! post_evaluation "$post"; then
    record "T1-create" FAIL "evaluation POST failed"
    return
  fi
  task_id="$(jq -r '.data.task.id // empty' "$post")"
  if [ -z "$task_id" ]; then
    record "T1-create" FAIL "response has no data.task.id"
    return
  fi
  record "T1-create" PASS "task identity allocated"

  start="$(date +%s)"
  while :; do
    if get_evaluation "$task_id" "$get"; then
      status="$(jq -r '.data.task.status // empty' "$get")"
      [ "$status" = "2" ] && break
      if [ "$status" = "3" ]; then record "T1-complete" FAIL "evaluation failed"; return; fi
    fi
    now="$(date +%s)"
    if [ $((now - start)) -ge "$timeout" ]; then record "T1-complete" FAIL "timed out"; return; fi
    sleep 3
  done
  record "T1-complete" PASS "evaluation completed"

  compose restart app >"$REPORT_DIR/t1-restart.log" 2>&1 || { record "T1-restart" FAIL "restart failed"; return; }
  wait_health "$BASE_URL" 180 || { record "T1-restart" FAIL "app unhealthy after restart"; return; }
  if get_evaluation "$task_id" "$get" && [ "$(jq -r '.data.task.status // empty' "$get")" = "2" ]; then
    record "T1-restart-query" PASS "same task remains queryable after restart"
  else
    record "T1-restart-query" FAIL "same task not queryable as COMPLETED"
  fi

  local post2="$TMP_DIR/t2-post.json" get2="$TMP_DIR/t2-get.json" task2
  if ! post_evaluation "$post2"; then record "T2-create" FAIL "evaluation POST failed"; return; fi
  task2="$(jq -r '.data.task.id // empty' "$post2")"
  [ -n "$task2" ] || { record "T2-create" FAIL "response has no data.task.id"; return; }
  record "T2-create" PASS "task identity allocated"

  compose kill app >"$REPORT_DIR/t2-kill.log" 2>&1 || { record "T2-kill" FAIL "SIGKILL failed"; return; }
  compose up -d app >>"$REPORT_DIR/t2-kill.log" 2>&1 || { record "T2-restart" FAIL "app restart failed"; return; }
  wait_health "$BASE_URL" 180 || { record "T2-restart" FAIL "app unhealthy after crash restart"; return; }
  sleep 2
  if get_evaluation "$task2" "$get2" && \
      [ "$(jq -r '.data.task.status // empty' "$get2")" = "3" ] && \
      jq -e '.data.task.err_msg | test("interrupted"; "i")' "$get2" >/dev/null; then
    record "T2-interrupted" PASS "same task reconciled as interrupted after SIGKILL"
  else
    record "T2-interrupted" FAIL "task did not reconcile to interrupted"
  fi

  # Responses stay in TMP_DIR and are deleted by default. Persist only a
  # secret-free structural summary, never raw params/prompts.
  jq -n --arg t1 "$task_id" --arg t2 "$task2" \
    '{t1_task_id:$t1,t2_task_id:$t2,raw_responses_persisted:false}' \
    >"$REPORT_DIR/live-summary.json"
}

case "$MODE" in
  static) run_static ;;
  postgres) run_postgres ;;
  live) run_live ;;
  all) run_static; run_postgres; run_live ;;
esac

rm -f "$REPORT_DIR/weknora-server-check"
printf '\nTask002 verification summary: PASS=%d FAIL=%d BLOCKED=%d\n' "$PASSES" "$FAILURES" "$BLOCKED"
printf 'Report: %s\n' "$REPORT_DIR"

if [ "$FAILURES" -gt 0 ]; then exit 1; fi
if [ "$BLOCKED" -gt 0 ]; then exit 2; fi
exit 0
