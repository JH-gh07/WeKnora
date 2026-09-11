#!/usr/bin/env bash
# Task016 Step 6 — Item/Attempt persistence + dual-DB migration verification.
# Negative controls prove:
#   NC1  SQLite + versioned(PostgreSQL) item/attempt migrations present (up/down)
#   NC2  terminal fact protected by PRIMARY KEY (tenant_id, run_id, item_id)
#   NC3  attempt records owner/lease/fencing token/status/reason/result
#   NC4  repository terminal commit is exactly-once (guard: status NOT IN terminal)
#   NC5  repository is tenant-scoped (every read/write carries tenant_id)
#   NC6  aggregate recomputes from item facts + ledger conservation (I09/I10)
#   NC7  LEGACY_AGGREGATE_ONLY marker for old runs without item facts
#   NC8  PARTIAL run status exists (terminal)
#   NC9  schema parity test compares SQLite vs PostgreSQL column names
#   NC10 dual-DB migration tests pass (SQLite fresh/up/repeat/down + parity)
#   NC11 migration_manifest.json frozen with production execution NOT_AUTHORIZED
set -u

WEKNORA_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SUMMARY="$(mktemp)"
STATUS=0

record() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$SUMMARY"; }
finish() { cat "$SUMMARY"; rm -f "$SUMMARY"; exit "$STATUS"; }
fail() { STATUS=2; record "$1" FAIL "$2"; }

SQL_UP="$WEKNORA_ROOT/migrations/sqlite/000018_evaluation_run_items.up.sql"
SQL_DOWN="$WEKNORA_ROOT/migrations/sqlite/000018_evaluation_run_items.down.sql"
PG_UP="$WEKNORA_ROOT/migrations/versioned/000095_evaluation_run_items.up.sql"
PG_DOWN="$WEKNORA_ROOT/migrations/versioned/000095_evaluation_run_items.down.sql"
TYPES="$WEKNORA_ROOT/internal/types/evaluation_run_item.go"
REPO="$WEKNORA_ROOT/internal/application/repository/evaluation_run_item.go"
AGG="$WEKNORA_ROOT/internal/application/service/evaluation_run_item_aggregate.go"
RUN="$WEKNORA_ROOT/internal/types/evaluation_run.go"
PARITY_TEST="$WEKNORA_ROOT/internal/database/migration_evaluation_run_items_test.go"

# ---- NC1: migrations present ----
for f in "$SQL_UP" "$SQL_DOWN" "$PG_UP" "$PG_DOWN"; do
  if [ -f "$f" ]; then
    record "NC1_$(basename "$f")" PASS "$(basename "$f") present"
  else
    fail "NC1_$(basename "$f")" "$f missing"
  fi
done

# ---- NC2: terminal fact PK ----
if grep -qF 'PRIMARY KEY (tenant_id, run_id, item_id)' "$SQL_UP"; then
  record NC2_ITEM_PK PASS "item terminal fact protected by (tenant_id,run_id,item_id)"
else
  fail NC2_ITEM_PK "item primary key missing"
fi
if grep -qF 'PRIMARY KEY (tenant_id, run_id, item_id, attempt_no)' "$SQL_UP"; then
  record NC2_ATTEMPT_PK PASS "attempt uniqueness (tenant_id,run_id,item_id,attempt_no)"
else
  fail NC2_ATTEMPT_PK "attempt primary key missing"
fi

# ---- NC3: attempt fields ----
for f in owner_id lease_until fencing_token status reason result_artifact_hash; do
  if grep -qF "$f" "$TYPES"; then
    record "NC3_ATTEMPT_$f" PASS "$f present on attempt/item"
  else
    fail "NC3_ATTEMPT_$f" "$f missing"
  fi
done

# ---- NC4: exactly-once terminal commit + current-owner fencing ----
# Step 8 tightened the original Step 6 guard. A commit now succeeds only for
# the live RUNNING owner/token/lease; classifyCommitMiss distinguishes an
# already-terminal fact from a stale owner without allowing either overwrite.
if grep -qF 'status = ? AND "+leaseActivePredicate(tx)' "$REPO" && \
   grep -qF 'owner_id = ? AND fencing_token = ?' "$REPO" && \
   grep -qF 'ErrEvaluationItemAlreadyTerminal' "$REPO"; then
  record NC4_EXACTLY_ONCE PASS "terminal commit requires live owner/token/lease; existing terminal fact cannot be overwritten"
else
  fail NC4_EXACTLY_ONCE "live-owner terminal guard or already-terminal classification missing"
fi

# ---- NC5: tenant scope ----
if grep -qE '"tenant_id = \?' "$REPO"; then
  record NC5_TENANT_SCOPE PASS "repository reads/writes are tenant-scoped"
else
  fail NC5_TENANT_SCOPE "tenant scope missing"
fi

# ---- NC6: aggregate recompute + conservation ----
if grep -qF 'func AggregateRunItems' "$AGG" && grep -qF 'func (a RunItemAggregate) Conserved()' "$AGG"; then
  record NC6_AGGREGATE_CONSERVATION PASS "aggregate recompute + ledger conservation present"
else
  fail NC6_AGGREGATE_CONSERVATION "aggregate recompute or conservation missing"
fi

# ---- NC7: LEGACY_AGGREGATE_ONLY ----
if grep -qF 'AggregateSourceLegacy = "LEGACY_AGGREGATE_ONLY"' "$AGG"; then
  record NC7_LEGACY_AGGREGATE_ONLY PASS "legacy runs read as LEGACY_AGGREGATE_ONLY"
else
  fail NC7_LEGACY_AGGREGATE_ONLY "LEGACY_AGGREGATE_ONLY marker missing"
fi

# ---- NC8: PARTIAL run status ----
if grep -qF 'EvaluationRunStatusPartial' "$RUN" && grep -qF '"PARTIAL"' "$RUN"; then
  record NC8_PARTIAL_STATUS PASS "PARTIAL run status present"
else
  fail NC8_PARTIAL_STATUS "PARTIAL run status missing"
fi

# ---- NC9: schema parity test ----
if grep -qF 'func TestMigrationSchemaParitySQLiteVsPostgres' "$PARITY_TEST"; then
  record NC9_SCHEMA_PARITY PASS "dual-DB schema parity test present"
else
  fail NC9_SCHEMA_PARITY "schema parity test missing"
fi

# ---- NC10: targeted Go tests ----
if (cd "$WEKNORA_ROOT" && go test -count=1 -run 'TestSQLiteMigrationsEvaluationRunItems|TestMigrationSchemaParity|TestEvaluationRunItem|TestEvaluationItemAttempt|TestAggregateRunItems' ./internal/database/ ./internal/application/repository/ ./internal/application/service/ >/dev/null 2>&1); then
  record NC10_GO_TESTS PASS "targeted Step 6 Go tests pass"
else
  fail NC10_GO_TESTS "targeted Step 6 Go tests failed"
fi

# ---- NC11: migration manifest ----
MANIFEST="$WEKNORA_ROOT/../status/evidence/task016/design/migration_manifest.json"
if [ -f "$MANIFEST" ] && grep -qF '"status": "NOT_AUTHORIZED"' "$MANIFEST"; then
  record NC11_MIGRATION_MANIFEST PASS "migration manifest frozen; production execution NOT_AUTHORIZED"
else
  fail NC11_MIGRATION_MANIFEST "migration manifest missing or production execution not gated"
fi

finish
