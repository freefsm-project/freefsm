DO $$
DECLARE company_count bigint;
DECLARE deleted_orphan_links bigint;
BEGIN
  LOCK TABLE companies IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE customers IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE projects IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE assets IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE estimates IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE invoices IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE tags IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE tag_links IN ACCESS EXCLUSIVE MODE;
  SELECT count(*) INTO company_count FROM companies;

  -- Configured clones may retain polymorphic links after their supported target
  -- was omitted. These links cannot establish ownership and are irrecoverable.
  DELETE FROM tag_links l
  WHERE l.object_type IN ('customer','project','job','asset','estimate','invoice')
    AND NOT EXISTS(
      SELECT 1 FROM (
        SELECT 'customer' object_type,id FROM customers UNION ALL
        SELECT 'project',id FROM projects UNION ALL
        SELECT 'job',id FROM jobs UNION ALL
        SELECT 'asset',id FROM assets UNION ALL
        SELECT 'estimate',id FROM estimates UNION ALL
        SELECT 'invoice',id FROM invoices
      ) o WHERE o.object_type=l.object_type AND o.id=l.object_id
    );
  GET DIAGNOSTICS deleted_orphan_links = ROW_COUNT;
  RAISE NOTICE 'tag ownership migration removed % orphan supported target link(s)', deleted_orphan_links;

  IF EXISTS(SELECT 1 FROM tag_links WHERE company_id IS NULL) THEN
    RAISE EXCEPTION 'tag ownership preflight: tag link company_id is null';
  END IF;

  IF company_count = 1 THEN
    UPDATE tags SET company_id=(SELECT id FROM companies) WHERE company_id IS NULL;
  ELSE
    IF EXISTS(
      SELECT 1 FROM tags t LEFT JOIN tag_links l ON l.tag_id=t.id
      WHERE t.company_id IS NULL GROUP BY t.id HAVING count(l.id)=0
    ) THEN RAISE EXCEPTION 'tag ownership preflight: unlinked tag has ambiguous ownership'; END IF;
    IF EXISTS(
      SELECT 1 FROM tags t JOIN tag_links l ON l.tag_id=t.id
      WHERE t.company_id IS NULL GROUP BY t.id HAVING count(DISTINCT l.company_id) <> 1
    ) THEN RAISE EXCEPTION 'tag ownership preflight: tag links span companies'; END IF;
    UPDATE tags t SET company_id=x.company_id FROM (
      SELECT l.tag_id,min(l.company_id) company_id FROM tag_links l JOIN tags t0 ON t0.id=l.tag_id
      WHERE t0.company_id IS NULL GROUP BY l.tag_id HAVING count(DISTINCT l.company_id)=1
    ) x WHERE t.id=x.tag_id;
  END IF;

  IF EXISTS(SELECT 1 FROM tags WHERE company_id IS NULL) THEN
    RAISE EXCEPTION 'tag ownership preflight: tag company_id remains ambiguous';
  END IF;
  IF EXISTS(
    SELECT 1 FROM tag_links l LEFT JOIN tags t ON t.id=l.tag_id
    WHERE t.id IS NULL OR l.company_id IS DISTINCT FROM t.company_id
  ) THEN RAISE EXCEPTION 'tag ownership preflight: tag/link company mismatch'; END IF;
  IF EXISTS(
    SELECT 1 FROM tag_links l LEFT JOIN (
      SELECT 'customer' object_type,id,company_id FROM customers UNION ALL
      SELECT 'project',id,company_id FROM projects UNION ALL
      SELECT 'job',id,company_id FROM jobs UNION ALL
      SELECT 'asset',id,company_id FROM assets UNION ALL
      SELECT 'estimate',id,company_id FROM estimates UNION ALL
      SELECT 'invoice',id,company_id FROM invoices
    ) o ON o.object_type=l.object_type AND o.id=l.object_id
    WHERE l.object_type NOT IN ('customer','project','job','asset','estimate','invoice')
       OR o.id IS NULL OR l.company_id IS DISTINCT FROM o.company_id
  ) THEN RAISE EXCEPTION 'tag ownership preflight: target missing, unsupported, or belongs to another company'; END IF;
END $$;

ALTER TABLE tags ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE tags ADD CONSTRAINT tags_company_fk FOREIGN KEY(company_id) REFERENCES companies(id);
ALTER TABLE tag_links ADD CONSTRAINT tag_links_company_fk FOREIGN KEY(company_id) REFERENCES companies(id);
ALTER TABLE tags ADD CONSTRAINT tags_id_company_unique UNIQUE(id,company_id);
ALTER TABLE tag_links DROP CONSTRAINT tag_links_tag_id_fkey;
ALTER TABLE tag_links ADD CONSTRAINT tag_links_tag_company_fk FOREIGN KEY(tag_id,company_id) REFERENCES tags(id,company_id) ON DELETE RESTRICT;

CREATE FUNCTION validate_tag_link_target_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_company bigint;
BEGIN
  CASE NEW.object_type
    WHEN 'customer' THEN SELECT company_id INTO target_company FROM customers WHERE id=NEW.object_id FOR KEY SHARE;
    WHEN 'project' THEN SELECT company_id INTO target_company FROM projects WHERE id=NEW.object_id FOR KEY SHARE;
    WHEN 'job' THEN SELECT company_id INTO target_company FROM jobs WHERE id=NEW.object_id FOR KEY SHARE;
    WHEN 'asset' THEN SELECT company_id INTO target_company FROM assets WHERE id=NEW.object_id FOR KEY SHARE;
    WHEN 'estimate' THEN SELECT company_id INTO target_company FROM estimates WHERE id=NEW.object_id FOR KEY SHARE;
    WHEN 'invoice' THEN SELECT company_id INTO target_company FROM invoices WHERE id=NEW.object_id FOR KEY SHARE;
    ELSE RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='tag target type does not support tags';
  END CASE;
  IF target_company IS NULL THEN
    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='tag target does not exist or has no company ownership';
  END IF;
  IF target_company IS DISTINCT FROM NEW.company_id THEN
    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='tag target must belong to the same company';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER tag_link_target_ownership BEFORE INSERT OR UPDATE OF company_id,object_type,object_id ON tag_links
FOR EACH ROW EXECUTE FUNCTION validate_tag_link_target_ownership();

CREATE FUNCTION guard_tagged_target_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF EXISTS(
    SELECT 1 FROM tag_links
    WHERE object_type=TG_ARGV[0] AND object_id=OLD.id
      AND (TG_OP='DELETE' OR company_id IS DISTINCT FROM NEW.company_id)
  ) THEN
    RAISE EXCEPTION USING ERRCODE='23503', MESSAGE='tagged target cannot be deleted or transferred to another company';
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
CREATE TRIGGER customer_tag_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON customers FOR EACH ROW EXECUTE FUNCTION guard_tagged_target_ownership('customer');
CREATE TRIGGER project_tag_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON projects FOR EACH ROW EXECUTE FUNCTION guard_tagged_target_ownership('project');
CREATE TRIGGER job_tag_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON jobs FOR EACH ROW EXECUTE FUNCTION guard_tagged_target_ownership('job');
CREATE TRIGGER asset_tag_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON assets FOR EACH ROW EXECUTE FUNCTION guard_tagged_target_ownership('asset');
CREATE TRIGGER estimate_tag_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON estimates FOR EACH ROW EXECUTE FUNCTION guard_tagged_target_ownership('estimate');
CREATE TRIGGER invoice_tag_ownership_guard BEFORE DELETE OR UPDATE OF company_id ON invoices FOR EACH ROW EXECUTE FUNCTION guard_tagged_target_ownership('invoice');
