#!/usr/bin/env bash
# Self-contained Task010 browser matrix: real report stack + source Vite UI + Chromium.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
EVIDENCE_DIR="${TASK010_BROWSER_EVIDENCE:-${STATUS_EVIDENCE:-$PROJECT_ROOT/../status/evidence/task010}/browser_evidence}"
HEADLESS="${HEADLESS:-true}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/task010-browser.XXXXXX")"
BACKEND_ADDR_FILE="$TMP_DIR/backend.addr"
BACKEND_PID=""
FRONTEND_PID=""
FIXTURE_BIN="$TMP_DIR/live_http"

cleanup() {
  local exit_code=$?
  [[ -n "$FRONTEND_PID" ]] && kill "$FRONTEND_PID" 2>/dev/null || true
  [[ -n "$BACKEND_PID" ]] && kill "$BACKEND_PID" 2>/dev/null || true
  [[ -n "$FRONTEND_PID" ]] && wait "$FRONTEND_PID" 2>/dev/null || true
  [[ -n "$BACKEND_PID" ]] && wait "$BACKEND_PID" 2>/dev/null || true
  rm -rf "$TMP_DIR"
  exit "$exit_code"
}
trap cleanup EXIT INT TERM

for tool in go node npm curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "ERROR: missing tool: $tool" >&2; exit 2; }
done
[[ -d "$PROJECT_ROOT/frontend/node_modules" ]] || { echo "ERROR: frontend dependencies are not installed" >&2; exit 2; }
node -e "require('$SCRIPT_DIR/node_modules/playwright')" >/dev/null 2>&1 || {
  echo "ERROR: browser_matrix Playwright dependency is not installed" >&2
  exit 2
}

mkdir -p "$EVIDENCE_DIR"

if ! (cd "$PROJECT_ROOT" && go build -o "$FIXTURE_BIN" ./scripts/tmpCheck/task010/live_http); then
  echo "ERROR: could not build report fixture" >&2
  exit 2
fi
(exec "$FIXTURE_BIN" --serve "$TMP_DIR/backend" "$BACKEND_ADDR_FILE") >"$TMP_DIR/backend.log" 2>&1 &
BACKEND_PID=$!

# First-run `go run` may spend over 30s compiling the fixture on a cold cache.
# Keep the bounded wait, but allow a realistic two-minute startup window.
for _ in $(seq 1 480); do
  [[ -s "$BACKEND_ADDR_FILE" ]] && break
  kill -0 "$BACKEND_PID" 2>/dev/null || { cat "$TMP_DIR/backend.log" >&2; exit 2; }
  sleep 0.25
done
[[ -s "$BACKEND_ADDR_FILE" ]] || { echo "ERROR: report fixture did not start" >&2; exit 2; }
BACKEND_URL="$(cat "$BACKEND_ADDR_FILE")"

FRONTEND_PORT="$(node -e "const s=require('net').createServer();s.listen(0,'127.0.0.1',()=>{console.log(s.address().port);s.close()})")"
(
  cd "$PROJECT_ROOT/frontend"
  VITE_DEV_PROXY_TARGET="$BACKEND_URL" exec npm run dev -- --host 127.0.0.1 --port "$FRONTEND_PORT" --strictPort
) >"$TMP_DIR/frontend.log" 2>&1 &
FRONTEND_PID=$!
FRONTEND_URL="http://127.0.0.1:$FRONTEND_PORT"

for _ in $(seq 1 240); do
  if curl -fsS "$FRONTEND_URL/" >/dev/null 2>&1; then break; fi
  kill -0 "$FRONTEND_PID" 2>/dev/null || { cat "$TMP_DIR/frontend.log" >&2; exit 2; }
  sleep 0.25
done
curl -fsS "$FRONTEND_URL/" >/dev/null || { echo "ERROR: Vite source server did not start" >&2; exit 2; }

export WEKNORA_BASE_URL="$FRONTEND_URL"
export TASK010_BROWSER_EVIDENCE="$EVIDENCE_DIR"
export HEADLESS

cd "$SCRIPT_DIR"
node run_browser_tests.js

# Fail closed on incomplete/duplicate/non-PASS matrices even if the JS process
# is accidentally weakened later.
node - "$EVIDENCE_DIR/browser_matrix.tsv" <<'NODE'
const fs = require('fs');
const rows = fs.readFileSync(process.argv[2], 'utf8').trim().split(/\r?\n/).slice(1).map((line) => line.split('\t'));
const expected = Array.from({length: 18}, (_, i) => `B${String(i + 1).padStart(2, '0')}`);
if (rows.length !== 18 || new Set(rows.map((r) => r[0])).size !== 18 ||
    !expected.every((id) => rows.some((r) => r[0] === id && r[1] === 'PASS'))) {
  console.error('ERROR: browser matrix must contain exactly B01-B18, all PASS');
  process.exit(1);
}
NODE

cp "$TMP_DIR/backend.log" "$EVIDENCE_DIR/fixture_backend.log"
cp "$TMP_DIR/frontend.log" "$EVIDENCE_DIR/vite_frontend.log"
echo "Task010 browser matrix: PASS (18/18 strict)"
