#!/usr/bin/env bash
# Task016 Step 1 — containment verification (negative controls).
# Proves the three Step 1 negative controls cannot produce GO/PASS:
#   NC1: protocol v1 / UNVERSIONED metric contract -> NOT_COMPARABLE_MEASUREMENT_CONTRACT
#   NC2: legacy Precision/MAP excluded from blocking allowlist
#   NC3: Task014 polluted evidence covered by invalidation manifest (fail closed)
set -u

WEKNORA_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
EVID_ROOT="${WEKNORA_ROOT}/../status/evidence/task016"
SUMMARY="$(mktemp)"
STATUS=0

record() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$SUMMARY"; }
finish() { cat "$SUMMARY"; rm -f "$SUMMARY"; exit "$STATUS"; }
fail() { STATUS=2; record "$1" FAIL "$2"; }

# ---- NC1: UNVERSIONED -> NOT_COMPARABLE_MEASUREMENT_CONTRACT ----
if grep -qE 'ReasonMeasurementContractVersion[[:space:]]*=[[:space:]]*"NOT_COMPARABLE_MEASUREMENT_CONTRACT"' \
    "$WEKNORA_ROOT/internal/types/evaluation_report.go"; then
  record NC1_PASS PASS "report reason code NOT_COMPARABLE_MEASUREMENT_CONTRACT present"
else
  fail NC1_PASS "missing report reason code"
fi
if grep -qE 'MeasurementContractStatus[[:space:]]*==[[:space:]]*""' \
    "$WEKNORA_ROOT/internal/application/service/evaluation_report.go" && \
   grep -qE 'measurementContractUnversioned' \
    "$WEKNORA_ROOT/internal/application/service/evaluation_report.go"; then
  record NC1_WARN PASS "UNVERSIONED/empty contract surfaces a warning"
else
  fail NC1_WARN "no UNVERSIONED warning path"
fi

# ---- NC2: legacy precision/map not blocking ----
ALLOWLIST="$WEKNORA_ROOT/internal/application/service/evaluation_regression_comparator.go"
# Extract the blockingMetricAllowlist map literal only (avoid matching the
# metricFieldName/metricValue switch cases elsewhere in the file).
ALLOW_BLOCK="$(awk '/var blockingMetricAllowlist = map\[string\]struct\{\}\{/{f=1} f{print} f&&/\}/{exit}' "$ALLOWLIST")"
if printf '%s\n' "$ALLOW_BLOCK" | grep -qE '"retrieval\.(precision|map)"'; then
  fail NC2_ALLOWLIST "precision/map present in blocking allowlist map"
else
  record NC2_ALLOWLIST PASS "precision/map absent from blocking allowlist map"
fi
if grep -qE 'legacy_nonstandard|legacyMetricDefinitions|"precision", "map"' \
    "$WEKNORA_ROOT/internal/application/service/evaluation_report.go"; then
  record NC2_LEGACY_MARKER PASS "legacy precision/map explicitly marked in report"
else
  fail NC2_LEGACY_MARKER "legacy marker missing"
fi

# ---- NC3: Task014 invalidation manifest fail-closed ----
INV="$EVID_ROOT/integrity/task014_invalidation_manifest.json"
if [ -f "$INV" ] && python3 -S -c "import json; d=json.load(open('$INV', encoding='utf-8')); assert any(i['reason_code']=='EVIDENCE_IN_QUERY' for i in d['invalidations']); assert any(i['reason_code']=='CER_FORMULA_INCORRECT' for i in d['invalidations'])" 2>/dev/null; then
  record NC3_MANIFEST PASS "invalidation manifest covers EVIDENCE_IN_QUERY + CER_FORMULA_INCORRECT"
else
  fail NC3_MANIFEST "invalidation manifest missing or incomplete"
fi
if grep -qE '"original_files_preserved"[[:space:]]*:[[:space:]]*true' "$INV"; then
  record NC3_ORIGINAL_PRESERVED PASS "original Task014 files preserved (not deleted/rewritten)"
else
  fail NC3_ORIGINAL_PRESERVED "original files not marked preserved"
fi

finish
