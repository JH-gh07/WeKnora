CREATE TABLE IF NOT EXISTS embedding_cache_entries (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    cache_key VARCHAR(64) NOT NULL,
    model_id VARCHAR(128) NOT NULL DEFAULT '',
    provider_identity VARCHAR(256) NOT NULL DEFAULT '',
    model_config_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    cache_schema_version INTEGER NOT NULL DEFAULT 0,
    dimensions INTEGER NOT NULL DEFAULT 0,
    vector_payload TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_embedding_cache_entries_tenant_key ON embedding_cache_entries (tenant_id, cache_key);
CREATE INDEX IF NOT EXISTS idx_embedding_cache_entries_tenant_model ON embedding_cache_entries (tenant_id, model_id);

CREATE TABLE IF NOT EXISTS embedding_cache_observations (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    run_id VARCHAR(36),
    task_id VARCHAR(128),
    trace_id VARCHAR(128) NOT NULL DEFAULT '',
    model_id VARCHAR(128) NOT NULL DEFAULT '',
    operation VARCHAR(32) NOT NULL DEFAULT 'embedding',
    cache_mode VARCHAR(8) NOT NULL DEFAULT 'OFF',
    logical_embedding_item_count BIGINT NOT NULL DEFAULT 0,
    local_embedding_hit_count BIGINT NOT NULL DEFAULT 0,
    local_embedding_miss_count BIGINT NOT NULL DEFAULT 0,
    local_embedding_bypass_count BIGINT NOT NULL DEFAULT 0,
    lookup_failure_count BIGINT NOT NULL DEFAULT 0,
    corruption_count BIGINT NOT NULL DEFAULT 0,
    write_failure_count BIGINT NOT NULL DEFAULT 0,
    provider_bound_model_call_count BIGINT NOT NULL DEFAULT 0,
    persistence_status VARCHAR(16) NOT NULL DEFAULT 'STARTED',
    request_elapsed_ms BIGINT NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finalized_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_embedding_cache_obs_tenant_time ON embedding_cache_observations (tenant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_embedding_cache_obs_tenant_model ON embedding_cache_observations (tenant_id, model_id, created_at);
CREATE INDEX IF NOT EXISTS idx_embedding_cache_obs_tenant_run ON embedding_cache_observations (tenant_id, run_id);
