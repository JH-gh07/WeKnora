#!/usr/bin/env bash
# Generate a reproducible identity for the exact Task005 implementation under
# test, including untracked source files. It intentionally excludes runtime
# evidence and build artifacts so repeated verifier runs do not change it.
set -euo pipefail
export LC_ALL=C
export LANG=C

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
OUT="${1:-$REPO_ROOT/../status/evidence/task005/final_implementation_manifest.tsv}"
TMP="${OUT}.tmp"

cd "$REPO_ROOT"
mkdir -p "$(dirname "$OUT")"

FILES=(
  docs/adr/ADR-007-embedding-cache-identity.md
  frontend/src/views/settings/ModelSettings.vue
  frontend/src/views/settings/components/ModelUsagePanel.vue
  frontend/src/views/settings/modelUsageState.ts
  frontend/src/views/settings/modelUsageState.test.ts
  internal/application/repository/embedding_cache.go
  internal/application/repository/embedding_cache_test.go
  internal/application/service/model.go
  internal/application/service/tenant.go
  internal/application/service/tenant_delete_purge_test.go
  internal/config/config.go
  internal/config/embedding_cache_env_test.go
  internal/container/container.go
  internal/database/migration_embedding_cache_test.go
  internal/models/embedding/cache.go
  internal/models/embedding/cache_test.go
  internal/types/embedding_cache.go
  internal/types/interfaces/embedding_cache.go
  migrations/sqlite/000008_embedding_cache.down.sql
  migrations/sqlite/000008_embedding_cache.up.sql
  migrations/versioned/000088_embedding_cache.down.sql
  migrations/versioned/000088_embedding_cache.up.sql
)

while IFS= read -r path; do
  FILES+=("$path")
done < <(find scripts/tmpCheck/task005 -type f ! -name '*.log' | LC_ALL=C sort)

{
  printf 'kind\tpath_or_key\tvalue\n'
  printf 'meta\tgit_commit\t%s\n' "$(git rev-parse HEAD)"
  printf 'meta\tgo_version\t%s\n' "$(go version)"
  printf 'meta\tnode_version\t%s\n' "$(node --version 2>/dev/null || printf unavailable)"
  printf 'meta\tnpm_version\t%s\n' "$(npm --version 2>/dev/null || printf unavailable)"
  printf 'meta\tos\t%s\n' "$(uname -srvmp)"
  for path in "${FILES[@]}"; do
    if [ ! -f "$path" ]; then
      printf 'missing\t%s\t-\n' "$path"
      exit 1
    fi
    printf 'file\t%s\t%s\n' "$path" "$(shasum -a 256 "$path" | awk '{print $1}')"
  done
} > "$TMP"

printf 'meta\tmanifest_content_sha256\t%s\n' "$(shasum -a 256 "$TMP" | awk '{print $1}')" >> "$TMP"
mv "$TMP" "$OUT"
printf '%s\n' "$OUT"
