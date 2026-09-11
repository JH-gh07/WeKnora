-- Migration 000096: reconcile schemas from development deployments that had
-- already advanced their migration watermark before the final Task016 branch
-- assigned versions 000085-000089.
--
-- Released v0.7.2 databases stop at 000079 and therefore execute 000080+
-- normally. This idempotent migration protects long-lived development
-- databases whose schema_migrations value skipped one or more of the additive
-- objects below. Every statement is safe both when the earlier migration ran
-- and when it was skipped.

ALTER TABLE messages ADD COLUMN IF NOT EXISTS usage JSONB;

CREATE TABLE IF NOT EXISTS tenant_skills (
    id                    VARCHAR(36)  PRIMARY KEY,
    tenant_id             BIGINT       NOT NULL,
    sandbox_config_id     VARCHAR(36)  NOT NULL,
    name                  VARCHAR(255) NOT NULL,
    version               VARCHAR(64),
    description           TEXT,
    instructions          TEXT,
    bundle_ref            VARCHAR(1024),
    bundle_sha256         VARCHAR(64),
    enabled               BOOLEAN      NOT NULL DEFAULT TRUE,
    installed_snapshot_id VARCHAR(255),
    install_session_id    VARCHAR(36),
    install_message_id    VARCHAR(36),
    envs                  JSONB,
    status                VARCHAR(32)  NOT NULL,
    error                 TEXT,
    installing_since      TIMESTAMPTZ,
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at            TIMESTAMPTZ
);

-- CREATE TABLE IF NOT EXISTS does not reconcile columns on a partially
-- created table, so retain explicit additive guards for 000087/000089.
ALTER TABLE tenant_skills ADD COLUMN IF NOT EXISTS install_session_id VARCHAR(36);
ALTER TABLE tenant_skills ADD COLUMN IF NOT EXISTS install_message_id VARCHAR(36);
ALTER TABLE tenant_skills ADD COLUMN IF NOT EXISTS envs JSONB;

CREATE UNIQUE INDEX IF NOT EXISTS uq_tenant_skills_config_name
    ON tenant_skills (sandbox_config_id, name) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS tenant_skill_snapshots (
    id                  VARCHAR(36)  PRIMARY KEY,
    tenant_id           BIGINT       NOT NULL,
    sandbox_config_id   VARCHAR(36)  NOT NULL,
    skill_id            VARCHAR(36),
    snapshot_id         VARCHAR(255),
    parent_snapshot_id  VARCHAR(255),
    generation          INTEGER      NOT NULL DEFAULT 0,
    planned_name        VARCHAR(255),
    trigger             VARCHAR(16)  NOT NULL,
    state               VARCHAR(16)  NOT NULL,
    superseded_at       TIMESTAMPTZ,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

ALTER TABLE tenant_skill_snapshots ADD COLUMN IF NOT EXISTS planned_name VARCHAR(255);

CREATE INDEX IF NOT EXISTS idx_tenant_skill_snapshots_config
    ON tenant_skill_snapshots (sandbox_config_id);
CREATE INDEX IF NOT EXISTS idx_tenant_skill_snapshots_state
    ON tenant_skill_snapshots (state);

CREATE TABLE IF NOT EXISTS tenant_user_env_vars (
    id                VARCHAR(36)  PRIMARY KEY,
    tenant_id         BIGINT       NOT NULL,
    principal_type    VARCHAR(32)  NOT NULL,
    principal_id      VARCHAR(512) NOT NULL,
    sandbox_config_id VARCHAR(36)  NOT NULL,
    skill_id          VARCHAR(36)  NOT NULL DEFAULT '',
    name              VARCHAR(255) NOT NULL,
    value             TEXT,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_user_env_var
    ON tenant_user_env_vars
       (tenant_id, principal_type, principal_id, sandbox_config_id, skill_id, name);
CREATE INDEX IF NOT EXISTS idx_user_env_var_skill
    ON tenant_user_env_vars (tenant_id, skill_id);
CREATE INDEX IF NOT EXISTS idx_user_env_var_config
    ON tenant_user_env_vars (tenant_id, sandbox_config_id);
