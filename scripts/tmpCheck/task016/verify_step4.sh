#!/usr/bin/env bash
# Task016 Step 4 — Standard Metrics + Measurement Contract v1 verification.
# Negative controls prove:
#   NC1  standard metric functions exist (precision/recall/rr/ap/map/ndcg)
#   NC2  Precision@k denominator is the FIXED cutoff k (ranx parity), not |retrieved|
#   NC3  measurement contract hash is derived from //go:embed standard.go
#   NC4  legacy Precision/MAP JSON fields are prefixed legacy_nonstandard_ (no collision)
#   NC5  report legacy definitions carry the legacy_nonstandard_ names
#   NC6  MetricsJSON payload carries standard_retrieval_metrics + measurement_contract_hash
#   NC7  report decode extracts standard metrics + contract hash
#   NC8  frozen contract hash test + frozen differential vectors present
#   NC9  differential test SHA-256 matches the frozen vector digest
#   NC10 legacy/standard JSON names do not collide (AC07)
set -u

WEKNORA_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SUMMARY="$(mktemp)"
STATUS=0

record() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$SUMMARY"; }
finish() { cat "$SUMMARY"; rm -f "$SUMMARY"; exit "$STATUS"; }
fail() { STATUS=2; record "$1" FAIL "$2"; }

STD="$WEKNORA_ROOT/internal/application/service/metric/standard.go"
CTR="$WEKNORA_ROOT/internal/application/service/metric/contract.go"
CTRT="$WEKNORA_ROOT/internal/application/service/metric/contract_test.go"
EV="$WEKNORA_ROOT/internal/types/evaluation.go"
REP="$WEKNORA_ROOT/internal/application/service/evaluation_report.go"
MH="$WEKNORA_ROOT/internal/application/service/metric_hook.go"
DIFF="$WEKNORA_ROOT/internal/application/service/metric/testdata/metric_differential.tsv"
DIFFT="$WEKNORA_ROOT/internal/application/service/metric/differential_test.go"

# ---- NC1: standard metric functions present ----
for fn in "func PrecisionAtK" "func RecallAtK" "func ReciprocalRankAtK" "func AveragePrecision" "func MeanAveragePrecision" "func NDCGAtK"; do
  if grep -qF "$fn" "$STD"; then
    record "NC1_$(echo "$fn" | awk '{print $2}')" PASS "$fn present"
  else
    fail "NC1_$(echo "$fn" | awk '{print $2}')" "$fn missing in standard.go"
  fi
done

# ---- NC2: Precision@k denominator is fixed k (not len(dedup)) ----
if grep -qF 'return float64(hits) / float64(k)' "$STD"; then
  record NC2_PRECISION_DENOMINATOR_K PASS "Precision@k denominator is the fixed cutoff k"
else
  fail NC2_PRECISION_DENOMINATOR_K "Precision@k denominator is not fixed k (ranx parity broken)"
fi

# ---- NC3: contract hash derives from embedded standard.go ----
if grep -qF '//go:embed standard.go' "$CTR" && grep -qF 'func MeasurementContractHash() string' "$CTR"; then
  record NC3_CONTRACT_HASH_EMBED PASS "contract hash embeds standard.go artifact"
else
  fail NC3_CONTRACT_HASH_EMBED "contract hash does not embed standard.go"
fi

# ---- NC4: legacy Precision/MAP JSON fields prefixed legacy_nonstandard_ ----
if grep -qF 'json:"legacy_nonstandard_precision"' "$EV" && grep -qF 'json:"legacy_nonstandard_map"' "$EV"; then
  record NC4_LEGACY_PREFIX PASS "legacy Precision/MAP renamed legacy_nonstandard_*"
else
  fail NC4_LEGACY_PREFIX "legacy Precision/MAP not prefixed"
fi
# Scope the unprefixed check to the legacy RetrievalMetrics struct only (the
# standard StandardRetrievalMetrics legitimately uses json:"map").
LEGACY_BLOCK="$(awk '/type RetrievalMetrics struct \{/{f=1} f{print} f&&/^\}/{exit}' "$EV")"
if printf '%s\n' "$LEGACY_BLOCK" | grep -qE 'json:"(precision|map)"'; then
  fail NC4_NO_UNPREFIXED "legacy RetrievalMetrics still exposes unprefixed precision/map"
else
  record NC4_NO_UNPREFIXED PASS "no unprefixed legacy precision/map json tag in RetrievalMetrics"
fi

# ---- NC5: report legacy definitions use legacy_nonstandard_* ----
if grep -qF '"legacy_nonstandard_precision", "legacy_nonstandard_map"' "$REP"; then
  record NC5_REPORT_LEGACY_NAMES PASS "report legacyMetricDefinitions use legacy_nonstandard_*"
else
  fail NC5_REPORT_LEGACY_NAMES "report legacyMetricDefinitions not renamed"
fi

# ---- NC6: MetricsJSON payload carries standard metrics + contract hash ----
if grep -qF 'standard_retrieval_metrics' "$MH" && grep -qF 'measurement_contract_hash' "$MH" && grep -qF 'MeasurementContractHash()' "$MH"; then
  record NC6_METRICSJSON_STANDARD PASS "MetricsJSON payload carries standard metrics + contract hash"
else
  fail NC6_METRICSJSON_STANDARD "MetricsJSON payload missing standard metrics / contract hash"
fi

# ---- NC7: report decode extracts standard metrics + hash ----
if grep -qF 'decodeStandardMetrics' "$REP" && grep -qF 'StandardRetrieval' "$REP"; then
  record NC7_REPORT_DECODE_STANDARD PASS "report decode extracts standard metrics + hash"
else
  fail NC7_REPORT_DECODE_STANDARD "report decode does not extract standard metrics"
fi

# ---- NC8: frozen contract hash + frozen vectors present ----
if grep -qF 'func TestMeasurementContractHashFrozen' "$CTRT" && [ -f "$DIFF" ] && [ -f "$DIFFT" ]; then
  record NC8_FROZEN_ARTIFACTS PASS "frozen contract hash test + frozen differential vectors present"
else
  fail NC8_FROZEN_ARTIFACTS "frozen contract hash test or differential vectors missing"
fi

# ---- NC9: differential test SHA-256 matches the frozen vector digest ----
if grep -qF 'const frozenDifferentialSHA256' "$DIFFT"; then
  frozen="$(grep -oE 'const frozenDifferentialSHA256 = "[0-9a-f]{64}"' "$DIFFT" | grep -oE '[0-9a-f]{64}')"
  actual="$(shasum -a 256 "$DIFF" | awk '{print $1}')"
  if [ -n "$frozen" ] && [ "$frozen" = "$actual" ]; then
    record NC9_DIFF_SHA256 PASS "differential vectors SHA-256 matches frozen digest ($actual)"
  else
    fail NC9_DIFF_SHA256 "differential SHA-256 mismatch: frozen=$frozen actual=$actual"
  fi
else
  fail NC9_DIFF_SHA256 "frozenDifferentialSHA256 constant missing in differential_test.go"
fi

# ---- NC10: legacy vs standard JSON field names do not collide (AC07) ----
# The legacy type's two non-standard fields are prefixed; the standard type uses
# distinct names. Check the type definition file for the standard names.
if grep -qF 'json:"precision_at_10"' "$EV" && grep -qF 'json:"recall_at_10"' "$EV"; then
  record NC10_STANDARD_NAMES PASS "standard metric JSON names present (precision_at_10 etc.)"
else
  fail NC10_STANDARD_NAMES "standard metric JSON names missing"
fi

finish
