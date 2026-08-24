CREATE TABLE IF NOT EXISTS model_calls (
    id VARCHAR(36) PRIMARY KEY, tenant_id INTEGER NOT NULL, run_id VARCHAR(36), task_id VARCHAR(128),
    trace_id VARCHAR(128) NOT NULL DEFAULT '', model_id VARCHAR(128) NOT NULL, model_name VARCHAR(256) NOT NULL DEFAULT '',
    provider VARCHAR(64) NOT NULL DEFAULT '', operation VARCHAR(32) NOT NULL, purpose VARCHAR(128) NOT NULL DEFAULT '',
    reported_model_revision VARCHAR(128), deployment_id VARCHAR(128), input_tokens INTEGER, output_tokens INTEGER,
    cache_read_tokens INTEGER, cache_write_tokens INTEGER, cache_miss_tokens INTEGER,
    usage_finality VARCHAR(24) NOT NULL DEFAULT 'unavailable', cache_status VARCHAR(24) NOT NULL DEFAULT 'unreported',
    provider_latency_ms INTEGER, request_elapsed_ms INTEGER NOT NULL DEFAULT 0, success BOOLEAN NOT NULL DEFAULT 0,
    error_type VARCHAR(128) NOT NULL DEFAULT '', attempt_count INTEGER, attempt_observability VARCHAR(24) NOT NULL DEFAULT 'unobservable',
    estimated_cost NUMERIC, currency VARCHAR(8) NOT NULL DEFAULT '', pricing_version VARCHAR(64) NOT NULL DEFAULT '',
    pricing_source VARCHAR(128) NOT NULL DEFAULT '', pricing_effective_at DATETIME, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_model_calls_tenant_created ON model_calls (tenant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_model_calls_tenant_run ON model_calls (tenant_id, run_id);
CREATE INDEX IF NOT EXISTS idx_model_calls_tenant_model ON model_calls (tenant_id, model_id, created_at);
CREATE TABLE IF NOT EXISTS model_metering_health (
    id VARCHAR(36) PRIMARY KEY, tenant_id INTEGER NOT NULL, attempted_at DATETIME NOT NULL, persisted BOOLEAN NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_model_metering_health_tenant_time ON model_metering_health (tenant_id, attempted_at);
