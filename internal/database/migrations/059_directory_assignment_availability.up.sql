-- Preserve every existing assignment. Only new or retargeted relationships
-- require an available person; availability is serialized with disablement.
CREATE TRIGGER directory_assignments_lock
BEFORE INSERT OR UPDATE OR DELETE ON job_assignments
FOR EACH STATEMENT EXECUTE FUNCTION authorization_lock();

CREATE FUNCTION directory_assignment_available() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.user_id=OLD.user_id AND NEW.job_id=OLD.job_id THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM users u JOIN jobs j ON j.company_id=u.company_id
               WHERE u.id=NEW.user_id AND j.id=NEW.job_id AND u.is_active) THEN
   RAISE EXCEPTION 'selected person is unavailable for a new assignment';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER directory_assignment_available BEFORE INSERT OR UPDATE ON job_assignments
FOR EACH ROW EXECUTE FUNCTION directory_assignment_available();
