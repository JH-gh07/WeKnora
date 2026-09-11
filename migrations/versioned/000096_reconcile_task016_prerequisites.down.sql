-- This reconciliation is intentionally irreversible. The objects belong to
-- migrations 000085-000089 and may contain data created before 000096 ran;
-- dropping them here would make a routine rollback destructive.
SELECT 1;
