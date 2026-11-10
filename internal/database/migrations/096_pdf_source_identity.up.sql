-- A baseline, not reconstructed history. Missing clocks mean revision 1.
-- Keep PDF concurrency metadata outside domain rows: installed full-row source
-- projections and whole-row invariants must not acquire PDF bookkeeping fields.
-- Retained snapshots/bytes and existing commercial/planning revisions are untouched.
CREATE TABLE pdf_source_revisions (
 source_type text NOT NULL CHECK (source_type IN ('invoices','estimates','customers','jobs','statuses','assets','job_asset_links','job_asset')),
 source_id bigint NOT NULL,
 revision bigint NOT NULL CHECK (revision >= 2),
 PRIMARY KEY(source_type,source_id)
);
CREATE FUNCTION pdf_review_revision(source text, identity bigint) RETURNS bigint LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT revision FROM pdf_source_revisions WHERE source_type=source AND source_id=identity),1);
$$;
CREATE FUNCTION advance_pdf_review_revision(source text, identity bigint) RETURNS void LANGUAGE sql AS $$
 INSERT INTO pdf_source_revisions(source_type,source_id,revision) VALUES(source,identity,2)
 ON CONFLICT(source_type,source_id) DO UPDATE SET revision=pdf_source_revisions.revision+1;
$$;

CREATE FUNCTION pdf_document_review_values(document jsonb) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT jsonb_build_object(
  'document',(SELECT jsonb_object_agg(key,value) FROM jsonb_each(document)
    WHERE key=ANY(ARRAY['customer_id','job_id','number_label','invoice_number','title','notes','terms','tax_rate','financial','invoice_date','due_date','valid_through','created_at','status_id','deleted_at','conversion_hidden_at'])),
  'lines',(SELECT coalesce(jsonb_agg(CASE WHEN cs.pdf_show_line_item_descriptions THEN line ELSE line-'description' END ORDER BY ordinal),'[]'::jsonb)
    FROM company_settings cs, jsonb_array_elements(document->'line_items') WITH ORDINALITY lines(line,ordinal)
    WHERE cs.company_id=(document->>'company_id')::bigint),
  'fields',(SELECT coalesce(jsonb_agg(value ORDER BY f.sort_order,f.id),'[]'::jsonb)
    FROM jsonb_array_elements(document->'custom_fields') value JOIN custom_field_definitions f ON f.id=(value->>'definition_id')::bigint WHERE f.include_pdf));
$$;
CREATE FUNCTION pdf_document_review_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF pdf_document_review_values(to_jsonb(NEW)) IS DISTINCT FROM pdf_document_review_values(to_jsonb(OLD)) THEN
  PERFORM advance_pdf_review_revision(TG_TABLE_NAME,NEW.id);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER pdf_document_review_revision AFTER UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION pdf_document_review_revision();
CREATE TRIGGER pdf_document_review_revision AFTER UPDATE ON estimates FOR EACH ROW EXECUTE FUNCTION pdf_document_review_revision();

-- Related records have narrow rendering facets: operational notes, assignments,
-- field help/validation and other non-rendered data do not advance these clocks.
CREATE FUNCTION pdf_related_review_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE before_values jsonb; after_values jsonb;
BEGIN
 SELECT jsonb_object_agg(key,value) INTO before_values FROM jsonb_each(to_jsonb(OLD)) WHERE key=ANY(TG_ARGV);
 SELECT jsonb_object_agg(key,value) INTO after_values FROM jsonb_each(to_jsonb(NEW)) WHERE key=ANY(TG_ARGV);
 IF before_values IS DISTINCT FROM after_values THEN PERFORM advance_pdf_review_revision(TG_TABLE_NAME,NEW.id); END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER pdf_related_review_revision AFTER UPDATE ON customers FOR EACH ROW EXECUTE FUNCTION pdf_related_review_revision('display_name','company_name','email','phone');
CREATE TRIGGER pdf_related_review_revision AFTER UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION pdf_related_review_revision('job_type','subtitle','start_time','due_date');
CREATE TRIGGER pdf_related_review_revision AFTER UPDATE ON statuses FOR EACH ROW EXECUTE FUNCTION pdf_related_review_revision('name','color','category_key');
CREATE TRIGGER pdf_related_review_revision AFTER UPDATE ON assets FOR EACH ROW EXECUTE FUNCTION pdf_related_review_revision('manufacturer','model','serial_number','customer_id','deleted_at');
CREATE TRIGGER pdf_related_review_revision AFTER UPDATE ON job_asset_links FOR EACH ROW EXECUTE FUNCTION pdf_related_review_revision('asset_id','job_id','operational','unlinked_at');

CREATE FUNCTION pdf_job_asset_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.asset_id IS DISTINCT FROM OLD.asset_id THEN PERFORM advance_pdf_review_revision('job_asset',NEW.id); END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER pdf_job_asset_revision AFTER UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION pdf_job_asset_revision();

-- Definition inclusion can go false -> true -> false. A clock attached only
-- to currently rendered definitions would forget that transition. Keep this
-- narrow facet on the affected document identity instead, without touching its
-- commercial revision or creating historical events. Missing row means baseline 1.
CREATE TABLE pdf_field_source_revisions (
 document_type text NOT NULL CHECK (document_type IN ('invoice','estimate')),
 document_id bigint NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 PRIMARY KEY(document_type,document_id)
);
CREATE FUNCTION pdf_field_source_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (OLD.include_pdf OR NEW.include_pdf) AND
 ROW(OLD.name,OLD.field_type,OLD.sort_order,OLD.include_pdf) IS DISTINCT FROM
 ROW(NEW.name,NEW.field_type,NEW.sort_order,NEW.include_pdf) THEN
  INSERT INTO pdf_field_source_revisions(document_type,document_id,revision)
   SELECT 'invoice',id,2 FROM invoices WHERE EXISTS(SELECT 1 FROM jsonb_array_elements(custom_fields) value WHERE (value->>'definition_id')::bigint=NEW.id)
   UNION ALL
   SELECT 'estimate',id,2 FROM estimates WHERE EXISTS(SELECT 1 FROM jsonb_array_elements(custom_fields) value WHERE (value->>'definition_id')::bigint=NEW.id)
   ON CONFLICT(document_type,document_id) DO UPDATE SET revision=pdf_field_source_revisions.revision+1;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER pdf_field_source_revision AFTER UPDATE ON custom_field_definitions FOR EACH ROW EXECUTE FUNCTION pdf_field_source_revision();
