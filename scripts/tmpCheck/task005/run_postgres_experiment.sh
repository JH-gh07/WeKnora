#!/usr/bin/env bash
# Task005 Step 8/9 — PostgreSQL parity: ephemeral container, real migrations,
# then the deterministic OFF/COLD/WARM experiment against PostgreSQL.
# Produces migration_postgres.log, repository_tests_postgres.log, and
# raw/ samples under the given output dir. No credential is echoed.
set -euo pipefail

IMAGE="${IMAGE:-paradedb/paradedb:v0.22.2-pg17}"
OUT_DIR="${1:-/tmp/task005-postgres}"
PG_PORT="${PG_PORT:-$((15432 + RANDOM % 1000))}"
CONTAINER="weknora-task005-pg-${RANDOM}"
DB_USER="task005"
DB_PASS="task005_test_only_password"
DB_NAME="task005_test"

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

mkdir -p "$OUT_DIR/raw"

echo "[task005-pg] starting ephemeral postgres container $CONTAINER on 127.0.0.1:$PG_PORT"
docker run -d --name "$CONTAINER" \
  -e POSTGRES_USER="$DB_USER" \
  -e POSTGRES_PASSWORD="$DB_PASS" \
  -e POSTGRES_DB="$DB_NAME" \
  -p "127.0.0.1:${PG_PORT}:5432" \
  --shm-size=256m \
  "$IMAGE" >/dev/null

# ParadeDB runs a two-phase init: bootstrap extensions into POSTGRES_DB, then
# shutdown, then final server startup. pg_isready can succeed during the
# bootstrap phase, so wait for the bootstrap marker first, then for a stable
# queryable connection.
bootstrap=0
for i in $(seq 1 120); do
  if docker logs "$CONTAINER" 2>&1 | grep -q "ParadeDB bootstrap completed"; then
    echo "[task005-pg] ParadeDB bootstrap completed after ${i}s"
    bootstrap=1
    break
  fi
  sleep 1
done
if [ "$bootstrap" != "1" ]; then
  echo "[task005-pg] ParadeDB bootstrap never completed" >&2
  docker logs "$CONTAINER" 2>&1 | tail -30 >&2
  exit 1
fi

ready=0
for i in $(seq 1 90); do
  if docker exec "$CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT 1" 2>/dev/null | grep -q 1; then
    echo "[task005-pg] database $DB_NAME queryable after bootstrap +${i}s"
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" != "1" ]; then
  echo "[task005-pg] database $DB_NAME never became queryable" >&2
  exit 1
fi

# Apply the real versioned migrations in order, capturing DDL output.
MIG_LOG="$OUT_DIR/migration_postgres.log"
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
echo "[task005-pg] migrations applied"

# Verify the two cache tables and the tenant-scoped unique index exist.
docker exec "$CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('embedding_cache_entries','embedding_cache_observations')" \
  >> "$MIG_LOG"

DSN="postgres://${DB_USER}:${DB_PASS}@127.0.0.1:${PG_PORT}/${DB_NAME}?sslmode=disable"

# PostgreSQL repository/failure behaviour parity: restart, corruption,
# lookup/write failure, concurrent unique conflict, tenant/model purge. This
# closes the gap where PostgreSQL previously only ran the main experiment while
# the repository failure matrix was SQLite-only.
REPO_LOG="$OUT_DIR/repository_tests_postgres.log"
: > "$REPO_LOG"
if ! go run ./scripts/tmpCheck/task005/repo_parity/ \
  -dsn "$DSN" >> "$REPO_LOG" 2>&1; then
  echo "[task005-pg] repository/failure parity FAILED" >&2
  tail -40 "$REPO_LOG" >&2
  exit 1
fi
echo "[task005-pg] repository/failure parity passed"

# Run the identical experiment harness against PostgreSQL.
go run ./scripts/tmpCheck/task005/experiment/ \
  -driver postgres \
  -dsn "$DSN" \
  -out "$OUT_DIR" 2>&1 | tee "$OUT_DIR/experiment_postgres.stdout"

echo "[task005-pg] postgres experiment complete: $OUT_DIR"
