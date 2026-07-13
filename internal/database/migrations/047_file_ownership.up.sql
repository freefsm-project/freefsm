DO $$
BEGIN
  LOCK TABLE companies IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE customers IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE projects IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE assets IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE estimates IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE invoices IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE files IN ACCESS EXCLUSIVE MODE;

  IF EXISTS(
    SELECT 1 FROM files f LEFT JOIN companies c ON c.id=f.company_id
    WHERE c.id IS NULL
  ) THEN
    RAISE EXCEPTION 'file ownership preflight: file company is missing';
  END IF;

  IF EXISTS(
    SELECT 1 FROM files f LEFT JOIN users u ON u.id=f.uploaded_by
    WHERE u.id IS NULL OR u.company_id IS DISTINCT FROM f.company_id
  ) THEN
    RAISE EXCEPTION 'file ownership preflight: uploader is missing or belongs to another company';
  END IF;

  IF EXISTS(
    SELECT 1 FROM files f LEFT JOIN (
      SELECT 'customer' object_type,id,company_id FROM customers UNION ALL
      SELECT 'project',id,company_id FROM projects UNION ALL
      SELECT 'job',id,company_id FROM jobs UNION ALL
      SELECT 'asset',id,company_id FROM assets UNION ALL
      SELECT 'estimate',id,company_id FROM estimates UNION ALL
      SELECT 'invoice',id,company_id FROM invoices
    ) o ON o.object_type=f.object_type AND o.id=f.object_id
    WHERE f.object_type NOT IN ('customer','project','job','asset','estimate','invoice')
       OR o.id IS NULL
       OR o.company_id IS DISTINCT FROM f.company_id
  ) THEN
    RAISE EXCEPTION 'file ownership preflight: target is missing, unsupported, or belongs to another company';
  END IF;
END $$;

ALTER TABLE files ADD CONSTRAINT files_company_fk
  FOREIGN KEY(company_id) REFERENCES companies(id);
ALTER TABLE files ADD CONSTRAINT files_uploader_company_fk
  FOREIGN KEY(uploaded_by,company_id) REFERENCES users(id,company_id);

CREATE FUNCTION validate_file_target_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_company bigint;
BEGIN
  CASE NEW.object_type
    WHEN 'customer' THEN SELECT company_id INTO target_company FROM customers WHERE id=NEW.object_id FOR KEY SHARE;
    WHEN 'project' THEN SELECT company_id INTO target_company FROM projects WHERE id=NEW.object_id FOR KEY SHARE;
    WHEN 'job' THEN SELECT company_id INTO target_company FROM jobs WHERE id=NEW.object_id FOR KEY SHARE;
    WHEN 'asset' THEN SELECT company_id INTO target_company FROM assets WHERE id=NEW.object_id FOR KEY SHARE;
    WHEN 'estimate' THEN SELECT company_id INTO target_company FROM estimates WHERE id=NEW.object_id FOR KEY SHARE;
    WHEN 'invoice' THEN SELECT company_id INTO target_company FROM invoices WHERE id=NEW.object_id FOR KEY SHARE;
    ELSE RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='file target type does not support files';
  END CASE;
  IF target_company IS NULL THEN
    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='file target does not exist or has no company ownership';
  END IF;
  IF target_company IS DISTINCT FROM NEW.company_id THEN
    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='file target must belong to the same company';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER file_target_ownership
  BEFORE INSERT OR UPDATE OF company_id,object_type,object_id ON files
  FOR EACH ROW EXECUTE FUNCTION validate_file_target_ownership();

CREATE FUNCTION guard_file_target_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF EXISTS(
    SELECT 1 FROM files
    WHERE object_type=TG_ARGV[0] AND object_id=OLD.id
      AND (TG_OP='DELETE' OR company_id IS DISTINCT FROM NEW.company_id)
  ) THEN
    RAISE EXCEPTION USING ERRCODE='23503', MESSAGE='file target cannot be deleted or transferred to another company';
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
CREATE TRIGGER customer_file_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON customers FOR EACH ROW EXECUTE FUNCTION guard_file_target_ownership('customer');
CREATE TRIGGER project_file_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON projects FOR EACH ROW EXECUTE FUNCTION guard_file_target_ownership('project');
CREATE TRIGGER job_file_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON jobs FOR EACH ROW EXECUTE FUNCTION guard_file_target_ownership('job');
CREATE TRIGGER asset_file_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON assets FOR EACH ROW EXECUTE FUNCTION guard_file_target_ownership('asset');
CREATE TRIGGER estimate_file_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON estimates FOR EACH ROW EXECUTE FUNCTION guard_file_target_ownership('estimate');
CREATE TRIGGER invoice_file_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON invoices FOR EACH ROW EXECUTE FUNCTION guard_file_target_ownership('invoice');
