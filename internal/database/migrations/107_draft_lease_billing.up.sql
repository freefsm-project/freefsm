-- Prospective Draft terms are not invoices, obligations or coverage.
-- Billing-only authority must not imply base details or collaboration access.
UPDATE authorization_permissions SET prerequisite=NULL WHERE permission='lease.billing.view' AND scope='A';
CREATE TABLE lease_draft_billing (
 lease_id bigint PRIMARY KEY REFERENCES leases(id),
 revision bigint NOT NULL DEFAULT 0,
 initial_due_on date
);
CREATE TABLE lease_draft_asset_terms (
 lease_id bigint NOT NULL REFERENCES lease_draft_billing(lease_id),
 asset_id bigint NOT NULL REFERENCES assets(id),
 initial_cents bigint CHECK (initial_cents>=0),
 rent_cents bigint CHECK (rent_cents>=0),
 tax_rate numeric CHECK (tax_rate>=0 AND tax_rate<=100),
 PRIMARY KEY(lease_id,asset_id)
);
CREATE TRIGGER lease_billing_lock BEFORE INSERT OR UPDATE OR DELETE ON lease_draft_billing FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER lease_terms_lock BEFORE INSERT OR UPDATE OR DELETE ON lease_draft_asset_terms FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
