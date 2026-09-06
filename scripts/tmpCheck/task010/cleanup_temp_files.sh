#!/usr/bin/env bash
# Clean up temporary test evidence and artifacts before git commit
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=== Cleaning up temporary files under $SCRIPT_DIR ==="

# Remove temporary evidence directories created during testing
if [[ -d "$SCRIPT_DIR/status" ]]; then
  echo "Removing $SCRIPT_DIR/status/"
  rm -rf "$SCRIPT_DIR/status"
fi

if [[ -d "$SCRIPT_DIR/browser_matrix/browser_evidence_quick" ]]; then
  echo "Removing $SCRIPT_DIR/browser_matrix/browser_evidence_quick/"
  rm -rf "$SCRIPT_DIR/browser_matrix/browser_evidence_quick"
fi

# Remove node_modules (should not be committed)
if [[ -d "$SCRIPT_DIR/browser_matrix/node_modules" ]]; then
  echo "Removing $SCRIPT_DIR/browser_matrix/node_modules/"
  rm -rf "$SCRIPT_DIR/browser_matrix/node_modules"
fi

# Remove temporary documentation files
temp_docs=(
  "DELIVERY_CHECKLIST.md"
  "FINAL_SUMMARY.txt"
  "00-START-HERE.txt"
  "SUMMARY.md"
  "BROWSER_AUTOMATION_COMPARISON.md"
  "COMPLETION_CHECKLIST.md"
  "WORK_COMPLETION_REPORT.md"
  "START_HERE.sh"
  "quick_validate_agentbrowser.sh"
  "QUICK_VALIDATE_GUIDE.md"
  "INDEX.md"
  "basic_validation.js"
)

for doc in "${temp_docs[@]}"; do
  if [[ -f "$SCRIPT_DIR/browser_matrix/$doc" ]]; then
    echo "Removing $SCRIPT_DIR/browser_matrix/$doc"
    rm -f "$SCRIPT_DIR/browser_matrix/$doc"
  fi
done

echo ""
echo "=== Cleanup complete ==="
echo "Files remaining:"
find "$SCRIPT_DIR" -type f | grep -v ".git" | sort
