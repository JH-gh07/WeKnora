-- Migration 000090: persisted evaluation run facts.
--
-- One row per logical evaluation execution, so the run's protocol/provenance,
-- lifecycle, progress, metrics validity, error and temporary-resource locator
-- survive a process restart (see task002). The table intentionally does NOT
-- model ModelCall / Provider Attempt / ItemResult — those belong to later
-- Tasks (R5 metering).
--
-- protocol_snapshot / run_provenance / metrics_json hold secret-free JSON.
-- Prompt fields inside a snapshot are stored as {version/hash/length} only.

CREATE TABLE IF NOT EXISTS evaluation_runs (
    run_id VARCHAR(36) PRIMARY KEY,
    task_id VARCHAR(128) NOT NULL DEFAULT '',
    tenant_id INTEGER NOT NULL,
    protocol_hash VARCHAR(64) NOT NULL DEFAULT '',
    protocol_snapshot JSONB,
    run_provenance JSONB,
    git_commit VARCHAR(64) NOT NULL DEFAULT '',
    app_version VARCHAR(32) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
    interruption_reason VARCHAR(32) NOT NULL DEFAULT '',
    started_at TIMESTAMP WITH TIME ZONE,
    ended_at TIMESTAMP WITH TIME ZONE,
    total_count INTEGER NOT NULL DEFAULT 0,
    processed_count INTEGER NOT NULL DEFAULT 0,
    finished_count INTEGER NOT NULL DEFAULT 0,
    metrics_json JSONB,
    metrics_valid BOOLEAN NOT NULL DEFAULT false,
    error_type VARCHAR(64) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    temporary_resource_key VARCHAR(128) NOT NULL DEFAULT '',
    temporary_kb_id VARCHAR(36) NOT NULL DEFAULT '',
    cleanup_status VARCHAR(16) NOT NULL DEFAULT '',
    measurement_status VARCHAR(16) NOT NULL DEFAULT 'UNKNOWN',
    persistence_status VARCHAR(16) NOT NULL DEFAULT 'PERSISTED',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS idx_evaluation_runs_task_id ON evaluation_runs (task_id);
CREATE INDEX IF NOT EXISTS idx_evaluation_runs_tenant ON evaluation_runs (tenant_id);
CREATE INDEX IF NOT EXISTS idx_evaluation_runs_status ON evaluation_runs (status);
CREATE INDEX IF NOT EXISTS idx_evaluation_runs_protocol_hash ON evaluation_runs (protocol_hash);
CREATE INDEX IF NOT EXISTS idx_evaluation_runs_resource_key ON evaluation_runs (temporary_resource_key);
CREATE INDEX IF NOT EXISTS idx_evaluation_runs_tenant_status ON evaluation_runs (tenant_id, status);
