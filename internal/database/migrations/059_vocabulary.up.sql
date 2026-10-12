-- Fail before modifying definitions; operators must explicitly review collisions.
DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM tags GROUP BY company_id,vocabulary_name_key(name) HAVING count(*)>1)
 OR EXISTS (SELECT 1 FROM asset_types GROUP BY vocabulary_name_key(name) HAVING count(*)>1)
 OR EXISTS (SELECT 1 FROM asset_statuses GROUP BY vocabulary_name_key(name) HAVING count(*)>1) THEN
 RAISE EXCEPTION 'vocabulary preflight: normalized name collisions require explicit review';
 END IF;
END $$;
ALTER TABLE tags ADD COLUMN retired boolean NOT NULL DEFAULT false;
ALTER TABLE asset_types ADD COLUMN retired boolean NOT NULL DEFAULT false;
ALTER TABLE asset_statuses ADD COLUMN retired boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX tags_normalized_name ON tags(company_id,vocabulary_name_key(name));
CREATE UNIQUE INDEX asset_types_normalized_name ON asset_types(vocabulary_name_key(name));
CREATE UNIQUE INDEX asset_statuses_normalized_name ON asset_statuses(vocabulary_name_key(name));
ALTER TABLE tags ADD CHECK (vocabulary_name_key(name) <> '');
ALTER TABLE asset_types ADD CHECK (vocabulary_name_key(name) <> '');
ALTER TABLE asset_statuses ADD CHECK (vocabulary_name_key(name) <> '');
ALTER TABLE assets DROP CONSTRAINT assets_asset_type_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_asset_type_id_fkey FOREIGN KEY(asset_type_id) REFERENCES asset_types(id) ON DELETE RESTRICT;
ALTER TABLE assets DROP CONSTRAINT assets_asset_status_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_asset_status_id_fkey FOREIGN KEY(asset_status_id) REFERENCES asset_statuses(id) ON DELETE RESTRICT;

-- FOR SHARE conflicts with retirement UPDATE as well as deletion. Unchanged
-- references are deliberately exempt, including required retired Types.
CREATE FUNCTION vocabulary_selection_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE inactive boolean;
BEGIN
 IF TG_TABLE_NAME='tag_links' THEN
  IF TG_OP='UPDATE' AND NEW.tag_id=OLD.tag_id THEN RETURN NEW; END IF;
  SELECT retired INTO inactive FROM tags WHERE id=NEW.tag_id FOR SHARE;
  IF inactive THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='retired tag cannot be selected'; END IF;
 ELSE
  IF TG_OP='INSERT' OR NEW.asset_type_id IS DISTINCT FROM OLD.asset_type_id THEN
   SELECT retired INTO inactive FROM asset_types WHERE id=NEW.asset_type_id FOR SHARE;
   IF inactive THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='retired asset type cannot be selected'; END IF;
  END IF;
  IF TG_OP='INSERT' OR NEW.asset_status_id IS DISTINCT FROM OLD.asset_status_id THEN
   SELECT retired INTO inactive FROM asset_statuses WHERE id=NEW.asset_status_id FOR SHARE;
   IF inactive THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='retired asset status cannot be selected'; END IF;
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER vocabulary_tag_selection BEFORE INSERT OR UPDATE OF tag_id ON tag_links FOR EACH ROW EXECUTE FUNCTION vocabulary_selection_guard();
CREATE TRIGGER vocabulary_asset_selection BEFORE INSERT OR UPDATE OF asset_type_id,asset_status_id ON assets FOR EACH ROW EXECUTE FUNCTION vocabulary_selection_guard();
