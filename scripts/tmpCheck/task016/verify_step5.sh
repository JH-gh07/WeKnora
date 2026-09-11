#!/usr/bin/env bash
# Task016 Step 5 — Protocol v2 + Comparability verification.
# Negative controls prove:
#   NC1  v2 protocol file exists with four separated identity hashes
#   NC2  comparator returns enum status + mismatched_fields[] (never bare bool)
#   NC3  measurement vs metric comparability are distinct (UNVERSIONED -> NOT_COMPARABLE_MEASUREMENT, changed hash -> NOT_COMPARABLE_METRIC)
#   NC4  model computation fingerprint carries secret-free digests only (no api_key/token/password)
#   NC5  provenance fields are NOT part of quality comparison identity (I05)
#   NC6  measurement_contract_hash reuses the Step 4 metric contract (metric package)
#   NC7  schema version is "evaluation_protocol/2" (not unversioned)
#   NC8  W3 mutation test + TSV generator test present
#   NC9  identity_mutation.tsv frozen SHA-256 matches (>=30 rows, all PASS)
#   NC10 secret scanner test present (TestProtocolV2SecretFree)
#   NC11 comparator enum coverage test present (>=3 per state)
#   NC12 targeted Go tests pass
set -u

WEKNORA_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SUMMARY="$(mktemp)"
STATUS=0

record() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$SUMMARY"; }
finish() { cat "$SUMMARY"; rm -f "$SUMMARY"; exit "$STATUS"; }
fail() { STATUS=2; record "$1" FAIL "$2"; }

V2="$WEKNORA_ROOT/internal/application/service/evaluation_protocol_v2.go"
V2T="$WEKNORA_ROOT/internal/application/service/evaluation_protocol_v2_test.go"
TSVGEN="$WEKNORA_ROOT/internal/application/service/evaluation_protocol_v2_tsv_test.go"
STATUS_EVIDENCE="$WEKNORA_ROOT/../status/evidence/task016/correctness/identity_mutation.tsv"

# ---- NC1: four identity hashes ----
for f in QualityHash PerformanceHash ProvenanceHash MeasurementContractHash; do
  if grep -qE "^\t${f} " "$V2"; then
    record "NC1_${f}" PASS "${f} present in ProtocolV2"
  else
    fail "NC1_${f}" "${f} missing"
  fi
done

# ---- NC2: comparator returns enum + mismatched_fields ----
if grep -qF 'type ComparabilityResult struct' "$V2" && grep -qF 'MismatchedFields []string' "$V2"; then
  record NC2_COMPARATOR_FIELDS PASS "comparator returns status + mismatched_fields[]"
else
  fail NC2_COMPARATOR_FIELDS "comparator missing status/mismatched_fields"
fi

# ---- NC3: measurement vs metric distinction ----
if grep -qF 'NotComparableMeasurement' "$V2" \
   && grep -qF 'NotComparableMetric' "$V2" \
   && grep -qF 'func normalizeContractHashV2' "$V2"; then
  record NC3_MEASUREMENT_VS_METRIC PASS "distinct measurement/metric comparability states"
else
  fail NC3_MEASUREMENT_VS_METRIC "measurement vs metric not distinguished"
fi

# ---- NC4: secret-free model fingerprint (field NAMES only, not comments) ----
FP_BLOCK="$(awk '/type ModelComputationFingerprint struct \{/{f=1} f{print} f&&/^\}/{exit}' "$V2")"
if printf '%s\n' "$FP_BLOCK" | grep -qE 'json:"(api_key|apikey|token|secret|password|credential|authorization)"'; then
  fail NC4_FINGERPRINT_SECRET_FREE "model fingerprint leaks a secret field name"
else
  record NC4_FINGERPRINT_SECRET_FREE PASS "fingerprint carries digest fields only (secret-free)"
fi
if printf '%s\n' "$FP_BLOCK" | grep -qF 'EndpointDigest' && printf '%s\n' "$FP_BLOCK" | grep -qF 'ParamsDigest'; then
  record NC4_FINGERPRINT_DIGESTS PASS "endpoint/params are digest-only"
else
  fail NC4_FINGERPRINT_DIGESTS "fingerprint missing digest-only identity"
fi

# ---- NC5: provenance not in quality identity ----
if grep -qF 'type RunProvenanceV2 struct' "$V2" && ! grep -qF 'Provenance' <(awk '/func diffQualityFields/{f=1} f{print} f&&/^}/{exit}' "$V2"); then
  record NC5_PROVENANCE_NOT_QUALITY PASS "provenance fields excluded from quality diff"
else
  fail NC5_PROVENANCE_NOT_QUALITY "provenance leaked into quality identity"
fi

# ---- NC6: measurement contract reuses metric package ----
if grep -qF 'metric.MeasurementContractHash()' "$V2"; then
  record NC6_METRIC_CONTRACT_REUSE PASS "v2 reuses Step 4 metric contract hash"
else
  fail NC6_METRIC_CONTRACT_REUSE "v2 does not reuse metric contract hash"
fi

# ---- NC7: schema version ----
if grep -qF 'evaluationProtocolSchemaVersionV2 = "evaluation_protocol/2"' "$V2"; then
  record NC7_SCHEMA_V2 PASS "schema version is evaluation_protocol/2"
else
  fail NC7_SCHEMA_V2 "schema version not v2"
fi

# ---- NC8: mutation + TSV generator tests present ----
if grep -qF 'func TestProtocolV2IdentityMutation' "$V2T" && grep -qF 'func TestGenerateIdentityMutationTSV' "$TSVGEN"; then
  record NC8_MUTATION_TESTS PASS "W3 mutation test + TSV generator present"
else
  fail NC8_MUTATION_TESTS "mutation test or TSV generator missing"
fi

# ---- NC9: frozen identity_mutation.tsv SHA-256 ----
FROZEN_TSV_SHA="ea79b6ed10a31ba338c849ef5e34bd25f447172e2c6bfbfced7b270f8101869f"
if [ -f "$STATUS_EVIDENCE" ]; then
  actual="$(shasum -a 256 "$STATUS_EVIDENCE" | awk '{print $1}')"
  rows="$(tail -n +2 "$STATUS_EVIDENCE" | wc -l | tr -d ' ')"
  passrows="$(awk -F'\t' 'NR>1 && $8=="true"{c++} END{print c+0}' "$STATUS_EVIDENCE")"
  if [ "$actual" = "$FROZEN_TSV_SHA" ] && [ "$rows" -ge 30 ] && [ "$passrows" = "$rows" ]; then
    record NC9_FROZEN_TSV PASS "identity_mutation.tsv SHA ok, $rows rows all PASS"
  else
    fail NC9_FROZEN_TSV "TSV drift: sha=$actual rows=$rows pass=$passrows"
  fi
else
  fail NC9_FROZEN_TSV "identity_mutation.tsv evidence missing at $STATUS_EVIDENCE"
fi

# ---- NC10: secret scanner test ----
if grep -qF 'func TestProtocolV2SecretFree' "$V2T"; then
  record NC10_SECRET_SCANNER PASS "secret scanner test present"
else
  fail NC10_SECRET_SCANNER "secret scanner test missing"
fi

# ---- NC11: comparator enum coverage test ----
if grep -qF 'func TestCompareQualityProtocolEnumStates' "$V2T"; then
  record NC11_ENUM_COVERAGE PASS "comparator enum coverage test present"
else
  fail NC11_ENUM_COVERAGE "comparator enum coverage test missing"
fi

# ---- NC12: targeted Go tests ----
if (cd "$WEKNORA_ROOT" && go test -count=1 -run 'TestBuildProtocolV2|TestProtocolV2|TestCompareQualityProtocolEnumStates|TestGenerateIdentityMutationTSV' ./internal/application/service/ >/dev/null 2>&1); then
  record NC12_GO_TESTS PASS "targeted Step 5 Go tests pass"
else
  fail NC12_GO_TESTS "targeted Step 5 Go tests failed"
fi

finish
