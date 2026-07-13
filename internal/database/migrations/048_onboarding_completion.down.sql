-- Invitation token ownership backfilled by the up migration is intentionally retained.
ALTER TABLE users DROP COLUMN IF EXISTS onboarding_completed_at;
