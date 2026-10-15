-- Service facts are explicit new records. No Job text or Field Notes are
-- interpreted as historical service dates, responsible users or summaries.
CREATE TABLE asset_service_entries (
 id BIGSERIAL PRIMARY KEY,
 company_id BIGINT NOT NULL REFERENCES companies(id),
 asset_id BIGINT NOT NULL REFERENCES assets(id),
 job_id BIGINT NOT NULL REFERENCES jobs(id),
 link_id BIGINT NOT NULL REFERENCES job_asset_links(id),
 customer_id BIGINT NOT NULL REFERENCES customers(id),
 association_event_id BIGINT NOT NULL,
 actor_id BIGINT NOT NULL REFERENCES users(id),
 accepted_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE asset_service_revisions (
 id UUID PRIMARY KEY,
 entry_id BIGINT NOT NULL REFERENCES asset_service_entries(id),
 service_date DATE NOT NULL,
 summary TEXT NOT NULL CHECK (NOT job_title_is_blank(summary)),
 actor_id BIGINT NOT NULL REFERENCES users(id),
 accepted_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 predecessor UUID UNIQUE REFERENCES asset_service_revisions(id)
);
CREATE UNIQUE INDEX asset_service_initial_revision ON asset_service_revisions(entry_id) WHERE predecessor IS NULL;
CREATE TABLE asset_service_responsible_users (
 revision_id UUID NOT NULL REFERENCES asset_service_revisions(id),
 user_id BIGINT NOT NULL REFERENCES users(id),
 PRIMARY KEY(revision_id,user_id)
);
CREATE FUNCTION asset_service_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Service Entry evidence is immutable'; END $$;
CREATE TRIGGER asset_service_entries_immutable BEFORE UPDATE OR DELETE ON asset_service_entries FOR EACH ROW EXECUTE FUNCTION asset_service_immutable();
CREATE TRIGGER asset_service_revisions_immutable BEFORE UPDATE OR DELETE ON asset_service_revisions FOR EACH ROW EXECUTE FUNCTION asset_service_immutable();
CREATE TRIGGER asset_service_users_immutable BEFORE UPDATE OR DELETE ON asset_service_responsible_users FOR EACH ROW EXECUTE FUNCTION asset_service_immutable();
CREATE FUNCTION asset_service_customer_blocker() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- Same Job lock as Customer reassignment, held before checking provenance.
 PERFORM 1 FROM jobs WHERE id=NEW.job_id FOR UPDATE;
 IF NOT EXISTS(SELECT 1 FROM jobs WHERE id=NEW.job_id AND customer_id=NEW.customer_id AND company_id=NEW.company_id) THEN
  RAISE EXCEPTION 'Service Entry original Customer relationship was lost';
 END IF;
 INSERT INTO job_customer_blockers(job_id,service_entry_id) VALUES(NEW.job_id,NEW.id);
 RETURN NEW;
END $$;
CREATE TRIGGER asset_service_customer_blocker BEFORE INSERT ON asset_service_entries FOR EACH ROW EXECUTE FUNCTION asset_service_customer_blocker();
CREATE INDEX asset_service_asset_history ON asset_service_entries(asset_id,customer_id,id);

-- Domain receipts only. Cross-family operation admission belongs to the shared
-- operation-admission integration, not a Service Entry UUID namespace.
CREATE TABLE service_entry_operation_receipts (
 operation_id UUID PRIMARY KEY,
 user_id BIGINT NOT NULL REFERENCES users(id),
 client_key TEXT NOT NULL,
 intent JSONB NOT NULL,
 entry_id BIGINT REFERENCES asset_service_entries(id),
 code TEXT NOT NULL CHECK(code IN ('accepted','not_found','forbidden','service_entry_conflict')),
 accepted_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER service_entry_receipt_immutable BEFORE UPDATE OR DELETE ON service_entry_operation_receipts FOR EACH ROW EXECUTE FUNCTION asset_service_immutable();
