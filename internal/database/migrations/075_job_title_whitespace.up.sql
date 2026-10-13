-- Installations that already applied the original 074 btrim-only guard need
-- the same non-destructive review gate and ongoing invariant. Never rewrite
-- their titles or invent replacement facts to make an upgrade proceed.
CREATE OR REPLACE FUNCTION job_title_is_blank(value text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT octet_length(btrim(coalesce(value,''),U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'))=0;
$$;

DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM jobs WHERE job_title_is_blank(job_type)) THEN
  RAISE EXCEPTION 'Job cutover requires Administrator review: whitespace-only title; original records have not been changed';
 END IF;
END $$;

CREATE FUNCTION job_title_nonblank_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF job_title_is_blank(NEW.job_type) THEN
  RAISE EXCEPTION USING ERRCODE='23514',CONSTRAINT='job_title_nonblank',MESSAGE='Job Title is required';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_title_nonblank_guard BEFORE INSERT OR UPDATE ON jobs
FOR EACH ROW EXECUTE FUNCTION job_title_nonblank_guard();
