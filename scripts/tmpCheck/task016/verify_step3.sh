#!/usr/bin/env bash
# Task016 Step 3 — Stable Lineage 垂直切片 verification (negative controls).
# Proves the identity-based lineage chain holds and the old content-matching
# mapping is gone:
#   NC1: strings.Contains content matching removed from metric_hook mapping
#   NC2: stable SourcePassageID field present on SearchResult / ParsedChunk / metadata
#   NC3: passage creation writes SourcePassageID (strconv.Itoa of slice index)
#   NC4: search result propagation reads chunk.SourcePassageIDValue()
#   NC5: identity mapper never content-matches; LINEAGE_UNAVAILABLE is explicit
set -u

WEKNORA_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SUMMARY="$(mktemp)"
STATUS=0

record() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$SUMMARY"; }
finish() { cat "$SUMMARY"; rm -f "$SUMMARY"; exit "$STATUS"; }
fail() { STATUS=2; record "$1" FAIL "$2"; }

MH="$WEKNORA_ROOT/internal/application/service/metric_hook.go"
SR="$WEKNORA_ROOT/internal/types/search.go"
DP="$WEKNORA_ROOT/internal/types/docparser.go"
FQ="$WEKNORA_ROOT/internal/types/faq.go"
KP="$WEKNORA_ROOT/internal/application/service/knowledge_process.go"
KBSR="$WEKNORA_ROOT/internal/application/service/knowledgebase_search_results.go"

# ---- NC1: no content matching in the metric mapping path ----
if grep -nqE 'strings\.Contains\((passage|r\.Content)' "$MH"; then
  fail NC1_NO_CONTENT_MATCH "strings.Contains content matching still present in metric_hook.go"
else
  record NC1_NO_CONTENT_MATCH PASS "metric hook no longer content-matches retrieved chunks"
fi

# ---- NC2: SourcePassageID field present on all three carriers ----
for pair in "search:$SR" "docparser:$DP" "faq:$FQ"; do
  label="${pair%%:*}"
  f="${pair#*:}"
  if grep -qE 'SourcePassageID[[:space:]]+string' "$f"; then
    record "NC2_FIELD_${label}" PASS "SourcePassageID field present in ${label}"
  else
    fail "NC2_FIELD_${label}" "missing SourcePassageID field in ${label}"
  fi
done

# ---- NC3: passage creation writes stable identity ----
if grep -qE 'SourcePassageID:[[:space:]]*strconv\.Itoa\(i\)' "$KP"; then
  record NC3_WRITE_AT_CREATION PASS "passage creation writes SourcePassageID=itoa(slice index)"
else
  fail NC3_WRITE_AT_CREATION "passage creation does not write SourcePassageID"
fi

# ---- NC4: search result propagates identity verbatim ----
if grep -qE 'SourcePassageID:[[:space:]]*chunk\.SourcePassageIDValue\(\)' "$KBSR"; then
  record NC4_SEARCH_PROPAGATION PASS "buildSearchResult propagates chunk.SourcePassageIDValue()"
else
  fail NC4_SEARCH_PROPAGATION "buildSearchResult does not propagate SourcePassageID"
fi

# ---- NC5: identity mapper is explicit, no guessing ----
if grep -qE 'func mapRetrievalToPassageIDs' "$MH" && \
   grep -qE 'lineageUnavailable\+\+' "$MH" && \
   grep -qE 'strconv\.Atoi\(r\.SourcePassageID\)' "$MH"; then
  record NC5_IDENTITY_MAPPER PASS "identity mapper counts LINEAGE_UNAVAILABLE, never guesses"
else
  fail NC5_IDENTITY_MAPPER "identity mapper missing or still guessing"
fi

# ---- NC6: LINEAGE_UNAVAILABLE audit signal surfaced ----
if grep -qE 'func \(h \*HookMetric\) LineageUnavailable\(\) int' "$MH"; then
  record NC6_AUDIT_SIGNAL PASS "LineageUnavailable() audit getter present"
else
  fail NC6_AUDIT_SIGNAL "LineageUnavailable() getter missing"
fi

finish
