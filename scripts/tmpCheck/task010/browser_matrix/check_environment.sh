#!/usr/bin/env bash
# Task010 Browser Matrix — Quick Environment Check
#
# 快速检查浏览器测试所需的环境和依赖是否就绪。
# 不执行实际测试，只验证前置条件。

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "=== Task010 Browser Test Environment Check ==="
echo ""

checks_passed=0
checks_failed=0

check() {
  local name="$1"
  local test_command="$2"
  local advice="${3:-}"

  printf "%-40s" "$name"
  if eval "$test_command" >/dev/null 2>&1; then
    echo -e "${GREEN}✓ OK${NC}"
    ((checks_passed++))
    return 0
  else
    echo -e "${RED}✗ MISSING${NC}"
    if [[ -n "$advice" ]]; then
      echo "  → $advice"
    fi
    ((checks_failed++))
    return 1
  fi
}

# System dependencies
echo "## System Dependencies"
check "Node.js" "command -v node" "Install: brew install node (macOS) or apt-get install nodejs (Linux)"
check "npm" "command -v npm" "Should be installed with Node.js"
check "jq" "command -v jq" "Install: brew install jq (macOS) or apt-get install jq (Linux)"

# Browser
echo ""
echo "## Browser"
if [[ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ]]; then
  echo -e "Chrome (macOS)                          ${GREEN}✓ OK${NC}"
  ((checks_passed++))
elif command -v google-chrome >/dev/null 2>&1; then
  echo -e "Chrome (Linux)                          ${GREEN}✓ OK${NC}"
  ((checks_passed++))
elif command -v chromium >/dev/null 2>&1; then
  echo -e "Chromium                                ${GREEN}✓ OK${NC}"
  ((checks_passed++))
else
  echo -e "Chrome/Chromium                         ${RED}✗ MISSING${NC}"
  echo "  → Install Google Chrome or Chromium browser"
  ((checks_failed++))
fi

# Playwright
echo ""
echo "## Playwright"
if node -e "require('playwright')" 2>/dev/null; then
  echo -e "Playwright module                       ${GREEN}✓ OK${NC}"
  ((checks_passed++))
else
  echo -e "Playwright module                       ${RED}✗ MISSING${NC}"
  echo "  → Install: npm install -g playwright"
  ((checks_failed++))
fi

# Environment variables
echo ""
echo "## Environment Configuration"
check "WEKNORA_BASE_URL" "[[ -n \"\${WEKNORA_BASE_URL:-}\" ]]" "Set: export WEKNORA_BASE_URL=http://localhost:80"
check "WEKNORA_AUTH_TOKEN" "[[ -n \"\${WEKNORA_AUTH_TOKEN:-}\" ]]" "Set: export WEKNORA_AUTH_TOKEN=\"<your-auth-token>\""
check "TEST_RUN_ID" "[[ -n \"\${TEST_RUN_ID:-}\" ]]" "Set: export TEST_RUN_ID=uuid-of-completed-run"

# Service availability
echo ""
echo "## Service Availability"
if [[ -n "${WEKNORA_BASE_URL:-}" ]]; then
  if curl -s -o /dev/null -w "%{http_code}" "$WEKNORA_BASE_URL" | grep -q "200\|301\|302"; then
    echo -e "Frontend ($WEKNORA_BASE_URL)                ${GREEN}✓ OK${NC}"
    ((checks_passed++))
  else
    echo -e "Frontend ($WEKNORA_BASE_URL)                ${RED}✗ UNREACHABLE${NC}"
    echo "  → Start services: ./scripts/start_all.sh or docker-compose up"
    ((checks_failed++))
  fi

  # Try to detect backend URL
  backend_url="${WEKNORA_BASE_URL%:*}:8080"
  if curl -s -o /dev/null -w "%{http_code}" "$backend_url/health" | grep -q "200"; then
    echo -e "Backend ($backend_url)                ${GREEN}✓ OK${NC}"
    ((checks_passed++))
  else
    echo -e "Backend ($backend_url)                ${YELLOW}⚠ UNREACHABLE${NC}"
    echo "  → May be proxied through frontend or on different port"
    # Don't count as failure, backend might be proxied
  fi
else
  echo -e "Frontend                                ${YELLOW}⚠ SKIP (WEKNORA_BASE_URL not set)${NC}"
  echo -e "Backend                                 ${YELLOW}⚠ SKIP (WEKNORA_BASE_URL not set)${NC}"
fi

# Summary
echo ""
echo "=== Summary ==="
echo -e "Passed: ${GREEN}$checks_passed${NC}"
echo -e "Failed: ${RED}$checks_failed${NC}"
echo ""

if [[ $checks_failed -eq 0 ]]; then
  echo -e "${GREEN}✓ Environment ready for browser tests${NC}"
  echo ""
  echo "Run tests with:"
  echo "  ./scripts/tmpCheck/task010/browser_matrix/run_browser_matrix.sh"
  exit 0
else
  echo -e "${RED}✗ Environment not ready. Please fix the issues above.${NC}"
  echo ""
  echo "See: scripts/tmpCheck/task010/browser_matrix/README.md for detailed setup instructions"
  exit 1
fi
