ALTER TABLE statuses ADD COLUMN meaning_revision uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE jobs ADD COLUMN status_revision uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE projects ADD COLUMN status_revision uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE estimates ADD COLUMN status_revision uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE invoices ADD COLUMN status_revision uuid NOT NULL DEFAULT gen_random_uuid();
CREATE FUNCTION revise_status_meaning() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.category_key IS DISTINCT FROM OLD.category_key THEN NEW.meaning_revision=gen_random_uuid(); END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER status_meaning_revision BEFORE UPDATE ON statuses FOR EACH ROW EXECUTE FUNCTION revise_status_meaning();
CREATE FUNCTION revise_record_status() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.status_id IS DISTINCT FROM OLD.status_id THEN NEW.status_revision=gen_random_uuid(); END IF;
 RETURN NEW;
END $$;
-- Sort after the conversion guards: a replacement changes only status_id when
-- the immutable tombstone guard validates its narrowly scoped exception.
CREATE TRIGGER zz_status_revision BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION revise_record_status();
CREATE TRIGGER zz_status_revision BEFORE UPDATE ON projects FOR EACH ROW EXECUTE FUNCTION revise_record_status();
CREATE TRIGGER zz_status_revision BEFORE UPDATE ON estimates FOR EACH ROW EXECUTE FUNCTION revise_record_status();
CREATE TRIGGER zz_status_revision BEFORE UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION revise_record_status();
