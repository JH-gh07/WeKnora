#!/usr/bin/env bash

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
MODE="${1:-all}"
[ "$#" -gt 0 ] && shift
ALLOW_DOCKER=0
START_LOCAL=0
for arg in "$@"; do
  case "$arg" in
    --allow-docker) ALLOW_DOCKER=1 ;;
    --start-local) START_LOCAL=1 ;;
    *) printf 'unknown option: %s\n' "$arg" >&2; exit 1 ;;
  esac
done

REPORT_DIR="${TASK004_REPORT_DIR:-/tmp/task004-check-$(date +%Y%m%d-%H%M%S)}"
SUMMARY="$REPORT_DIR/summary.tsv"
APP_CONTAINER="task004-app"
BUILDER_IMAGE="weknora-task004-builder:local"
RUNTIME_IMAGE="wechatopenai/weknora-app:latest"
APP_URL="${TASK004_APP_URL:-http://127.0.0.1:18080}"
UI_URL="${TASK004_UI_URL:-http://127.0.0.1:15173}"
RUN_TAG="t004_$(date +%s)_$$"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/task004.XXXXXX")"
VITE_PID=""
FAIL=0
BLOCKED=0

mkdir -p "$REPORT_DIR/live_cases"
printf 'gate\tstatus\tdetail\n' >"$SUMMARY"

record() {
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >>"$SUMMARY"
  printf '[%s] %s - %s\n' "$2" "$1" "$3"
  [ "$2" != FAIL ] || FAIL=$((FAIL + 1))
  [ "$2" != BLOCKED ] || BLOCKED=$((BLOCKED + 1))
}

run_logged() {
  local gate="$1" log="$2"; shift 2
  if "$@" >"$REPORT_DIR/$log" 2>&1; then record "$gate" PASS "$log"; else record "$gate" FAIL "$log"; fi
}

psql_exec() {
  docker exec -i WeKnora-postgres sh -lc 'exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" "$POSTGRES_DB" "$@"' task004-psql "$@"
}

cleanup() {
  if command -v agent-browser >/dev/null 2>&1; then
    agent-browser --session task004 close >/dev/null 2>&1 || true
    agent-browser --session task004b close >/dev/null 2>&1 || true
  fi
  if [ -n "$VITE_PID" ]; then kill "$VITE_PID" >/dev/null 2>&1 || true; fi
  if [ "$START_LOCAL" -eq 1 ]; then docker rm -f "$APP_CONTAINER" >/dev/null 2>&1 || true; fi
  if [ -f "$TMP_DIR/tenant-a" ]; then
    local ta tb
    ta="$(cat "$TMP_DIR/tenant-a")"; tb="$(cat "$TMP_DIR/tenant-b" 2>/dev/null || true)"
    psql_exec >/dev/null 2>&1 <<SQL || true
DELETE FROM model_metering_health WHERE id LIKE '${RUN_TAG}%';
DELETE FROM model_calls WHERE id LIKE '${RUN_TAG}%';
DELETE FROM models WHERE id LIKE '${RUN_TAG}%';
DELETE FROM tenant_api_keys WHERE name LIKE '${RUN_TAG}%';
DELETE FROM auth_tokens WHERE user_id IN (SELECT id FROM users WHERE username LIKE '${RUN_TAG}%');
UPDATE tenant_members SET deleted_at=COALESCE(deleted_at,NOW()), updated_at=NOW()
  WHERE user_id IN (SELECT id FROM users WHERE username LIKE '${RUN_TAG}%')
     OR tenant_id IN (SELECT tenant_id FROM users WHERE username LIKE '${RUN_TAG}%');
UPDATE tenants SET deleted_at=COALESCE(deleted_at,NOW()), status='inactive', updated_at=NOW()
  WHERE id IN (SELECT tenant_id FROM users WHERE username LIKE '${RUN_TAG}%');
UPDATE users SET deleted_at=COALESCE(deleted_at,NOW()), is_active=false, updated_at=NOW()
  WHERE username LIKE '${RUN_TAG}%';
SQL
    local residue
    residue="$(psql_exec -At 2>/dev/null <<SQL || true
SELECT (SELECT COUNT(*) FROM model_calls WHERE id LIKE '${RUN_TAG}%') +
       (SELECT COUNT(*) FROM model_metering_health WHERE id LIKE '${RUN_TAG}%') +
       (SELECT COUNT(*) FROM users WHERE username LIKE '${RUN_TAG}%' AND deleted_at IS NULL) +
       (SELECT COUNT(*) FROM tenants WHERE name LIKE '${RUN_TAG}%' AND deleted_at IS NULL);
SQL
)"
    printf 'fixture_residue=%s\n' "${residue:-unknown}" >"$REPORT_DIR/fixture_cleanup.log"
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

run_static() {
  cd "$REPO_ROOT" || exit 1
  run_logged backend-contract backend_contract.log go test ./internal/models/chat ./internal/application/repository ./internal/database ./internal/handler -run 'TestMeteredChat|TestModelCall|TestSQLiteMigrationsCreateModelCalls|TestModelUsage' -count=1
  run_logged frontend-contract frontend_contract.log bash -lc 'cd frontend && npx tsx --test src/views/settings/modelUsageState.test.ts src/views/settings/modelUsageRequestController.test.ts'
  run_logged frontend-typecheck frontend_typecheck.log bash -lc 'cd frontend && npm run type-check'
  run_logged frontend-i18n frontend_i18n.log bash -lc 'cd frontend && npm run check-i18n'
  run_logged frontend-build frontend_build.log bash -lc 'cd frontend && npm run build'
  if git diff --check >"$REPORT_DIR/diff_check.log" 2>&1; then record diff-check PASS diff_check.log; else record diff-check FAIL diff_check.log; fi
}

wait_http() {
  local url="$1" i
  for i in $(seq 1 90); do curl -fsS "$url" >/dev/null 2>&1 && return 0; sleep 1; done
  return 1
}

register_and_login() {
  local label="$1" email="$2" password="$3"
  jq -n --arg username "${RUN_TAG}-${label}" --arg email "$email" --arg password "$password" '{username:$username,email:$email,password:$password}' |
    curl -fsS -H 'Content-Type: application/json' -d @- "$APP_URL/api/v1/auth/register" >/dev/null
  jq -n --arg email "$email" --arg password "$password" '{email:$email,password:$password}' |
    curl -fsS -H 'Content-Type: application/json' -d @- "$APP_URL/api/v1/auth/login" -o "$TMP_DIR/$label-login.json"
}

bearer_get() {
  local token="$1" url="$2" out="$3"
  curl -fsS -H "Authorization: Bearer $token" "$url" -o "$out"
}

capture_browser_case() {
  local session="$1" case_id="$2" pattern="$3" dir
  dir="$REPORT_DIR/live_cases/$case_id"
  mkdir -p "$dir"
  agent-browser --session "$session" snapshot >"$dir/snapshot.txt" 2>&1
  agent-browser --session "$session" screenshot --full "$dir/page.png" >"$dir/screenshot.log" 2>&1
  if rg -q "$pattern" "$dir/snapshot.txt"; then
    record "$case_id-ui" PASS 'snapshot + screenshot'
  else
    record "$case_id-ui" FAIL "expected UI pattern absent: $pattern"
  fi
}

select_usage_model() {
  local session="$1" model_name="$2"
  # Settings is rendered once as the route surface and once in the foreground
  # overlay. The second control is the pointer-active foreground instance.
  agent-browser --session "$session" find nth 1 '[aria-label="model-usage-model"]' click >/dev/null
  agent-browser --session "$session" wait 200 >/dev/null
  agent-browser --session "$session" find first ".t-select-option:has-text(\"$model_name\")" click >/dev/null
  agent-browser --session "$session" wait 3000 >/dev/null
}

run_live() {
  if [ "$ALLOW_DOCKER" -ne 1 ]; then record live BLOCKED 'requires --allow-docker'; return; fi
  for tool in docker curl jq agent-browser; do command -v "$tool" >/dev/null 2>&1 || { record tools FAIL "missing $tool"; return; }; done
  cd "$REPO_ROOT" || exit 1

  if [ "$START_LOCAL" -eq 1 ]; then
    docker rm -f "$APP_CONTAINER" >/dev/null 2>&1 || true
    if ! docker image inspect "$BUILDER_IMAGE" >/dev/null 2>&1; then
      run_logged backend-image backend_image.log docker build --target builder -f docker/Dockerfile.app --build-arg WITH_ANYDOC=0 -t "$BUILDER_IMAGE" .
      [ "$FAIL" -eq 0 ] || return
    fi
    local builder_container
    builder_container="$(docker create "$BUILDER_IMAGE")"
    if ! docker cp "$builder_container:/app/WeKnora" "$TMP_DIR/WeKnora" >"$REPORT_DIR/binary_extract.log" 2>&1; then
      docker rm "$builder_container" >/dev/null 2>&1 || true
      record binary-extract FAIL binary_extract.log
      return
    fi
    docker rm "$builder_container" >/dev/null 2>&1 || true
    chmod +x "$TMP_DIR/WeKnora"
    if ! docker run -d --name "$APP_CONTAINER" --network weknora_WeKnora-network -p 18080:8080 \
      --env-file .env -e DB_HOST=postgres -e REDIS_ADDR=redis:6379 -e DOCREADER_ADDR=docreader:50051 \
      -v "$REPO_ROOT/config/config.yaml:/app/config/config.yaml" \
      -v "$REPO_ROOT/migrations:/app/migrations:ro" \
      -v "$TMP_DIR/WeKnora:/app/WeKnora:ro" \
      "$RUNTIME_IMAGE" >"$REPORT_DIR/app_start.log" 2>&1; then
      record app-start FAIL app_start.log; return
    fi
    VITE_DEV_PROXY_TARGET="$APP_URL" npm --prefix frontend run dev -- --port 15173 >"$REPORT_DIR/vite.log" 2>&1 &
    VITE_PID=$!
  fi
  wait_http "$APP_URL/health" || { record app-health FAIL 'local backend unavailable'; return; }
  wait_http "$UI_URL/login" || { record ui-health FAIL 'local Vite unavailable'; return; }
  record local-stack PASS 'isolated backend + Vite ready'

  local suffix password email_a email_b email_v token_a token_b token_v tenant_a tenant_b viewer_id switched
  suffix="$(date +%s)$$"; password="T004-${suffix}-Aa9!"
  email_a="task004-a-${suffix}@example.invalid"; email_b="task004-b-${suffix}@example.invalid"; email_v="task004-v-${suffix}@example.invalid"
  register_and_login t004a "$email_a" "$password" || { record identities FAIL registration; return; }
  register_and_login t004b "$email_b" "$password" || { record identities FAIL registration; return; }
  register_and_login t004v "$email_v" "$password" || { record identities FAIL registration; return; }
  token_a="$(jq -r '.token // .data.token' "$TMP_DIR/t004a-login.json")"
  token_b="$(jq -r '.token // .data.token' "$TMP_DIR/t004b-login.json")"
  token_v="$(jq -r '.token // .data.token' "$TMP_DIR/t004v-login.json")"
  tenant_a="$(jq -r '.active_tenant.id // .tenant.id // .data.tenant.id' "$TMP_DIR/t004a-login.json")"
  tenant_b="$(jq -r '.active_tenant.id // .tenant.id // .data.tenant.id' "$TMP_DIR/t004b-login.json")"
  viewer_id="$(jq -r '.user.id // .data.user.id' "$TMP_DIR/t004v-login.json")"
  printf '%s' "$tenant_a" >"$TMP_DIR/tenant-a"; printf '%s' "$tenant_b" >"$TMP_DIR/tenant-b"

  psql_exec >"$REPORT_DIR/fixture_seed.log" <<SQL
INSERT INTO tenant_members (user_id, tenant_id, role, status, joined_at, created_at, updated_at)
VALUES ('$viewer_id',$tenant_a,'viewer','active',NOW(),NOW(),NOW()) ON CONFLICT DO NOTHING;
INSERT INTO models (id,tenant_id,name,display_name,type,source,description,parameters,is_default,status,created_at,updated_at)
VALUES
('${RUN_TAG}_chat_a',$tenant_a,'fixture-chat-a','Fixture Chat A','KnowledgeQA','remote','Task004 fixture','{}',false,'active',NOW(),NOW()),
('${RUN_TAG}_chat_b',$tenant_a,'fixture-chat-b','Fixture Chat B','KnowledgeQA','remote','Task004 fixture','{}',false,'active',NOW(),NOW()),
('${RUN_TAG}_unreported',$tenant_a,'fixture-unreported','Fixture Unreported','KnowledgeQA','remote','Task004 fixture','{}',false,'active',NOW(),NOW()),
('${RUN_TAG}_empty',$tenant_a,'fixture-empty','Fixture Empty','Embedding','remote','Task004 fixture','{}',false,'active',NOW(),NOW()),
('${RUN_TAG}_vlm',$tenant_a,'fixture-vlm','Fixture VLM','VLLM','remote','Task004 fixture','{}',false,'active',NOW(),NOW()),
('${RUN_TAG}_asr',$tenant_a,'fixture-asr','Fixture ASR','ASR','remote','Task004 fixture','{}',false,'active',NOW(),NOW()),
('${RUN_TAG}_tenant_b',$tenant_b,'fixture-tenant-b','Fixture Tenant B','KnowledgeQA','remote','Task004 fixture','{}',false,'active',NOW(),NOW());
INSERT INTO model_calls (id,tenant_id,model_id,model_name,provider,operation,input_tokens,output_tokens,cache_read_tokens,cache_miss_tokens,cache_reported_input_tokens,usage_finality,cache_status,request_elapsed_ms,success,attempt_observability,estimated_cost,currency,pricing_version,pricing_source,created_at)
VALUES
('${RUN_TAG}_a1',$tenant_a,'${RUN_TAG}_chat_a','Fixture Chat A','fixture','chat',100,10,80,20,100,'reported','hit',10,true,'unobservable',0.100000,'USD','fixture-v1','task004',NOW()),
('${RUN_TAG}_a2',$tenant_a,'${RUN_TAG}_chat_a','Fixture Chat A','fixture','chat',100,10,0,100,100,'reported','miss',10,true,'unobservable',0.200000,'USD','fixture-v1','task004',NOW()),
('${RUN_TAG}_a3',$tenant_a,'${RUN_TAG}_chat_a','Fixture Chat A','fixture','chat',50,5,NULL,NULL,NULL,'reported','unreported',10,true,'unobservable',NULL,'','', '',NOW()),
('${RUN_TAG}_b1',$tenant_a,'${RUN_TAG}_chat_b','Fixture Chat B','fixture','chat',NULL,NULL,NULL,NULL,NULL,'unavailable','unsupported',10,false,'unobservable',NULL,'','', '',NOW()),
('${RUN_TAG}_u1',$tenant_a,'${RUN_TAG}_unreported','Fixture Unreported','fixture','chat',50,5,NULL,NULL,NULL,'reported','unreported',10,true,'unobservable',NULL,'','', '',NOW()),
('${RUN_TAG}_tb1',$tenant_b,'${RUN_TAG}_tenant_b','Fixture Tenant B','fixture','chat',10,1,NULL,NULL,NULL,'reported','unsupported',10,true,'unobservable',NULL,'','', '',NOW());
INSERT INTO model_metering_health (id,tenant_id,attempted_at,persisted) VALUES
('${RUN_TAG}_h1',$tenant_a,NOW(),true),('${RUN_TAG}_h2',$tenant_a,NOW(),true),('${RUN_TAG}_h3',$tenant_a,NOW(),true),('${RUN_TAG}_h4',$tenant_a,NOW(),true),('${RUN_TAG}_h5',$tenant_a,NOW(),true);
SQL
  record fixture-seed PASS fixture_seed.log

  switched="$(jq -n --argjson tenant_id "$tenant_a" '{tenant_id:$tenant_id}' | curl -fsS -H 'Content-Type: application/json' -H "Authorization: Bearer $token_v" -d @- "$APP_URL/api/v1/auth/switch-tenant")"
  token_v="$(printf '%s' "$switched" | jq -r '.token // .data.token')"

  local from to base
  from="$(date -u -v-1d '+%Y-%m-%dT%H:%M:%SZ')"; to="$(date -u -v+1d '+%Y-%m-%dT%H:%M:%SZ')"
  base="$APP_URL/api/v1/model-usage?from=$from&to=$to"
  local anon_code viewer_code tenant_b_count
  anon_code="$(curl -sS -o "$REPORT_DIR/live_cases/B02-anonymous.json" -w '%{http_code}' "$base")"
  [ "$anon_code" = 401 ] && record B02a PASS 'anonymous=401' || record B02a FAIL "anonymous=$anon_code"
  viewer_code="$(curl -sS -o "$REPORT_DIR/live_cases/B02-viewer.json" -w '%{http_code}' -H "Authorization: Bearer $token_v" "$base")"
  [ "$viewer_code" = 200 ] && record B01-B02b PASS 'viewer read=200' || record B01-B02b FAIL "viewer=$viewer_code"

  local denied_key_json allowed_key_json denied_key allowed_key denied_code allowed_code
  denied_key_json="$(jq -n --arg name "${RUN_TAG}_denied" '{name:$name,full_access:false,knowledge_base_ids:[],capabilities:["read_agents"]}')"
  allowed_key_json="$(jq -n --arg name "${RUN_TAG}_allowed" '{name:$name,full_access:false,knowledge_base_ids:[],capabilities:["manage_models"]}')"
  printf '%s' "$denied_key_json" | curl -fsS -H 'Content-Type: application/json' -H "Authorization: Bearer $token_a" -d @- "$APP_URL/api/v1/tenants/$tenant_a/api-keys" -o "$TMP_DIR/denied-key.json"
  printf '%s' "$allowed_key_json" | curl -fsS -H 'Content-Type: application/json' -H "Authorization: Bearer $token_a" -d @- "$APP_URL/api/v1/tenants/$tenant_a/api-keys" -o "$TMP_DIR/allowed-key.json"
  denied_key="$(jq -r '.data.token // .token' "$TMP_DIR/denied-key.json")"
  allowed_key="$(jq -r '.data.token // .token' "$TMP_DIR/allowed-key.json")"
  denied_code="$(curl -sS -o "$REPORT_DIR/live_cases/B02-api-key-denied.json" -w '%{http_code}' -H "X-API-Key: $denied_key" "$base")"
  allowed_code="$(curl -sS -o "$REPORT_DIR/live_cases/B02-api-key-allowed.json" -w '%{http_code}' -H "X-API-Key: $allowed_key" "$base")"
  [ "$denied_code" = 403 ] && record B02c PASS 'scoped key without manage_models=403' || record B02c FAIL "denied-key=$denied_code"
  [ "$allowed_code" = 200 ] && record B02d PASS 'scoped key with manage_models=200' || record B02d FAIL "allowed-key=$allowed_code"
  bearer_get "$token_a" "$base" "$REPORT_DIR/live_cases/B03-tenant-a.json"
  bearer_get "$token_b" "$base" "$REPORT_DIR/live_cases/B03-tenant-b.json"
  tenant_b_count="$(jq -r '.data.logical_call_count' "$REPORT_DIR/live_cases/B03-tenant-b.json")"
  [ "$tenant_b_count" = 1 ] && record B03 PASS 'tenant isolation exact' || record B03 FAIL "tenant-b=$tenant_b_count"
  bearer_get "$token_a" "$base&model_id=${RUN_TAG}_chat_a" "$REPORT_DIR/live_cases/B04-model-a.json"
  jq -e '.data.logical_call_count==3 and .data.cache_reported_input_tokens==200 and .data.currency=="USD" and .data.mixed_currency==false' "$REPORT_DIR/live_cases/B04-model-a.json" >/dev/null && record B04-B11 PASS 'model/filter/cache/currency contract exact' || record B04-B11 FAIL 'API values differ'
  bearer_get "$token_a" "$base&model_id=${RUN_TAG}_empty" "$REPORT_DIR/live_cases/B06-empty.json"
  jq -e '.data.logical_call_count==0 and .data.measurement_status=="COMPLETE"' "$REPORT_DIR/live_cases/B06-empty.json" >/dev/null && record B06-B13 PASS 'empty scope retains independent health' || record B06-B13 FAIL 'empty/health contract differs'
  bearer_get "$token_a" "$base&model_id=${RUN_TAG}_chat_b" "$REPORT_DIR/live_cases/B09-unsupported.json"
  jq -e '.data.logical_call_count==1 and .data.cache_unsupported_count==1' "$REPORT_DIR/live_cases/B09-unsupported.json" >/dev/null && record B09 PASS 'unsupported distinct from unreported' || record B09 FAIL 'unsupported contract differs'
  bearer_get "$token_a" "$base&model_id=${RUN_TAG}_unreported" "$REPORT_DIR/live_cases/B10-unreported.json"
  jq -e '.data.logical_call_count==1 and .data.cache_eligible_count==1 and .data.cache_reported_count==0' "$REPORT_DIR/live_cases/B10-unreported.json" >/dev/null && record B10 PASS 'unreported distinct from unsupported/miss' || record B10 FAIL 'unreported contract differs'
  jq -e '.data.measurement_status=="COMPLETE" and .data.metering_attempted_count==5 and .data.metering_persisted_count==5 and .data.metering_failed_count==0' "$REPORT_DIR/live_cases/B03-tenant-a.json" >/dev/null && record health-complete PASS 'tenant-window health=COMPLETE' || record health-complete FAIL 'COMPLETE health differs'
  jq -e '.data.measurement_status=="UNKNOWN"' "$REPORT_DIR/live_cases/B03-tenant-b.json" >/dev/null && record B08 PASS 'missing health=UNKNOWN' || record B08 FAIL 'UNKNOWN health differs'

  # Browser: use the same viewer role through the real login form. Credentials
  # remain process-local and all command output containing them is discarded.
  export AGENT_BROWSER_ALLOWED_DOMAINS='127.0.0.1,localhost'
  export AGENT_BROWSER_CONTENT_BOUNDARIES=1
  export AGENT_BROWSER_DEFAULT_TIMEOUT=10000
  if [ -z "${AGENT_BROWSER_EXECUTABLE_PATH:-}" ]; then
    local bundled_chrome
    bundled_chrome="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    if [ -x "$bundled_chrome" ]; then export AGENT_BROWSER_EXECUTABLE_PATH="$bundled_chrome"; fi
  fi
  agent-browser --session task004 close >/dev/null 2>&1 || true
  agent-browser --session task004b close >/dev/null 2>&1 || true
  agent-browser --session task004anon close >/dev/null 2>&1 || true
  agent-browser --session task004 open "$UI_URL/login" >/dev/null || { record browser-launch FAIL 'cannot launch browser'; return; }
  (agent-browser --session task004 find placeholder '输入邮箱地址' fill "$email_v" >/dev/null 2>&1 || agent-browser --session task004 find placeholder 'Enter email address' fill "$email_v" >/dev/null) || { record browser-login FAIL 'email field'; return; }
  (agent-browser --session task004 find placeholder '输入密码（8-32个字符，包含字母和数字）' fill "$password" >/dev/null 2>&1 || agent-browser --session task004 find placeholder 'Enter password (8-32 characters, including letters and numbers)' fill "$password" >/dev/null) || { record browser-login FAIL 'password field'; return; }
  (agent-browser --session task004 find role button click --name '登录' >/dev/null 2>&1 || agent-browser --session task004 find role button click --name 'Login' >/dev/null) || { record browser-login FAIL 'submit'; return; }
  agent-browser --session task004 wait 1500 >/dev/null
  # The viewer owns a disposable registration tenant as well. Replace the
  # browser token with the server-issued tenant-A switch token so B01 proves
  # the viewer role against the seeded workspace rather than the owner role.
  agent-browser --session task004 storage local set weknora_token "$token_v" >/dev/null
  agent-browser --session task004 open "$UI_URL/platform/settings?section=models" >/dev/null
  agent-browser --session task004 wait --text 'Model Usage' >/dev/null 2>&1 || agent-browser --session task004 wait --text '模型用量' >/dev/null
  # A new disposable account opens the product tour, whose spotlight clones
  # accessible targets and makes otherwise unique controls appear twice.
  agent-browser --session task004 find nth 0 'button:has-text("跳过引导")' click >/dev/null 2>&1 || agent-browser --session task004 find nth 0 'button:has-text("Skip tour")' click >/dev/null 2>&1 || true
  agent-browser --session task004 wait 500 >/dev/null
  mkdir -p "$REPORT_DIR/live_cases/B01"
  agent-browser --session task004 screenshot --full "$REPORT_DIR/live_cases/B01/page.png" >"$REPORT_DIR/browser_screenshot.log" 2>&1
  agent-browser --session task004 snapshot >"$REPORT_DIR/live_cases/B01/snapshot.txt" 2>&1
  if rg -q '模型用量|Model Usage' "$REPORT_DIR/live_cases/B01/snapshot.txt" && ! rg -q '添加模型|Add Model' "$REPORT_DIR/live_cases/B01/snapshot.txt"; then
    record browser-viewer PASS 'usage visible; management action absent'
  else
    record browser-viewer FAIL 'viewer DOM contract failed'
  fi

  if rg -q '空间窗口计量完整|Workspace-window measurement complete' "$REPORT_DIR/live_cases/B01/snapshot.txt"; then
    record health-complete-ui PASS 'COMPLETE rendered from live health facts'
  else
    record health-complete-ui FAIL 'COMPLETE UI state absent'
  fi
  psql_exec >"$REPORT_DIR/live_cases/B07-health-transition.log" <<SQL
UPDATE model_metering_health SET persisted=false WHERE id='${RUN_TAG}_h5';
SQL
  bearer_get "$token_a" "$base" "$REPORT_DIR/live_cases/B07-partial.json"
  jq -e '.data.measurement_status=="PARTIAL" and .data.metering_attempted_count==5 and .data.metering_persisted_count==4 and .data.metering_failed_count==1' "$REPORT_DIR/live_cases/B07-partial.json" >/dev/null && record B07 PASS 'tenant-window health=PARTIAL' || record B07 FAIL 'PARTIAL health differs'
  agent-browser --session task004 find nth 1 'label.t-radio-button:has(input[value="7d"])' click >/dev/null
  agent-browser --session task004 wait 1000 >/dev/null
  capture_browser_case task004 B07 '空间窗口计量不完整|Workspace-window measurement partial'
  if rg -q 'Fixture VLM' "$REPORT_DIR/live_cases/B01/snapshot.txt" && rg -q 'Fixture ASR' "$REPORT_DIR/live_cases/B01/snapshot.txt" && rg -q '尚未计量|Metering not implemented' "$REPORT_DIR/live_cases/B01/snapshot.txt"; then
    record B14-ui PASS 'VLM/ASR cards explicitly unmetered'
  else
    record B14-ui FAIL 'VLM/ASR boundary absent'
  fi

  select_usage_model task004 'Fixture Chat A'
  capture_browser_case task004 B04 '3|USD 0\.300000'
  if rg -q 'USD 0\.300000' "$REPORT_DIR/live_cases/B04/snapshot.txt" && rg -q '1 次调用价格未知|1 calls have unknown pricing' "$REPORT_DIR/live_cases/B04/snapshot.txt"; then
    record B11-ui PASS 'known USD subtotal + unknown-price count'
  else
    record B11-ui FAIL 'cost identity/subtotal rendering differs'
  fi

  select_usage_model task004 'Fixture Chat B'
  capture_browser_case task004 B09 'Provider 不支持|Provider unsupported'
  select_usage_model task004 'Fixture Unreported'
  capture_browser_case task004 B10 'Provider 未上报|Provider did not report'
  select_usage_model task004 'Fixture Empty'
  capture_browser_case task004 B06 '该窗口内无逻辑调用|No logical calls in this window'
  if rg -q '尚未实现|Not implemented' "$REPORT_DIR/live_cases/B06/snapshot.txt" && rg -q '空间窗口计量不完整|Workspace-window measurement partial' "$REPORT_DIR/live_cases/B06/snapshot.txt"; then
    record B13-B15-ui PASS 'empty model preserves local-cache and tenant health truths'
  else
    record B13-B15-ui FAIL 'independent empty-state facts absent'
  fi

  agent-browser --session task004 network requests --clear >/dev/null
  agent-browser --session task004 find nth 1 'label.t-radio-button:has(input[value="24h"])' click >/dev/null
  agent-browser --session task004 wait 3000 >/dev/null
  agent-browser --session task004 network requests --filter 'model-usage' --json >"$REPORT_DIR/live_cases/B05-network.json" 2>&1
  mkdir -p "$REPORT_DIR/live_cases/B05"
  agent-browser --session task004 snapshot >"$REPORT_DIR/live_cases/B05/snapshot.txt" 2>&1
  agent-browser --session task004 screenshot --full "$REPORT_DIR/live_cases/B05/page.png" >/dev/null 2>&1
  local ui_range ui_from ui_to
  ui_range="$(rg -o '[0-9]{4}-[0-9:.TZ-]+ → [0-9]{4}-[0-9:.TZ-]+' "$REPORT_DIR/live_cases/B05/snapshot.txt" | tail -1)"
  ui_from="${ui_range%% → *}"; ui_to="${ui_range##* → }"
  if node -e 'const a=Date.parse(process.argv[1]),b=Date.parse(process.argv[2]);process.exit(Number.isFinite(a)&&Number.isFinite(b)&&b>a&&b-a===86400000?0:1)' "$ui_from" "$ui_to"; then
    record B05-ui PASS 'foreground 24h UTC range is exact and ordered'
  else
    record B05-ui FAIL 'foreground UTC range is not exact 24h'
  fi

  agent-browser --session task004 network route '**/api/v1/model-usage*' --abort >/dev/null
  agent-browser --session task004 find nth 1 'label.t-radio-button:has(input[value="30d"])' click >/dev/null
  agent-browser --session task004 wait 1000 >/dev/null
  capture_browser_case task004 B12-error '加载用量失败|Failed to load usage'
  agent-browser --session task004 network unroute '**/api/v1/model-usage*' >/dev/null
  agent-browser --session task004 find text '重试' click --exact >/dev/null 2>&1 || agent-browser --session task004 find text 'Retry' click --exact >/dev/null
  agent-browser --session task004 wait 3000 >/dev/null
  capture_browser_case task004 B12-retry '该窗口内无逻辑调用|No logical calls in this window'

  # Reuse the same browser context for a real tenant switch. Replacing the
  # server-issued JWT and navigating immediately also verifies stale tenant-A
  # facts are masked before tenant-B UNKNOWN arrives.
  agent-browser --session task004 storage local set weknora_token "$token_b" >/dev/null
  agent-browser --session task004 open "$UI_URL/platform/settings?section=models" >/dev/null 2>&1 || true
  agent-browser --session task004 wait 3000 >/dev/null
  capture_browser_case task004 B08 '空间窗口计量未知|Workspace-window measurement unknown'

  agent-browser --session task004 storage local clear >/dev/null
  agent-browser --session task004 open "$UI_URL/platform/settings?section=models" >/dev/null 2>&1 || true
  agent-browser --session task004 wait 1000 >/dev/null
  mkdir -p "$REPORT_DIR/live_cases/B02-anonymous-page"
  agent-browser --session task004 snapshot >"$REPORT_DIR/live_cases/B02-anonymous-page/snapshot.txt" 2>&1
  if rg -q '登录|Login|邮箱|Email' "$REPORT_DIR/live_cases/B02-anonymous-page/snapshot.txt" && ! rg -q 'Fixture Chat A' "$REPORT_DIR/live_cases/B02-anonymous-page/snapshot.txt"; then
    record B02a-ui PASS 'anonymous redirected; no cached tenant facts'
  else
    record B02a-ui FAIL 'anonymous page boundary differs'
  fi
}

case "$MODE" in
  static) run_static ;;
  live) run_live ;;
  all) run_static; run_live ;;
  *) printf 'usage: %s [static|live|all] [--allow-docker] [--start-local]\n' "$0"; exit 1 ;;
esac

trap - EXIT INT TERM
cleanup
if rg -n 'Bearer[[:space:]]+[A-Za-z0-9._-]{20,}|sk-[A-Za-z0-9_-]{20,}' "$REPORT_DIR" >"$REPORT_DIR/secret_scan.log" 2>&1; then
  record secret-scan FAIL 'credential-shaped text found in report'
else
  record secret-scan PASS secret_scan.log
fi

printf '\nSummary: %s\n' "$SUMMARY"
[ "$FAIL" -eq 0 ] || exit 1
[ "$BLOCKED" -eq 0 ] || exit 2
exit 0
