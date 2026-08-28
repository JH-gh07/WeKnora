#!/usr/bin/env bash
# Task008 Step 2 — Control Fact FACT-RUNTIME on isolated PostgreSQL.
# Ephemeral paradedb container + real versioned migrations, then the
# two-phase control_fact_pg program: seed -> (crash) -> reconcile x2.
# Produces control_fact_postgres.log. No credential is echoed; the DSN is
# passed to the Go program via TASK008_PG_DSN env only.
set -euo pipefail

IMAGE="${IMAGE:-paradedb/paradedb:v0.22.2-pg17}"
OUT_DIR="${1:-/tmp/task008-control-fact-pg}"
PG_PORT="${PG_PORT:-$((17432 + RANDOM % 1000))}"
CONTAINER="weknora-task008-cfpg-${RANDOM}"
DB_USER="task008"
DB_PASS="$(openssl rand -hex 12)"
DB_NAME="task008_test"

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

mkdir -p "$OUT_DIR"

echo "[task008-cfpg] starting ephemeral postgres container $CONTAINER on 127.0.0.1:$PG_PORT"
docker run -d --name "$CONTAINER" \
  -e POSTGRES_USER="$DB_USER" \
  -e POSTGRES_PASSWORD="$DB_PASS" \
  -e POSTGRES_DB="$DB_NAME" \
  -p "127.0.0.1:${PG_PORT}:5432" \
  --shm-size=256m \
  "$IMAGE" >/dev/null

bootstrap=0
for i in $(seq 1 120); do
  if docker logs "$CONTAINER" 2>&1 | grep -q "ParadeDB bootstrap completed"; then
    echo "[task008-cfpg] ParadeDB bootstrap completed after ${i}s"
    bootstrap=1
    break
  fi
  sleep 1
done
if [ "$bootstrap" != "1" ]; then
  echo "[task008-cfpg] ParadeDB bootstrap never completed" >&2
  docker logs "$CONTAINER" 2>&1 | tail -30 >&2
  exit 1
fi

ready=0
for i in $(seq 1 90); do
  if docker exec "$CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT 1" 2>/dev/null | grep -q 1; then
    echo "[task008-cfpg] database queryable after bootstrap +${i}s"
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" != "1" ]; then
  echo "[task008-cfpg] database never became queryable" >&2
  exit 1
fi

MIG_LOG="$OUT_DIR/migration_control_fact_postgres.log"
: > "$MIG_LOG"
for f in migrations/versioned/*.up.sql; do
  echo "=== $f ===" >> "$MIG_LOG"
  if ! docker exec -i "$CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 -q < "$f" >> "$MIG_LOG" 2>&1; then
    echo "FAIL $f"
    echo "=== container logs tail ===" >> "$MIG_LOG"
    docker logs "$CONTAINER" 2>&1 | tail -40 >> "$MIG_LOG"
    exit 1
  fi
  echo "OK  $f"
done
echo "[task008-cfpg] migrations applied"

export TASK008_PG_DSN="postgres://${DB_USER}:${DB_PASS}@127.0.0.1:${PG_PORT}/${DB_NAME}?sslmode=disable"

RUN_LOG="$OUT_DIR/control_fact_postgres.log"
: > "$RUN_LOG"
echo "== phase seed (process 1; exits to simulate crash) ==" >> "$RUN_LOG"
go run ./scripts/tmpCheck/task008/control_fact_pg/ seed >> "$RUN_LOG" 2>&1
echo "== phase reconcile pass 1 (process 2, fresh) ==" >> "$RUN_LOG"
go run ./scripts/tmpCheck/task008/control_fact_pg/ reconcile1 >> "$RUN_LOG" 2>&1
echo "== phase reconcile pass 2 (process 3, fresh; idempotence) ==" >> "$RUN_LOG"
go run ./scripts/tmpCheck/task008/control_fact_pg/ reconcile2 >> "$RUN_LOG" 2>&1
echo "[task008-cfpg] done: $RUN_LOG"
