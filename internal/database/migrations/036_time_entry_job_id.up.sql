ALTER TABLE time_entries ADD COLUMN IF NOT EXISTS job_id BIGINT REFERENCES jobs(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_time_entries_job_id ON time_entries(job_id);
