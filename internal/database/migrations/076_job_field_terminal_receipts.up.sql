-- A rejected target may not exist. Preserve its requested opaque identity in the
-- immutable receipt, without asserting a relationship to a real Job.
ALTER TABLE job_field_operations DROP CONSTRAINT job_field_operations_job_id_fkey;
