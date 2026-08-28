# ADR-007 — Embedding Cache computation identity

```yaml
status: ACCEPTED
supersedes: ADR-007 registry entry (PROPOSED)
owner: G3 / Task005
created_at: 2026-08-25
accepted_at: 2026-08-26
depends_on: [ADR-003, ADR-009, ADR-010, ADR-011]
```

## Context

The same input text can produce different vectors across tenant, provider/model,
dimensions, normalization, or preprocessing configuration. A cache keyed only on
`hash(text)` would therefore return wrong vectors when any of those change, and would
leak vectors across tenants. Task004 froze the Availability/UI contract; Task003 froze the
logical `ModelCall` grain. G3 now needs the concrete computation-identity key before it
can build a tenant-scoped persistent embedding cache.

## Decision

A cache key represents the effective **computation identity**, not the text alone:

```text
cache_key = SHA-256(
  cache_schema_version
  + "|" + owner_tenant_id
  + "|" + SHA-256(exact_text_bytes)
  + "|" + provider_identity
  + "|" + model_id
  + "|" + model_config_fingerprint
  + "|" + effective_dimension
  + "|" + query_mode
)
```

- `exact_text_bytes` are the UTF-8 input bytes; no trim/lowercase/Unicode normalization
  that the provider has not itself guaranteed.
- `owner_tenant_id` is the tenant that owns the embedding model. Cross-tenant knowledge
  sharing therefore uses the source (model-owning) tenant, never the requesting tenant.
- `provider_identity = source + provider + normalized base_url`；userinfo/fragment 排除，query 原文不持久化，但 canonical query 的 SHA-256 必须纳入 identity，避免不同 deployment/route 共享向量。
- `model_config_fingerprint` digests only an explicit allowlist of vector-affecting fields
  (`model_name`, `truncate_prompt_tokens`, `dimensions`, `supports_dimension_override`,
  and ExtraConfig `api_version` / `remote_model_name`). Credentials (`api_key`, `app_id`,
  `app_secret`, custom authorization headers) are never included.
- `query_mode` is read from `EmbedQueryContextKey` (today always `document`).

## Consequences

- Tenant isolation is the default: `UNIQUE(tenant_id, cache_key)` and every read/write is
  tenant-scoped; a cache entry cannot be hit or queried across tenants.
- Any identity-component change (model/provider/dimension/config/query-mode/schema version)
  produces a MISS; stale vectors are never returned or reinterpreted.
- Failed, dimension-mismatched, or non-finite results are never written.
- A cache HIT produces an item/batch observation but no provider-bound `ModelCall`; a MISS
  still produces exactly one logical provider-bound `ModelCall` per outer invocation
  (ADR-003, ADR-009).
- TTL/LRU/GC and cross-tenant sharing are not implied. Eviction is explicitly out of scope
  for the first release; manual tenant/model-scoped purge is provided.

## Evidence locator

- Contract + identity audit: shared-workspace-relative `../../../status/evidence/task005/embedding_cache_contract.yaml`,
  `../../../status/evidence/task005/identity_field_audit.md`.
- Implementation and behavioral tests: `internal/models/embedding/cache*.go`,
  `internal/application/repository/embedding_cache.go`, and their dual-DB tests.
- Status moves to `ACCEPTED` when Step 8 dual-DB parity and Step 9 OFF/Cold/Warm experiment
  Evidence close.

## Acceptance evidence (2026-08-26)

- Step 8 dual-DB parity: PostgreSQL (ephemeral paradedb container, full 89-migration
  `migrate up` incl. `000093_embedding_cache`) and SQLite both pass
  `embedding_cache_entries`/`embedding_cache_observations` create/unique/repeat/down,
  tenant isolation, restart warm hit, and the OFF/Cold/Warm experiment with identical
  `vector_batch_digest` values. See `status/evidence/task005/migration_postgres.log`,
  `migration_sqlite.log`, `repository_tests.log`.
- Step 9 OFF/Cold/Warm: warm is 100% item hit with 0 provider-bound ModelCall; OFF/COLD/WARM
  vectors are item-exact equal across 3 rounds on both databases.
- Decision 005-1-B (cache outside metered embedder) and `DISABLED` AvailabilityState
  (Change Review 005-1) are implemented and verified.
