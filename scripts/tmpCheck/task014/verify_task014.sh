#!/usr/bin/env bash
# Task014 pilot verification gate.
# Checks that the pilot evidence is internally consistent and complete.
set -euo pipefail

EVID="$PWD/status/evidence/task014"
RAW="$PWD/status/raw/task014"
FAIL=0

say()  { printf '  %s\n' "$*"; }
pass() { printf '[PASS] %s\n' "$*"; }
fail() { printf '[FAIL] %s\n' "$*"; FAIL=1; }

echo "== Task014 pilot verification =="

# 1. engine manifest: exactly 8 engines, 3 available + 5 unsupported
if [ -f "$EVID/engine_manifest.json" ]; then
  N=$(python3 -c "import json;d=json.load(open('$EVID/engine_manifest.json'));print(len(d['engines']))" 2>/dev/null)
  AVAIL=$(python3 -c "import json;d=json.load(open('$EVID/engine_manifest.json'));print(sum(1 for e in d['engines'] if e['status']=='AVAILABLE'))" 2>/dev/null)
  UNS=$(python3 -c "import json;d=json.load(open('$EVID/engine_manifest.json'));print(sum(1 for e in d['engines'] if e['status']=='UNSUPPORTED'))" 2>/dev/null)
  [ "$N" = "8" ] && pass "engine_manifest: 8 engines" || fail "engine_manifest: expected 8, got $N"
  [ "$AVAIL" = "3" ] && pass "engine_manifest: 3 AVAILABLE" || fail "engine_manifest: 3 AVAILABLE expected, got $AVAIL"
  [ "$UNS" = "5" ] && pass "engine_manifest: 5 UNSUPPORTED" || fail "engine_manifest: 5 UNSUPPORTED expected, got $UNS"
else
  fail "engine_manifest.json missing"
fi

# 2. execution ledger: 1128 terminal records, all statuses closed
if [ -f "$EVID/execution_ledger.jsonl" ]; then
  LINES=$(wc -l < "$EVID/execution_ledger.jsonl" | tr -d ' ')
  say "execution_ledger.jsonl lines: $LINES (target 1128)"
  [ "$LINES" -ge 1000 ] && pass "ledger record count >= 1000" || fail "ledger record count < 1000"
  BAD=$(python3 - "$EVID/execution_ledger.jsonl" <<'PY'
import json,sys
ok={'SUCCESS','PARSER_ERROR','TIMEOUT','OOM','EMPTY_OUTPUT','UNSUPPORTED','INVALID_OUTPUT','FALLBACK_CONTAMINATED','INFRA_INVALIDATED'}
bad=0
for l in open(sys.argv[1]):
    d=json.loads(l)
    if d['status'] not in ok: bad+=1
    if d['requested_engine']!=d['effective_engine'] and d['status']!='FALLBACK_CONTAMINATED':
        bad+=1
print(bad)
PY
)
  [ "$BAD" = "0" ] && pass "ledger: all statuses closed, no silent fallback" || fail "ledger: $BAD invalid/fallback rows"
else
  fail "execution_ledger.jsonl missing"
fi

# 3. scorer outputs
for f in omnidocbench_results.tsv olmocr_assertion_results.tsv ohr_retrieval_results.tsv holdout_results.tsv; do
  if [ -f "$EVID/$f" ]; then
    n=$(wc -l < "$EVID/$f" | tr -d ' ')
    [ "$n" -gt 1 ] && pass "$f present ($n lines)" || fail "$f empty"
  else
    fail "$f missing"
  fi
done

# 4. aggregation
if [ -f "$EVID/statistical_tests.json" ]; then
  pass "statistical_tests.json present"
else
  fail "statistical_tests.json missing"
fi

# 5. sampled data presence
OM=$(ls "$RAW/omnidocbench/images" 2>/dev/null | wc -l | tr -d ' ')
OL=$(find "$RAW/olmocr/pdfs" -name '*.pdf' 2>/dev/null | wc -l | tr -d ' ')
OH=$(ls "$RAW/ohr/pdfs" 2>/dev/null | wc -l | tr -d ' ')
say "sampled data: OmniDocBench=$OM imgs, olmOCR=$OL pdfs, OHR=$OH pdfs"
[ "$OM" -ge 50 ] && pass "OmniDocBench >=50 images" || fail "OmniDocBench $OM <50"
[ "$OL" -ge 50 ] && pass "olmOCR >=50 pdfs" || fail "olmOCR $OL <50"

echo
if [ "$FAIL" = "0" ]; then
  echo "== RESULT: PILOT VERIFICATION PASS =="
else
  echo "== RESULT: PILOT VERIFICATION FAIL =="
fi
exit $FAIL
