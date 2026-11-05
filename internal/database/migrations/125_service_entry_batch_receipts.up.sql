-- Add terminal dependency dispositions without modifying installed effects,
-- receipt identities, history, grants or attribution.
ALTER TABLE service_entry_operation_receipts
 DROP CONSTRAINT service_entry_operation_receipts_code_check;
ALTER TABLE service_entry_operation_receipts
 ADD CONSTRAINT service_entry_operation_receipts_code_check
 CHECK (code IN ('accepted','not_found','forbidden','service_entry_conflict',
                 'invalid_dependency','failed_dependency','invalid_occurrence'));
