-- Legacy releases added this field through GORM AutoMigrate without advancing
-- schema_migrations beyond v89. Keep the forward migration safe for those
-- databases as well as for clean installs.
ALTER TABLE model_calls ADD COLUMN IF NOT EXISTS cache_reported_input_tokens INTEGER;
