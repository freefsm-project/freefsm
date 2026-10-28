CREATE OR REPLACE FUNCTION validate_tag_link_target_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_company bigint;
BEGIN
 CASE NEW.object_type
  WHEN 'customer' THEN SELECT company_id INTO target_company FROM customers WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'project' THEN SELECT company_id INTO target_company FROM projects WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'job' THEN SELECT company_id INTO target_company FROM jobs WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'asset' THEN SELECT company_id INTO target_company FROM assets WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'estimate' THEN SELECT company_id INTO target_company FROM estimates WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'invoice' THEN SELECT company_id INTO target_company FROM invoices WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'lease' THEN SELECT company_id INTO target_company FROM leases WHERE id=NEW.object_id FOR KEY SHARE;
  ELSE RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='tag target type does not support tags';
 END CASE;
 IF target_company IS NULL OR target_company IS DISTINCT FROM NEW.company_id THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='tag target must exist and belong to the same company';
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION validate_file_target_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_company bigint;
BEGIN
 IF TG_OP='UPDATE' AND (OLD.object_type='lease' OR NEW.object_type='lease') AND
  (OLD.object_type,OLD.object_id) IS DISTINCT FROM (NEW.object_type,NEW.object_id) THEN
  RAISE EXCEPTION 'Lease Files retain their original attachment parent';
 END IF;
 CASE NEW.object_type
  WHEN 'customer' THEN SELECT company_id INTO target_company FROM customers WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'project' THEN SELECT company_id INTO target_company FROM projects WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'job' THEN SELECT company_id INTO target_company FROM jobs WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'asset' THEN SELECT company_id INTO target_company FROM assets WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'estimate' THEN SELECT company_id INTO target_company FROM estimates WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'invoice' THEN SELECT company_id INTO target_company FROM invoices WHERE id=NEW.object_id FOR KEY SHARE;
  WHEN 'lease' THEN SELECT company_id INTO target_company FROM leases WHERE id=NEW.object_id FOR KEY SHARE;
  ELSE RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='file target type does not support files';
 END CASE;
 IF target_company IS NULL OR target_company IS DISTINCT FROM NEW.company_id THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='file target must exist and belong to the same company';
 END IF;
 RETURN NEW;
END $$;
