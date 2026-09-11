-- Task012: additive pricing snapshot columns for one-provider cost closure.
-- All columns are nullable so legacy rows stay NULL (interpreted as
-- legacy-unknown) and are never backfilled with current prices.
-- Legacy releases added these fields through GORM AutoMigrate without
-- advancing schema_migrations beyond v89. IF NOT EXISTS makes the versioned
-- migration converge both legacy and clean databases on the same schema.
ALTER TABLE model_calls ADD COLUMN IF NOT EXISTS pricing_status VARCHAR(24);
ALTER TABLE model_calls ADD COLUMN IF NOT EXISTS pricing_reason VARCHAR(48);
ALTER TABLE model_calls ADD COLUMN IF NOT EXISTS pricing_rule_id VARCHAR(128);
ALTER TABLE model_calls ADD COLUMN IF NOT EXISTS pricing_catalog_hash VARCHAR(64);
ALTER TABLE model_calls ADD COLUMN IF NOT EXISTS pricing_unit VARCHAR(32);
ALTER TABLE model_calls ADD COLUMN IF NOT EXISTS input_unit_price_nanos_per_million BIGINT;
ALTER TABLE model_calls ADD COLUMN IF NOT EXISTS output_unit_price_nanos_per_million BIGINT;
ALTER TABLE model_calls ADD COLUMN IF NOT EXISTS cache_read_unit_price_nanos_per_million BIGINT;
ALTER TABLE model_calls ADD COLUMN IF NOT EXISTS cache_write_unit_price_nanos_per_million BIGINT;
ALTER TABLE model_calls ADD COLUMN IF NOT EXISTS estimated_cost_nanos BIGINT;
