#!/usr/bin/env bash
# Classify a NUL-delimited `git diff --no-renames --name-status -z` stream.
# Output is a GitHub-output-compatible key=value block. Human-readable rows go
# to stderr so paths containing whitespace/newlines cannot corrupt the contract.

set -euo pipefail

protected=0
retrieval=0
changed=0

while IFS= read -r -d '' status && IFS= read -r -d '' path; do
  changed=$((changed + 1))
  class="unrelated"

  case "$path" in
    cmd/evaluation-regression/*|\
    internal/application/service/evaluation_regression.go|\
    internal/application/service/evaluation_regression_comparator.go|\
    internal/application/service/evaluation_regression_test.go|\
    internal/application/service/metric/*|\
    tests/evaluation/*|\
    .github/workflows/evaluation-regression.yml|\
    scripts/tmpCheck/task007/*)
      protected=1
      class="protected" ;;
  esac

  case "$path" in
    internal/application/service/knowledgebase_search_fusion.go)
      # Modifying the frozen seam is measurable. Removing/renaming it is not.
      if [[ "${status:0:1}" == "D" ]]; then
        retrieval=1
        class="retrieval-contract"
      fi ;;
    internal/application/service/knowledgebase*.go|\
    internal/application/service/knowledge*.go|\
    internal/application/service/chat_pipeline/*|\
    internal/application/service/retriever/*|\
    internal/application/service/vectorstore*.go|\
    internal/application/service/web_search*.go|\
    internal/application/service/session_knowledge_qa.go|\
    internal/types/retrieval*.go|\
    internal/types/retriever.go|\
    internal/types/search.go|\
    internal/types/web_search*.go|\
    internal/config/config.go)
      retrieval=1
      [[ "$class" == "unrelated" ]] && class="retrieval-contract" ;;
  esac

  printf '  status=%q path=%q class=%s\n' "$status" "$path" "$class" >&2
done

printf 'protected=%s\nretrieval=%s\nchanged=%s\n' "$protected" "$retrieval" "$changed"
