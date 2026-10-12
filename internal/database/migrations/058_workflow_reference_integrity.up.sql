-- A concurrent insert must reject instead of losing its selected identity when
-- deletion wins. The replacement command rewrites live references explicitly.
ALTER TABLE jobs DROP CONSTRAINT jobs_status_id_fkey;
ALTER TABLE jobs ADD CONSTRAINT jobs_status_id_fkey FOREIGN KEY(status_id) REFERENCES statuses(id) ON DELETE RESTRICT;
ALTER TABLE projects DROP CONSTRAINT projects_status_id_fkey;
ALTER TABLE projects ADD CONSTRAINT projects_status_id_fkey FOREIGN KEY(status_id) REFERENCES statuses(id) ON DELETE RESTRICT;
ALTER TABLE estimates DROP CONSTRAINT estimates_status_id_fkey;
ALTER TABLE estimates ADD CONSTRAINT estimates_status_id_fkey FOREIGN KEY(status_id) REFERENCES statuses(id) ON DELETE RESTRICT;
ALTER TABLE invoices DROP CONSTRAINT invoices_status_id_fkey;
ALTER TABLE invoices ADD CONSTRAINT invoices_status_id_fkey FOREIGN KEY(status_id) REFERENCES statuses(id) ON DELETE RESTRICT;

-- This is frozen delivery intent, not a live selection. Preserve the original
-- identity after replacement; handoff must not retarget that old intent.
ALTER TABLE document_deliveries DROP CONSTRAINT document_deliveries_expected_status_id_company_id_fkey;
