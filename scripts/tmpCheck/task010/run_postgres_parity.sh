#!/usr/bin/env bash
# Task010 Step 7 — PostgreSQL parity: ephemeral ParadeDB container, real
# versioned migrations, then the report read model runs the SAME canonical
# fixture on PostgreSQL and SQLite and the normalized report JSON is diffed.
# Prints no credential, prompt, document or model-response body.
set -euo pipefail

IMAGE="${IMAGE:-paradedb/paradedb:v0.22.2-pg17}"
OUT_DIR="${1:-/tmp/task010-postgres}"
PG_PORT="${PG_PORT:-$((16432 + RANDOM % 500))}"
CONTAINER="weknora-task010-pg-${RANDOM}"
DB_USER="task010"
DB_PASS="task010_test_only_password"
DB_NAME="task010_test"

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

mkdir -p "$OUT_DIR"

echo "[task010-pg] starting ephemeral postgres container $CONTAINER on 127.0.0.1:$PG_PORT"
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
    bootstrap=1; break
  fi
  sleep 1
done
if [ "$bootstrap" != "1" ]; then
  echo "[task010-pg] ParadeDB bootstrap never completed" >&2
  docker logs "$CONTAINER" 2>&1 | tail -30 >&2
  exit 1
fi

ready=0
for i in $(seq 1 90); do
  if docker exec "$CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT 1" 2>/dev/null | grep -q 1; then
    ready=1; break
  fi
  sleep 1
done
if [ "$ready" != "1" ]; then
  echo "[task010-pg] database never became queryable" >&2
  exit 1
fi

MIG_LOG="$OUT_DIR/migration_postgres.log"
: > "$MIG_LOG"
for f in migrations/versioned/*.up.sql; do
  echo "=== $f ===" >> "$MIG_LOG"
  docker exec -i "$CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 -q < "$f" >> "$MIG_LOG" 2>&1 \
    || { echo "FAIL $f" >> "$MIG_LOG"; docker logs "$CONTAINER" 2>&1 | tail -40 >> "$MIG_LOG"; echo "migration FAILED: $f" >&2; exit 1; }
  echo "OK  $f" >> "$MIG_LOG"
done
echo "[task010-pg] migrations applied"

DSN="postgres://${DB_USER}:${DB_PASS}@127.0.0.1:${PG_PORT}/${DB_NAME}?sslmode=disable"

go build -o "$OUT_DIR/pg_parity" ./scripts/tmpCheck/task010/pg_parity/

# SQLite reference (same binary, sqlite driver).
"$OUT_DIR/pg_parity" -driver sqlite -dsn "$OUT_DIR/parity.sqlite.db" > "$OUT_DIR/report_sqlite.json" 2> "$OUT_DIR/sqlite.log"

# PostgreSQL parity.
"$OUT_DIR/pg_parity" -driver postgres -dsn "$DSN" > "$OUT_DIR/report_postgres.json" 2> "$OUT_DIR/postgres.log"

if diff -u "$OUT_DIR/report_sqlite.json" "$OUT_DIR/report_postgres.json" > "$OUT_DIR/parity.diff"; then
  echo "[task010-pg] PARITY PASS: normalized report JSON identical between SQLite and PostgreSQL"
else
  echo "[task010-pg] PARITY FAIL: diff below" >&2
  cat "$OUT_DIR/parity.diff" >&2
  exit 1
fi

echo "[task010-pg] complete: $OUT_DIR"
