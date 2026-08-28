#!/usr/bin/env bash
# Task008 Step 4 — Cache failure matrix on isolated PostgreSQL, reusing the
# Task005 repo_parity harness (frozen upstream asset; runs the REAL cache
# repository against real PG with a deterministic in-process provider).
# Ephemeral paradedb container + versioned migrations. No credential echoed.
set -euo pipefail

IMAGE="${IMAGE:-paradedb/paradedb:v0.22.2-pg17}"
OUT_DIR="${1:-/tmp/task008-cache-pg}"
PG_PORT="${PG_PORT:-$((18432 + RANDOM % 1000))}"
CONTAINER="weknora-task008-cachepg-${RANDOM}"
DB_USER="task008c"
DB_PASS="$(openssl rand -hex 12)"
DB_NAME="task008c_test"

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

mkdir -p "$OUT_DIR"

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
    bootstrap=1
    break
  fi
  sleep 1
done
if [ "$bootstrap" != "1" ]; then
  echo "[task008-cachepg] ParadeDB bootstrap never completed" >&2
  exit 1
fi

ready=0
for i in $(seq 1 90); do
  if docker exec "$CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT 1" 2>/dev/null | grep -q 1; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" != "1" ]; then
  echo "[task008-cachepg] database never became queryable" >&2
  exit 1
fi

for f in migrations/versioned/*.up.sql; do
  if ! docker exec -i "$CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 -q < "$f" >/dev/null 2>&1; then
    echo "FAIL $f" >&2
    exit 1
  fi
  echo "OK  $f"
done

DSN="postgres://${DB_USER}:${DB_PASS}@127.0.0.1:${PG_PORT}/${DB_NAME}?sslmode=disable"
go run ./scripts/tmpCheck/task005/repo_parity/ -dsn "$DSN" > "$OUT_DIR/cache_failure_postgres.log" 2>&1
echo "[task008-cachepg] done: $OUT_DIR/cache_failure_postgres.log"
