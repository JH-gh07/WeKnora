#!/usr/bin/env bash
# Task005 secret scan (evidence-only). Scans status/evidence/task005 for
# structural credential/secret/PII patterns. It does NOT hardcode any live
# secret literal; the exact known-literal check is performed separately (see
# verify_task005.sh) and recorded as a pass/fail count without echoing values.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
EVID="$REPO_ROOT/../status/evidence/task005"

PATTERNS=(
  'eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}'                 # JWT
  '(?i)bearer[[:space:]]+[A-Za-z0-9._-]{16,}'                                     # bearer token
  'sk-[A-Za-z0-9]{16,}'                                                           # openai-style key
  '(?i)(api[_-]?key|app[_-]?secret|jwt[_-]?secret|system[_-]?aes[_-]?key|db[_-]?password|secret)[[:space:]]*[:=][[:space:]]*["'"'"']?[A-Za-z0-9+/._-]{12,}'  # secret assignment with value
  '(?i)password[[:space:]]*[:=][[:space:]]*["'"'"'][^"'"'"']{4,}["'"'"']'         # password value
  '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'                                # email / PII
)

hits=0
while IFS= read -r -d '' f; do
  for p in "${PATTERNS[@]}"; do
    if grep -Eq "$p" "$f" 2>/dev/null; then
      snippet="$(grep -Eo "$p" "$f" | head -1 | cut -c1-48)"
      echo "PATTERN  ${f#$EVID/}  :: ${snippet}..."
      hits=$((hits+1))
    fi
  done
done < <(find "$EVID" -type f \( -name '*.md' -o -name '*.yaml' -o -name '*.log' -o -name '*.tsv' -o -name '*.json' -o -name '*.sh' \) -print0 2>/dev/null)

echo "secret_scan: $hits finding(s)"
[ "$hits" -eq 0 ]
