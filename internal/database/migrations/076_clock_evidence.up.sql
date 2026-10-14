-- Additive evidence: legacy coordinates remain exactly as recorded, with no
-- fabricated capture timestamp, accuracy, clock-out or operation identity.
CREATE TABLE time_clock_gps (
 entry_id bigint NOT NULL REFERENCES time_entries(id),
 event text NOT NULL CHECK(event IN ('in','out')),
 latitude double precision NOT NULL CHECK(latitude BETWEEN -90 AND 90),
 longitude double precision NOT NULL CHECK(longitude BETWEEN -180 AND 180),
 captured_at timestamptz NOT NULL,
 accuracy double precision CHECK(accuracy >= 0 AND accuracy < 'Infinity'::float8),
 accepted_at timestamptz NOT NULL,
 PRIMARY KEY(entry_id,event), CHECK(captured_at <= accepted_at)
);
CREATE FUNCTION clock_evidence_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'clock evidence is immutable'; END $$;
CREATE TRIGGER clock_gps_immutable BEFORE UPDATE OR DELETE ON time_clock_gps FOR EACH ROW EXECUTE FUNCTION clock_evidence_immutable();

CREATE TABLE time_clock_leases (
 id uuid PRIMARY KEY,
 user_id bigint NOT NULL REFERENCES users(id),
 client_key text NOT NULL,
 issued_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CHECK(expires_at>issued_at)
);
CREATE TABLE time_clock_receipts (
 user_id bigint NOT NULL REFERENCES users(id),
 operation_id uuid NOT NULL,
 client_key text NOT NULL,
 intent_hash text NOT NULL,
 entry_id bigint REFERENCES time_entries(id),
 code text NOT NULL,
 accepted_at timestamptz NOT NULL,
 PRIMARY KEY(user_id,operation_id)
);
CREATE TRIGGER clock_receipt_immutable BEFORE UPDATE OR DELETE ON time_clock_receipts FOR EACH ROW EXECUTE FUNCTION clock_evidence_immutable();

CREATE TRIGGER time_entries_authority_fence BEFORE INSERT OR UPDATE OR DELETE ON time_entries FOR EACH STATEMENT EXECUTE FUNCTION authorization_lock();
CREATE FUNCTION time_entry_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Time Entry history must be retained; use reasoned Void'; END IF;
 IF NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.company_id IS DISTINCT FROM OLD.company_id
 OR NEW.latitude IS DISTINCT FROM OLD.latitude OR NEW.longitude IS DISTINCT FROM OLD.longitude
 OR NEW.is_manual IS DISTINCT FROM OLD.is_manual THEN
  RAISE EXCEPTION 'Time Entry subject, creation source and recorded GPS are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER time_entry_identity_guard BEFORE UPDATE OR DELETE ON time_entries FOR EACH ROW EXECUTE FUNCTION time_entry_identity_guard();
