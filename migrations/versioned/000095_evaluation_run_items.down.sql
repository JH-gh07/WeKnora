-- Down 000095: drop the additive item/attempt fact tables only. No existing
-- fact is modified or removed.
DROP TABLE IF EXISTS evaluation_item_attempts;
DROP TABLE IF EXISTS evaluation_run_items;
DROP INDEX IF EXISTS idx_evaluation_runs_tenant_run;
