#!/usr/bin/env bash
# Task007 GitHub governance closure helper.
#
# Safe default: `audit` is read-only and works for a public repository without
# credentials. `configure --apply` is the only mutating mode; it requires an
# authenticated repository administrator and an explicit repository guard.

set -euo pipefail

MODE="${1:-audit}"
[[ $# -gt 0 ]] && shift

REPO="${TASK007_REPOSITORY:-Tencent/WeKnora}"
CHECK_NAME="evaluation-regression / quality"
WORKFLOW_PATH=".github/workflows/evaluation-regression.yml"
OUT_DIR="${TASK007_GITHUB_AUDIT_DIR:-/tmp/task007-github-audit/$(date +%Y%m%d_%H%M%S)}"
APPLY=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) REPO="$2"; shift 2 ;;
    --out) OUT_DIR="$2"; shift 2 ;;
    --apply) APPLY=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 1 ;;
  esac
done

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "fatal: required command not found: $1" >&2
    exit 4
  }
}

need curl
need jq
mkdir -p "$OUT_DIR"

api_public() {
  local path="$1" output="$2" status
  local url="https://api.github.com/repos/$REPO"
  [[ -n "$path" ]] && url="$url/$path"
  status="$(curl -sS -L \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2022-11-28' \
    -o "$output" -w '%{http_code}' \
    "$url")"
  printf '%s' "$status"
}

has_authenticated_gh() {
  command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1
}

audit() {
  local repo_http rules_http workflow_http required=false workflow=false auth=false
  repo_http="$(api_public '' "$OUT_DIR/repository.json")"
  rules_http="$(api_public 'rulesets?per_page=100' "$OUT_DIR/rulesets.json")"
  workflow_http="$(api_public 'actions/workflows/evaluation-regression.yml' "$OUT_DIR/workflow.json")"

  if [[ "$repo_http" != "200" ]]; then
    jq -n --arg repo "$REPO" --arg http "$repo_http" \
      '{schema_version:"task007_github_audit/1",repository:$repo,status:"ERROR",repository_http:$http}' \
      > "$OUT_DIR/summary.json"
    echo "ERROR: repository metadata HTTP $repo_http; see $OUT_DIR" >&2
    return 4
  fi

  if [[ "$rules_http" == "200" ]] && jq -e --arg check "$CHECK_NAME" '
      any(.[ ];
        .enforcement == "active" and
        any(.rules[]?;
          .type == "required_status_checks" and
          any(.parameters.required_status_checks[]?; .context == $check)
        )
      )
    ' "$OUT_DIR/rulesets.json" >/dev/null; then
    required=true
  fi

  if [[ "$workflow_http" == "200" ]] &&
     [[ "$(jq -r '.state // ""' "$OUT_DIR/workflow.json")" == "active" ]]; then
    workflow=true
  fi

  if has_authenticated_gh; then
    auth=true
    gh api "repos/$REPO/branches/$(jq -r .default_branch "$OUT_DIR/repository.json")/protection" \
      > "$OUT_DIR/branch_protection.json" 2> "$OUT_DIR/branch_protection.err" || true
  else
    jq -n '{status:"UNVERIFIED",reason:"authenticated GitHub CLI unavailable"}' \
      > "$OUT_DIR/branch_protection.json"
  fi

  local status="BLOCKED_EXTERNAL_CONFIGURATION"
  if [[ "$required" == true && "$workflow" == true ]]; then status="CONFIGURED_AWAITING_REAL_PR_EVIDENCE"; fi

  jq -n \
    --arg repo "$REPO" \
    --arg check "$CHECK_NAME" \
    --arg workflow_path "$WORKFLOW_PATH" \
    --arg status "$status" \
    --argjson required "$required" \
    --argjson workflow "$workflow" \
    --argjson authenticated "$auth" \
    --arg rules_http "$rules_http" \
    --arg workflow_http "$workflow_http" \
    '{
      schema_version:"task007_github_audit/1",
      repository:$repo,
      status:$status,
      required_check:{context:$check,configured_in_active_ruleset:$required},
      workflow:{path:$workflow_path,active_on_repository:$workflow},
      authenticated_branch_protection_audit:$authenticated,
      http:{rulesets:$rules_http,workflow:$workflow_http}
    }' > "$OUT_DIR/summary.json"

  jq . "$OUT_DIR/summary.json"
  echo "audit_dir=$OUT_DIR"
  [[ "$required" == true && "$workflow" == true ]] && return 0
  return 3
}

configure() {
  need gh
  local pass_run_id="${TASK007_PASS_RUN_ID:-}"
  if [[ "$APPLY" -ne 1 ]]; then
    echo "refusing mutation: add --apply" >&2
    echo "also set TASK007_CONFIRM_REPO=$REPO" >&2
    return 4
  fi
  if [[ "${TASK007_CONFIRM_REPO:-}" != "$REPO" ]]; then
    echo "refusing mutation: TASK007_CONFIRM_REPO must equal $REPO" >&2
    return 4
  fi
  if ! gh auth status >/dev/null 2>&1; then
    echo "refusing mutation: GitHub CLI is not authenticated" >&2
    return 4
  fi
  if [[ "$(gh api "repos/$REPO" --jq '.permissions.admin // false')" != "true" ]]; then
    echo "refusing mutation: authenticated identity is not repository admin" >&2
    return 4
  fi
  if [[ -z "$pass_run_id" ]]; then
    echo "refusing mutation: TASK007_PASS_RUN_ID must name a successful pull_request run" >&2
    return 4
  fi

  # Prove the real check context before making it required. A typo here would
  # otherwise leave every pull request permanently Pending.
  gh api "repos/$REPO/actions/runs/$pass_run_id" > "$OUT_DIR/pass_run.json"
  gh api "repos/$REPO/actions/runs/$pass_run_id/jobs" > "$OUT_DIR/pass_run_jobs.json"
  if ! jq -e '
      .event == "pull_request" and
      .conclusion == "success" and
      .name == "Evaluation Quality Regression"
    ' "$OUT_DIR/pass_run.json" >/dev/null; then
    echo "refusing mutation: PASS run is not a successful pull_request Evaluation Quality Regression run" >&2
    return 4
  fi
  if ! jq -e --arg check "$CHECK_NAME" '
      any(.jobs[]?; .name == $check and .conclusion == "success")
    ' "$OUT_DIR/pass_run_jobs.json" >/dev/null; then
    echo "refusing mutation: PASS run lacks successful exact check context: $CHECK_NAME" >&2
    return 4
  fi

  audit >/dev/null 2>&1 && {
    echo "no-op: exact required check and active workflow already present"
    return 0
  }

  local payload="$OUT_DIR/create_ruleset_payload.json"
  jq -n --arg check "$CHECK_NAME" '{
    name:"Task007 Evaluation Quality Regression",
    target:"branch",
    enforcement:"active",
    conditions:{ref_name:{include:["~DEFAULT_BRANCH"],exclude:[]}},
    rules:[{
      type:"required_status_checks",
      parameters:{
        strict_required_status_checks_policy:true,
        required_status_checks:[{context:$check}]
      }
    }]
  }' > "$payload"

  gh api --method POST "repos/$REPO/rulesets" --input "$payload" \
    > "$OUT_DIR/created_ruleset.json"
  echo "created dedicated ruleset id=$(jq -r .id "$OUT_DIR/created_ruleset.json")"
  echo "evidence=$OUT_DIR/created_ruleset.json"
}

case "$MODE" in
  audit) audit ;;
  configure) configure ;;
  *)
    echo "usage: $0 [audit|configure] [--repo OWNER/REPO] [--out DIR] [--apply]" >&2
    exit 1
    ;;
esac
