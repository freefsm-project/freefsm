-- Additive, empty history: finalization and installed financial facts stay intact.
CREATE TABLE lease_billing_revisions (
 id bigserial PRIMARY KEY,
 lease_id bigint NOT NULL REFERENCES lease_finalizations(lease_id),
 actor_id bigint NOT NULL REFERENCES users(id),
 operation_key text NOT NULL UNIQUE CHECK(length(operation_key) BETWEEN 16 AND 128),
 intent jsonb NOT NULL,
 effective_on date NOT NULL,
 reason text NOT NULL CHECK(length(btrim(reason))>0),
 before_value jsonb NOT NULL,
 after_value jsonb NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX lease_billing_revision_history ON lease_billing_revisions(lease_id,effective_on,id);
CREATE TRIGGER lease_billing_revision_immutable BEFORE UPDATE OR DELETE ON lease_billing_revisions FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
CREATE TRIGGER lease_billing_revision_lock BEFORE INSERT ON lease_billing_revisions FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
