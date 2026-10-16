-- Preserve old immutable receipts verbatim: no historical reason is inferred.
ALTER TABLE job_field_operations ADD COLUMN validation_reason text;
