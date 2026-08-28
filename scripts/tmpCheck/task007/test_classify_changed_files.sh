#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
CLASSIFIER="$ROOT/scripts/tmpCheck/task007/classify_changed_files.sh"
OUT="${1:-/tmp/task007-classifier-tests.tsv}"
printf 'case\tstatus\tdetail\n' > "$OUT"
failures=0

run_case() {
  local name="$1" want_protected="$2" want_retrieval="$3"
  shift 3
  local input output protected retrieval
  input="$(mktemp /tmp/task007-classifier-input.XXXXXX)"
  output="$(mktemp /tmp/task007-classifier-output.XXXXXX)"
  while [[ "$#" -gt 0 ]]; do
    printf '%s\0%s\0' "$1" "$2" >> "$input"
    shift 2
  done
  "$CLASSIFIER" < "$input" > "$output" 2>/dev/null
  protected="$(awk -F= '$1=="protected" {print $2}' "$output")"
  retrieval="$(awk -F= '$1=="retrieval" {print $2}' "$output")"
  rm -f "$input" "$output"
  if [[ "$protected" == "$want_protected" && "$retrieval" == "$want_retrieval" ]]; then
    printf '%s\tPASS\tprotected=%s retrieval=%s\n' "$name" "$protected" "$retrieval" >> "$OUT"
  else
    printf '%s\tFAIL\twant=%s/%s got=%s/%s\n' "$name" "$want_protected" "$want_retrieval" "$protected" "$retrieval" >> "$OUT"
    failures=$((failures + 1))
  fi
}

run_case sut_modified                  0 0 M internal/application/service/knowledgebase_search_fusion.go
run_case sut_deleted                   0 1 D internal/application/service/knowledgebase_search_fusion.go
run_case unrelated                     0 0 M docs/readme.md
run_case unrelated_space               0 0 M "docs/file with space.md"
run_case unrelated_newline             0 0 M $'docs/file\nwith-newline.md'
run_case evaluator                     1 0 M internal/application/service/evaluation_regression.go
run_case comparator                    1 0 M internal/application/service/evaluation_regression_comparator.go
run_case metric                        1 0 M internal/application/service/metric/recall.go
run_case policy                        1 0 M tests/evaluation/policies/quality_core_v1.json
run_case workflow                      1 0 M .github/workflows/evaluation-regression.yml
run_case verifier                      1 0 M scripts/tmpCheck/task007/verify_task007.sh
run_case knowledge                     0 1 M internal/application/service/knowledge.go
run_case pipeline                      0 1 M internal/application/service/chat_pipeline/retrieval.go
run_case retriever                     0 1 M internal/application/service/retriever/foo.go
run_case retrieval_type                0 1 M internal/types/retrieval_result.go
run_case config                        0 1 M internal/config/config.go
run_case mixed_protected_and_retrieval 1 1 M tests/evaluation/fixtures/x.json M internal/config/config.go

# Integration proof: --no-renames must surface BOTH the protected old path and
# unrelated new path, so a rename cannot escape classification.
repo="$(mktemp -d /tmp/task007-classifier-git.XXXXXX)"
git -C "$repo" init -q
git -C "$repo" config user.email task007@example.invalid
git -C "$repo" config user.name task007
mkdir -p "$repo/tests/evaluation" "$repo/docs"
printf 'frozen\n' > "$repo/tests/evaluation/policy.json"
git -C "$repo" add .
git -C "$repo" commit -qm base
base="$(git -C "$repo" rev-parse HEAD)"
git -C "$repo" mv tests/evaluation/policy.json docs/renamed.json
git -C "$repo" commit -qm rename
head="$(git -C "$repo" rev-parse HEAD)"
diff_file="$(mktemp /tmp/task007-classifier-diff.XXXXXX)"
result_file="$(mktemp /tmp/task007-classifier-result.XXXXXX)"
git -C "$repo" diff --no-renames --name-status -z "$base" "$head" > "$diff_file"
"$CLASSIFIER" < "$diff_file" > "$result_file" 2>/dev/null
protected="$(awk -F= '$1=="protected" {print $2}' "$result_file")"
if [[ "$protected" == "1" ]]; then
  printf 'protected_rename_integration\tPASS\told protected path classified\n' >> "$OUT"
else
  printf 'protected_rename_integration\tFAIL\trename escaped classification\n' >> "$OUT"
  failures=$((failures + 1))
fi
rm -rf "$repo"
rm -f "$diff_file" "$result_file"

printf 'summary\t%s\t18 cases; failures=%s\n' "$([[ "$failures" -eq 0 ]] && echo PASS || echo FAIL)" "$failures" >> "$OUT"
cat "$OUT"
[[ "$failures" -eq 0 ]]
