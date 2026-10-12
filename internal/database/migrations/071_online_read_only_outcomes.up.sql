ALTER TABLE online_save_actions ADD COLUMN domain_outcome text
    CHECK (domain_outcome IN ('saved','completed'));
ALTER TABLE online_save_actions DROP CONSTRAINT online_save_actions_disposition_check;
ALTER TABLE online_save_actions ADD CONSTRAINT online_save_actions_disposition_check
    CHECK (disposition IN ('unknown','saved','review','rejected','completed'));
CREATE FUNCTION protect_online_domain_outcome() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.domain_committed_at IS NOT NULL AND NEW.domain_outcome IS DISTINCT FROM OLD.domain_outcome THEN
        RAISE EXCEPTION 'online domain outcome is immutable';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER online_domain_outcome_immutable BEFORE UPDATE ON online_save_actions
FOR EACH ROW EXECUTE FUNCTION protect_online_domain_outcome();
