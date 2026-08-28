#!/usr/bin/env bash
# Task005 one-click verifier. Modes:
#   static                        vet/build/tests/secret-scan/frontend
#   experiment --database sqlite  deterministic OFF/Cold/Warm on SQLite
#   experiment --database postgres --allow-docker   parity on ephemeral PostgreSQL
#   all [--allow-docker]          everything
# Outputs PASS/FAIL/BLOCKED per check, writes a run summary.tsv, exits non-zero
# on any required FAIL/BLOCKED. Never prints secrets, raw text, or vector bodies.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
EVID="$REPO_ROOT/../status/evidence/task005"
RUN_ID="$(date +%Y%m%d_%H%M%S)"
MODE="${1:-static}"
shift || true
ALLOW_DOCKER=0
DB="sqlite"
prev=""
for a in "$@"; do
  case "$a" in
    --allow-docker) ALLOW_DOCKER=1 ;;
    --database) prev="db" ;;
    *) [ "$prev" = "db" ] && DB="$a"; prev="" ;;
  esac
done

RESULTS=()   # "check<TAB>status<TAB>detail"
note() { RESULTS+=("$1	$2	$3"); }
pass() { note "$1" "PASS" "$2"; }
fail() { note "$1" "FAIL" "$2"; }
blocked() { note "$1" "BLOCKED" "$2"; }

# docker may block indefinitely when the daemon is down/busy, so every docker
# call is bounded by `timeout` to guarantee the verifier can always exit.
DOCKER_PS_Q="timeout 8 docker ps -aq --filter name=weknora-task005-pg-"

cleanup() {
  local ids
  ids="$($DOCKER_PS_Q 2>/dev/null)"
  [ -n "$ids" ] && timeout 8 docker rm -f $ids >/dev/null 2>&1 || true
  rm -f /tmp/task005-experiment.db /tmp/task005-experiment.db-* 2>/dev/null || true
  # Defensive: never leave a stray build artifact in the repo root.
  rm -f "$REPO_ROOT/server" "$REPO_ROOT/repo_parity" 2>/dev/null || true
}
trap cleanup EXIT

cd "$REPO_ROOT" || exit 1

emit_summary() {
  local dir="$EVID/automated_${1}_${RUN_ID}"
  mkdir -p "$dir"
  { echo "check	status	detail"; printf '%s\n' "${RESULTS[@]}"; } > "$dir/summary.tsv"
  {
    echo "mode=$1 run_id=$RUN_ID commit=$(git rev-parse --short HEAD) go=$(go version | awk '{print $3}') allow_docker=$ALLOW_DOCKER"
    echo "cleanup_residue=$($DOCKER_PS_Q 2>/dev/null | wc -l | tr -d ' ')"
  } > "$dir/environment.txt"
  echo "[verify] summary: $dir/summary.tsv"
}

run_static() {
  go vet ./internal/models/embedding ./internal/application/repository ./internal/application/service ./internal/handler ./internal/types ./cmd/server >/tmp/task005-vet.log 2>&1 \
    && pass "static:go-vet" "clean" || fail "static:go-vet" "see /tmp/task005-vet.log"

  # Build to /dev/null so the verifier never leaves a ~250MB `server` binary in
  # the repo root. The linker still runs end-to-end, so a broken build fails.
  go build -ldflags='-s -w' -o /dev/null ./cmd/server >/tmp/task005-build.log 2>&1
  if [ $? -eq 0 ]; then
    pass "static:go-build-server" "clean"
  elif grep -qi "no space left on device" /tmp/task005-build.log; then
    blocked "static:go-build-server" "environmental: disk full"
  else
    fail "static:go-build-server" "see /tmp/task005-build.log"
  fi

  go test ./internal/models/embedding ./internal/application/repository ./internal/application/service ./internal/database -run 'EmbeddingCache|Restart|Tenant|Failure|Cache|ModelUsage' -count=1 >/tmp/task005-test.log 2>&1 \
    && pass "static:go-test-unit" "clean" || fail "static:go-test-unit" "see /tmp/task005-test.log"

  go test -race ./internal/application/repository ./internal/models/embedding -run 'EmbeddingCache|Concurrent' -count=1 >/tmp/task005-race.log 2>&1 \
    && pass "static:go-test-race" "clean" || fail "static:go-test-race" "see /tmp/task005-race.log"

  if [ -f "$REPO_ROOT/scripts/tmpCheck/task005/secret_scan.sh" ]; then
    "$REPO_ROOT/scripts/tmpCheck/task005/secret_scan.sh" >/tmp/task005-secret.log 2>&1 \
      && pass "static:secret-scan" "clean" || fail "static:secret-scan" "see /tmp/task005-secret.log"
  else
    blocked "static:secret-scan" "script missing"
  fi

  if [ -d "$REPO_ROOT/frontend" ]; then
    ( cd "$REPO_ROOT/frontend" && npx tsx --test src/views/settings/modelUsageState.test.ts ) >/tmp/task005-fe-test.log 2>&1 \
      && pass "static:frontend-unit" "clean" || fail "static:frontend-unit" "see /tmp/task005-fe-test.log"
    ( cd "$REPO_ROOT/frontend" && npm run type-check ) >/tmp/task005-fe-typecheck.log 2>&1 \
      && pass "static:frontend-typecheck" "clean" || fail "static:frontend-typecheck" "see /tmp/task005-fe-typecheck.log"
  else
    blocked "static:frontend" "frontend dir missing"
  fi

  "$REPO_ROOT/scripts/tmpCheck/task005/generate_implementation_manifest.sh" >/tmp/task005-manifest.log 2>&1 \
    && pass "static:implementation-manifest" "tracked/untracked Task005 sources frozen" \
    || fail "static:implementation-manifest" "see /tmp/task005-manifest.log"
}

run_experiment() {
  local out="/tmp/task005-auto-$RUN_ID"
  local persisted="$EVID/automated_experiment_${DB}_${RUN_ID}/artifacts"
  mkdir -p "$out"
  if [ "$DB" = "postgres" ]; then
    if [ "$ALLOW_DOCKER" != 1 ]; then
      blocked "experiment:postgres" "requires --allow-docker"
      return
    fi
    if ! command -v docker >/dev/null 2>&1; then blocked "experiment:postgres" "docker missing"; return; fi
    "$REPO_ROOT/scripts/tmpCheck/task005/run_postgres_experiment.sh" "$out" >/tmp/task005-pg.log 2>&1 \
      && pass "experiment:postgres" "see $persisted/summary.tsv" || fail "experiment:postgres" "see /tmp/task005-pg.log"
  else
    go run ./scripts/tmpCheck/task005/experiment -driver sqlite -db "$out/exp.db" -migrations migrations/sqlite -out "$out" >/tmp/task005-exp.log 2>&1 \
      && pass "experiment:sqlite" "see $persisted/summary.tsv" || fail "experiment:sqlite" "see /tmp/task005-exp.log"
  fi

  local corr="$out/correctness.tsv"
  if [ -f "$corr" ] && [ "$(tail -n +2 "$corr" | awk -F'\t' '$2=="true" && $3=="true" && $4=="true" && $5=="true"' | wc -l | tr -d ' ')" = "3" ]; then
    pass "experiment:correctness" "vector+retrieval non-regression true x3 rounds"
  else
    fail "experiment:correctness" "not all rounds vector/retrieval equal"
  fi
  if [ -f "$out/summary.tsv" ] && [ "$(grep -c $'\tON_WARM\t' "$out/summary.tsv")" = "3" ] && \
     [ "$(awk -F'\t' '$2=="ON_WARM"{s+=$6} END{print s+0}' "$out/summary.tsv")" = "0" ]; then
    pass "experiment:warm-zero-provider" "warm provider_bound_model_call total = 0"
  else
    fail "experiment:warm-zero-provider" "warm provider calls not zero"
  fi

  # Keep the accepted run self-contained under Evidence instead of pointing at
  # ephemeral /tmp files. exp.db is deliberately excluded; raw JSON contains
  # only pre-registered digests/counts and is checked again by secret_scan.
  mkdir -p "$persisted"
  for file in correctness.tsv summary.tsv migration_postgres.log repository_tests_postgres.log experiment_postgres.stdout; do
    [ -f "$out/$file" ] && cp "$out/$file" "$persisted/$file"
  done
  [ -d "$out/raw" ] && cp -R "$out/raw" "$persisted/raw"
  if "$REPO_ROOT/scripts/tmpCheck/task005/secret_scan.sh" >/tmp/task005-experiment-secret.log 2>&1; then
    pass "experiment:evidence-secret-scan" "persisted artifacts clean"
  else
    fail "experiment:evidence-secret-scan" "see /tmp/task005-experiment-secret.log"
  fi
}

case "$MODE" in
  static) run_static; emit_summary "static" ;;
  experiment) run_experiment; emit_summary "experiment_${DB}" ;;
  all)
    run_static
    DB=sqlite run_experiment
    if [ "$ALLOW_DOCKER" = 1 ]; then DB=postgres run_experiment; else blocked "experiment:postgres" "skipped (no --allow-docker)"; fi
    emit_summary "all"
    ;;
  *) echo "usage: $0 {static|experiment --database sqlite|experiment --database postgres --allow-docker|all [--allow-docker]}" >&2; exit 2 ;;
esac

if printf '%s\n' "${RESULTS[@]}" | awk -F'\t' '$2=="FAIL" || $2=="BLOCKED"' | grep -q .; then
  printf '%s\n' "${RESULTS[@]}"
  echo "[verify] RESULT: FAIL/BLOCKED (exit 1)"
  exit 1
else
  printf '%s\n' "${RESULTS[@]}"
  echo "[verify] RESULT: PASS"
  exit 0
fi
