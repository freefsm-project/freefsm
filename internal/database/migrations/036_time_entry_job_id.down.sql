DROP INDEX IF EXISTS idx_time_entries_job_id;

ALTER TABLE time_entries DROP COLUMN IF EXISTS job_id;
