-- Down 000018: drop the additive item/attempt fact tables only. No existing
-- fact is modified or removed.
DROP TABLE IF EXISTS evaluation_item_attempts;
DROP TABLE IF EXISTS evaluation_run_items;
DROP INDEX IF EXISTS idx_evaluation_runs_tenant_run;
DROP INDEX IF EXISTS idx_model_calls_run_logical_call;
DROP INDEX IF EXISTS idx_model_metering_health_tenant_run;
ALTER TABLE model_metering_health DROP COLUMN logical_call_id;
ALTER TABLE model_metering_health DROP COLUMN item_id;
ALTER TABLE model_metering_health DROP COLUMN run_id;
ALTER TABLE model_calls DROP COLUMN logical_call_id;
ALTER TABLE model_calls DROP COLUMN item_id;
ALTER TABLE evaluation_runs DROP COLUMN unobservable_provider_attempt_count;
ALTER TABLE evaluation_runs DROP COLUMN metering_failed_count;
ALTER TABLE evaluation_runs DROP COLUMN metering_persisted_count;
ALTER TABLE evaluation_runs DROP COLUMN metering_attempted_count;
ALTER TABLE evaluation_runs DROP COLUMN expected_logical_calls;
ALTER TABLE evaluation_runs DROP COLUMN cleanup_fencing_token;
ALTER TABLE evaluation_runs DROP COLUMN cleanup_lease_until;
ALTER TABLE evaluation_runs DROP COLUMN cleanup_owner_id;
