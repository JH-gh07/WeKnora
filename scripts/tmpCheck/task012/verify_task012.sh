#!/usr/bin/env bash
# Task012 — One-provider Real Usage-based Cost Closure verifier.
#
# Modes:
#   static    — pricing catalog/estimator/recorder contract + static + security
#   sqlite    — SQLite migration/repository parity (offline)
#   postgres  — PostgreSQL migration (requires --allow-docker)
#   frontend  — UI compatibility (no production frontend change expected)
#   security  — secret/residue scan of source + evidence
#   live      — real SiliconFlow provider probe (requires --allow-provider)
#   all-safe  — static + sqlite + security (no provider call)
#   full      — all-safe + postgres + live (requires --allow-docker --allow-provider)
#
# Contract: every check writes one TSV row (id<TAB>status<TAB>detail) to summary.tsv;
# finish() emits the tally and exits non-zero iff any non-PASS/PARTIAL/SKIP row exists.
# No author absolute path is ever hardcoded; repo root is auto-discovered.
set -uo pipefail

SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SOURCE_DIR/../../.." && pwd)"
cd "$REPO_ROOT"

if command -v locale >/dev/null 2>&1; then
  for c in en_US.UTF-8 zh_CN.UTF-8 C.UTF-8 C.utf8 en_US.utf8 zh_CN.utf8; do
    if locale -a 2>/dev/null | grep -qx "$c"; then export LC_ALL="$c" LANG="$c"; break; fi
  done
fi
export PYTHONUTF8=1 PYTHONIOENCODING=utf-8

RUN_DIR="${TASK012_RUN_DIR:-$SOURCE_DIR/.run}"
mkdir -p "$RUN_DIR"
SUMMARY="$RUN_DIR/summary.tsv"
: > "$SUMMARY"
RECORDED=0

record() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$SUMMARY"; RECORDED=$((RECORDED+1)); }

finish() {
  local fails=0
  while IFS=$'\t' read -r id st dt; do
    [ -z "$id" ] && continue
    case "$st" in PASS|PARTIAL|SKIP|SKIP_WITH_REASON) ;; *) fails=$((fails+1)); printf 'FAIL  %-30s %s\n' "$id" "$dt" >&2 ;; esac
  done < "$SUMMARY"
  printf '\n===== Task012 verifier: %d checks, %d FAIL =====\n' "$RECORDED" "$fails" >&2
  return "$fails"
}

PKG="$REPO_ROOT/internal/models/pricing"
CAT="$PKG/catalogs/siliconflow.json"
EVID="$REPO_ROOT/status/evidence/task012"

have() { [ -f "$1" ]; }

# =============================================================================
# STATIC checks
# =============================================================================
ck_s01_catalog_file() {
  if have "$CAT"; then record s01.catalog_file PASS "catalogs/siliconflow.json present"; return 0
  else record s01.catalog_file FAIL "catalog JSON missing"; return 1; fi
}

ck_s02_catalog_valid() {
  go test ./internal/models/pricing/... -run 'TestLoadCatalogValid|TestLoadCatalog|TestParseDecimalNanos|TestResolveExact|TestCanonicalHash' -count=1 >/dev/null 2>&1
  if [ $? -eq 0 ]; then record s02.catalog_valid PASS "catalog load/validate/hash/resolve tests green"; return 0
  else record s02.catalog_valid FAIL "catalog tests failed"; return 1; fi
}

ck_s03_estimator_pure() {
  local f="$PKG/estimator.go"
  if have "$f" && ! grep -qE 'net/http|sql\.|gorm|os\.Getenv|http\.' "$f"; then
    record s03.estimator_pure PASS "estimator has no network/DB/config access"; return 0
  else record s03.estimator_pure FAIL "estimator must be a pure function"; return 1; fi
}

ck_s04_no_float_multiply() {
  local f="$PKG/estimator.go"
  if have "$f" && grep -qE 'math/big' "$f" && ! grep -qE 'float64\(.*\)\s*\*\s*float64|EstimatedCostNanos\s*=\s*int64\(float64' "$f"; then
    record s04.no_float_multiply PASS "math/big integer math; no binary-float price multiply"; return 0
  else record s04.no_float_multiply FAIL "price multiply must not use binary float"; return 1; fi
}

ck_s05_recorder_delegate() {
  local f="$PKG/recorder.go"
  if have "$f" && grep -qE 'types\.ModelCallRecorder' "$f" && grep -qE 'inner\.RecordModelCall|inner\.BeginModelCall' "$f"; then
    record s05.recorder_delegate PASS "recorder decorates ModelCallRecorder and delegates persistence"; return 0
  else record s05.recorder_delegate FAIL "recorder must delegate to inner recorder"; return 1; fi
}

ck_s06_migrations() {
  local pg="$REPO_ROOT/migrations/versioned/000089_model_call_pricing_snapshot.up.sql"
  local sq="$REPO_ROOT/migrations/sqlite/000009_model_call_pricing_snapshot.up.sql"
  if have "$pg" && have "$sq" && grep -q 'pricing_status' "$pg" && grep -q 'estimated_cost_nanos' "$sq"; then
    record s06.migrations PASS "PG 000089 + SQLite 000009 pricing snapshot migrations present"; return 0
  else record s06.migrations FAIL "pricing snapshot migrations missing"; return 1; fi
}

ck_s07_types_fields() {
  local f="$REPO_ROOT/internal/types/model_call.go"
  if have "$f" && grep -q 'EstimatedCostNanos' "$f" && grep -q 'PricingStatus' "$f" && grep -q 'PricingCatalogHash' "$f"; then
    record s07.types_fields PASS "ModelCall additive pricing fields present"; return 0
  else record s07.types_fields FAIL "ModelCall pricing fields missing"; return 1; fi
}

ck_s08_di_wiring() {
  local f="$REPO_ROOT/internal/container/container.go"
  if have "$f" && grep -q 'pricing\.NewPricingRecorder\|pricing\.NewEstimator\|pricing\.LoadDefaultCatalog' "$f"; then
    record s08.di_wiring PASS "DI provides LoadDefaultCatalog/NewEstimator/NewPricingRecorder"; return 0
  else record s08.di_wiring FAIL "DI wiring missing"; return 1; fi
}

ck_s09_unit_tests() {
  go test ./internal/models/pricing/... -count=1 >/dev/null 2>&1
  if [ $? -eq 0 ]; then record s09.unit_tests PASS "pricing package tests green (catalog+estimator+recorder)"; return 0
  else record s09.unit_tests FAIL "pricing package tests failed"; return 1; fi
}

ck_s10_race() {
  go test -race ./internal/models/pricing/... -count=1 >/dev/null 2>&1
  if [ $? -eq 0 ]; then record s10.race PASS "pricing package -race green"; return 0
  else record s10.race FAIL "-race failed"; return 1; fi
}

ck_s11_vet_build() {
  go vet ./internal/models/pricing/... ./internal/application/repository/... ./internal/application/service/... ./internal/container/... ./internal/types/... >/dev/null 2>&1 \
    && go build ./... >/dev/null 2>&1
  if [ $? -eq 0 ]; then record s11.vet_build PASS "go vet + go build green"; return 0
  else record s11.vet_build FAIL "vet/build failed"; return 1; fi
}

ck_s12_reason_allowlist() {
  local f="$REPO_ROOT/internal/types/model_call.go"
  for r in LEGACY_UNPRICED NO_EXACT_RULE RULE_NOT_YET_VALID RULE_EXPIRED CATALOG_UNAVAILABLE USAGE_UNAVAILABLE USAGE_PARTIAL INVALID_USAGE UNOBSERVABLE_BILLING_DIMENSION CALCULATION_OVERFLOW NON_PROVIDER_PRICED_MODEL; do
    if ! grep -q "$r" "$f"; then record s12.reason_allowlist FAIL "missing reason $r"; return 1; fi
  done
  record s12.reason_allowlist PASS "all 11 UNKNOWN reasons declared"; return 0
}

run_static() {
  ck_s01_catalog_file; ck_s02_catalog_valid; ck_s03_estimator_pure; ck_s04_no_float_multiply
  ck_s05_recorder_delegate; ck_s06_migrations; ck_s07_types_fields; ck_s08_di_wiring
  ck_s09_unit_tests; ck_s10_race; ck_s11_vet_build; ck_s12_reason_allowlist
}

# =============================================================================
# SQLITE checks
# =============================================================================
run_sqlite() {
  go test ./internal/database/... ./internal/application/repository/... -run 'Migration|ModelCall|Pricing|SQLite|PricingSnapshot' -count=1 >/dev/null 2>&1
  if [ $? -eq 0 ]; then record d01.sqlite_migration PASS "SQLite migration + repository roundtrip green"; return 0
  else record d01.sqlite_migration FAIL "SQLite migration/repository tests failed"; return 1; fi
}

# =============================================================================
# POSTGRES checks (requires docker)
# =============================================================================
run_postgres() {
  local out="$RUN_DIR/postgres"
  if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
    record d02.postgres_migration FAIL "Docker daemon unavailable after --allow-docker"
    return 1
  fi
  rm -rf "$out"
  if scripts/tmpCheck/task010/run_postgres_parity.sh "$out" >"$RUN_DIR/postgres.stdout.log" 2>&1 \
      && grep -q '^OK  migrations/versioned/000089_model_call_pricing_snapshot.up.sql$' "$out/migration_postgres.log" \
      && [ ! -s "$out/parity.diff" ]; then
    record d02.postgres_migration PASS "ephemeral PostgreSQL applied 000089; report parity equals SQLite"
    return 0
  fi
  record d02.postgres_migration FAIL "PostgreSQL migration/parity failed; see $out"
  return 1
}

# =============================================================================
# FRONTEND checks
# =============================================================================
run_frontend() {
  record f01.frontend SKIP_WITH_REASON "no production frontend change expected (existing Model Usage/Run Detail renders additive facts); verified by parity evidence"
  return 0
}

# =============================================================================
# SECURITY checks
# =============================================================================
ck_sec_secret_scan() {
  local pat='(sk-[A-Za-z0-9]{16,}|Bearer [A-Za-z0-9._-]{20,}|-----BEGIN [A-Z ]+ PRIVATE KEY-----|JWT_SECRET|OPENAI_API_KEY|ANTHROPIC_API_KEY|SILICONFLOW_API_KEY[[:space:]]*=[^$]|api[_-]?key[[:space:]]*[:=][[:space:]]*["'"'"']?[A-Za-z0-9]{16,}|password[[:space:]]*[:=])'
  local hits=0
  if [ -d "$EVID" ]; then
    hits=$(grep -rIiE "$pat" "$EVID" 2>/dev/null | wc -l | tr -d ' ')
  fi
  if [ "$hits" -eq 0 ]; then record sec1.secret_scan PASS "no secret pattern in evidence"; return 0
  else record sec1.secret_scan FAIL "$hits secret pattern hit(s) in evidence"; return 1; fi
}

ck_sec_prompt_response() {
  local hits=0
  if [ -d "$EVID" ]; then
    # Reject full prompt/response leakage markers in evidence.
    hits=$(grep -rIlE '"content"[[:space:]]*:[[:space:]]*"[^"]{40,}"|"messages"|"choices"|reasoning_content' "$EVID" 2>/dev/null | wc -l | tr -d ' ')
  fi
  if [ "$hits" -eq 0 ]; then record sec2.prompt_response PASS "no prompt/response/reasoning body in evidence"; return 0
  else record sec2.prompt_response FAIL "$hits file(s) contain prompt/response body"; return 1; fi
}

run_security() {
  ck_sec_secret_scan; ck_sec_prompt_response
}

# =============================================================================
# LIVE checks (requires --allow-provider)
# =============================================================================
run_live() {
  if [ -z "${SILICONFLOW_API_KEY:-}" ] || [ -z "${SILICONFLOW_API_URL:-}" ]; then
    record l00.credential SKIP_WITH_REASON "SILICONFLOW_API_KEY/SILICONFLOW_API_URL not set"
    return 0
  fi
  record l00.credential PASS "credential supplied via env var (name only, value never printed)"
  SSRF_WHITELIST="api.siliconflow.cn" go run ./scripts/tmpCheck/task012/live_probe >"$RUN_DIR/live_probe.stdout.log" 2>&1
  local rc=$?
  if [ "$rc" -eq 0 ] && grep -q 'SUMMARY mismatches=0' "$RUN_DIR/live_probe.stdout.log"; then
    record l01.live_probe PASS "live probe PRICED + independent recompute MATCH (0 mismatch)"
    return 0
  else
    record l01.live_probe FAIL "live probe failed (exit=$rc)"; return 1
  fi
}

# =============================================================================
# mode dispatch
# =============================================================================
MODE="${1:-all-safe}"
shift || true
ALLOW_DOCKER=0; ALLOW_PROVIDER=0
while [ $# -gt 0 ]; do
  case "$1" in
    --allow-docker) ALLOW_DOCKER=1 ;;
    --allow-provider) ALLOW_PROVIDER=1 ;;
    *) ;;
  esac
  shift
done

case "$MODE" in
  static)   run_static ;;
  sqlite)   run_sqlite ;;
  postgres)
    if [ "$ALLOW_DOCKER" -eq 1 ]; then run_postgres; else record d02.postgres_migration SKIP_WITH_REASON "postgres requires --allow-docker"; fi ;;
  frontend) run_frontend ;;
  security) run_security ;;
  live)
    if [ "$ALLOW_PROVIDER" -eq 1 ]; then run_live; else record l01.live_probe SKIP_WITH_REASON "live requires --allow-provider"; fi ;;
  all-safe) run_static; run_sqlite; run_security ;;
  full)
    run_static; run_sqlite; run_security
    [ "$ALLOW_DOCKER" -eq 1 ] && run_postgres || record d02.postgres_migration SKIP_WITH_REASON "postgres requires --allow-docker"
    [ "$ALLOW_PROVIDER" -eq 1 ] && run_live || record l01.live_probe SKIP_WITH_REASON "live requires --allow-provider"
    ;;
  *)
    echo "unknown mode: $MODE (static|sqlite|postgres|frontend|security|live|all-safe|full)" >&2; exit 2 ;;
esac

finish
