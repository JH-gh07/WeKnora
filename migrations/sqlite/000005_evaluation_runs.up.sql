-- Persisted evaluation run facts (Lite). Mirrors migrations/versioned/000085.
-- Row ids are generated in Go, so there is no server-side default here.

CREATE TABLE IF NOT EXISTS evaluation_runs (
    run_id VARCHAR(36) PRIMARY KEY,
    task_id VARCHAR(128) NOT NULL DEFAULT '',
    tenant_id INTEGER NOT NULL,
    protocol_hash VARCHAR(64) NOT NULL DEFAULT '',
    protocol_snapshot TEXT,
    run_provenance TEXT,
    git_commit VARCHAR(64) NOT NULL DEFAULT '',
    app_version VARCHAR(32) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
    interruption_reason VARCHAR(32) NOT NULL DEFAULT '',
    started_at DATETIME,
    ended_at DATETIME,
    total_count INTEGER NOT NULL DEFAULT 0,
    processed_count INTEGER NOT NULL DEFAULT 0,
    finished_count INTEGER NOT NULL DEFAULT 0,
    metrics_json TEXT,
    metrics_valid BOOLEAN NOT NULL DEFAULT 0,
    error_type VARCHAR(64) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    temporary_resource_key VARCHAR(128) NOT NULL DEFAULT '',
    temporary_kb_id VARCHAR(36) NOT NULL DEFAULT '',
    cleanup_status VARCHAR(16) NOT NULL DEFAULT '',
    measurement_status VARCHAR(16) NOT NULL DEFAULT 'UNKNOWN',
    persistence_status VARCHAR(16) NOT NULL DEFAULT 'PERSISTED',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_evaluation_runs_task_id ON evaluation_runs (task_id);
CREATE INDEX IF NOT EXISTS idx_evaluation_runs_tenant ON evaluation_runs (tenant_id);
CREATE INDEX IF NOT EXISTS idx_evaluation_runs_status ON evaluation_runs (status);
CREATE INDEX IF NOT EXISTS idx_evaluation_runs_protocol_hash ON evaluation_runs (protocol_hash);
CREATE INDEX IF NOT EXISTS idx_evaluation_runs_resource_key ON evaluation_runs (temporary_resource_key);
CREATE INDEX IF NOT EXISTS idx_evaluation_runs_tenant_status ON evaluation_runs (tenant_id, status);
