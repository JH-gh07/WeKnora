-- Task012: additive pricing snapshot columns (SQLite). All nullable.
ALTER TABLE model_calls ADD COLUMN pricing_status VARCHAR(24);
ALTER TABLE model_calls ADD COLUMN pricing_reason VARCHAR(48);
ALTER TABLE model_calls ADD COLUMN pricing_rule_id VARCHAR(128);
ALTER TABLE model_calls ADD COLUMN pricing_catalog_hash VARCHAR(64);
ALTER TABLE model_calls ADD COLUMN pricing_unit VARCHAR(32);
ALTER TABLE model_calls ADD COLUMN input_unit_price_nanos_per_million INTEGER;
ALTER TABLE model_calls ADD COLUMN output_unit_price_nanos_per_million INTEGER;
ALTER TABLE model_calls ADD COLUMN cache_read_unit_price_nanos_per_million INTEGER;
ALTER TABLE model_calls ADD COLUMN cache_write_unit_price_nanos_per_million INTEGER;
ALTER TABLE model_calls ADD COLUMN estimated_cost_nanos INTEGER;
