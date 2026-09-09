-- Migration 000018: evaluation item + attempt fact tables (Task016 Step 6).
-- Run -> Item -> Attempt durable truth model. A terminal item fact is protected
-- by PRIMARY KEY (tenant_id, run_id, item_id): at most one committed terminal
-- result per item (I07). The attempt table records every claim/execution with
-- its owner, lease, fencing token and outcome (I08). Additive and reversible.

CREATE UNIQUE INDEX IF NOT EXISTS idx_evaluation_runs_tenant_run ON evaluation_runs (tenant_id, run_id);

CREATE TABLE IF NOT EXISTS evaluation_run_items (
    tenant_id INTEGER NOT NULL CHECK (tenant_id > 0),
    run_id VARCHAR(36) NOT NULL CHECK (length(trim(run_id)) > 0),
    item_id VARCHAR(36) NOT NULL CHECK (length(trim(item_id)) > 0),
    status VARCHAR(24) NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','RUNNING','SUCCEEDED','FAILED_TERMINAL','RETRY_WAIT','RECLAIMABLE','CANCELLED')),
    owner_id VARCHAR(64) NOT NULL DEFAULT '',
    lease_until DATETIME,
    fencing_token INTEGER NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    terminal_reason VARCHAR(64) NOT NULL DEFAULT '',
    result_artifact_hash VARCHAR(64) NOT NULL DEFAULT '',
    result_json TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, run_id, item_id),
    FOREIGN KEY (tenant_id, run_id) REFERENCES evaluation_runs (tenant_id, run_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_evaluation_run_items_run ON evaluation_run_items (tenant_id, run_id);
CREATE INDEX IF NOT EXISTS idx_evaluation_run_items_claim ON evaluation_run_items (tenant_id, run_id, status, lease_until, item_id);

CREATE TABLE IF NOT EXISTS evaluation_item_attempts (
    tenant_id INTEGER NOT NULL CHECK (tenant_id > 0),
    run_id VARCHAR(36) NOT NULL CHECK (length(trim(run_id)) > 0),
    item_id VARCHAR(36) NOT NULL CHECK (length(trim(item_id)) > 0),
    attempt_no INTEGER NOT NULL CHECK (attempt_no > 0),
    owner_id VARCHAR(64) NOT NULL DEFAULT '',
    fencing_token INTEGER NOT NULL DEFAULT 0 CHECK (fencing_token > 0),
    lease_until DATETIME,
    status VARCHAR(24) NOT NULL DEFAULT 'RUNNING' CHECK (status IN ('RUNNING','SUCCEEDED','FAILED_TERMINAL','RETRY_WAIT','CANCELLED')),
    reason VARCHAR(64) NOT NULL DEFAULT '',
    result_artifact_hash VARCHAR(64) NOT NULL DEFAULT '',
    result_json TEXT,
    started_at DATETIME,
    ended_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, run_id, item_id, attempt_no),
    FOREIGN KEY (tenant_id, run_id, item_id) REFERENCES evaluation_run_items (tenant_id, run_id, item_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_evaluation_item_attempts_run ON evaluation_item_attempts (tenant_id, run_id, item_id);
