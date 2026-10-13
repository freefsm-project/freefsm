-- Released definition writers could insert NULL company_id after migration 042.
-- NULL is a retained global identity, not an orphan to reassign or duplicate.
CREATE FUNCTION custom_field_applies_to_company(owner bigint, company bigint)
RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT owner IS NULL OR owner=company
$$;

-- Reject ambiguous/incompatible effective pairs before changing any guard. Do
-- not choose a winner, rewrite historical pairing, or infer it from names.
DO $$
DECLARE conflict record;
BEGIN
 SELECT a.id AS first_id,b.id AS second_id INTO conflict
 FROM custom_field_definitions a JOIN custom_field_definitions b
 ON a.id<b.id AND a.conversion_key=b.conversion_key
 AND (a.company_id IS NULL OR b.company_id IS NULL OR a.company_id=b.company_id)
 WHERE a.conversion_key IS NOT NULL AND (
 a.object_type=b.object_type OR a.field_type IS DISTINCT FROM b.field_type OR
 (a.field_type='number' AND to_jsonb(a)->'precision' IS DISTINCT FROM to_jsonb(b)->'precision') OR
 (a.field_type IN ('select','multiselect','checkbox') AND a.options::jsonb IS DISTINCT FROM b.options::jsonb))
 ORDER BY a.id,b.id LIMIT 1;
 IF FOUND THEN
  RAISE EXCEPTION 'retained-global field pairing requires explicit review: definitions % and % have overlapping incompatible or ambiguous conversion keys',conflict.first_id,conflict.second_id;
 END IF;
END $$;

-- Earlier #30 cycles could record an apparently complete empty pairing while
-- omitting globals. Classify that evidence at read time; never rewrite the
-- original snapshot or invent a historic pair from today's configuration.
CREATE OR REPLACE FUNCTION conversion_field_snapshot(target uuid) RETURNS jsonb
LANGUAGE plpgsql STABLE AS $$
DECLARE original jsonb; reviewed jsonb; captured jsonb; converted timestamptz;
BEGIN
 SELECT c.source_snapshot,r.evidence,c.converted_at
 INTO original,reviewed,converted
 FROM estimate_invoice_conversion_cycles c LEFT JOIN conversion_field_reviews r ON r.cycle_id=c.id
 WHERE c.id=target;
 IF NOT FOUND THEN RETURN NULL; END IF;
 IF reviewed IS NOT NULL THEN RETURN original || reviewed; END IF;
 IF original->'field_pairs' IS NULL THEN RETURN original; END IF;
 captured:=original->'field_definitions';
 IF jsonb_typeof(original->'field_pairs') IS DISTINCT FROM 'object'
   OR jsonb_typeof(captured) IS DISTINCT FROM 'array' THEN
  RETURN original-'field_pairs'-'field_definitions';
 END IF;
 IF EXISTS(SELECT 1 FROM custom_field_definitions d
   WHERE d.company_id IS NULL AND d.object_type IN ('estimate','invoice') AND d.created_at<=converted
   AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(captured) f WHERE f->>'id'=d.id::text))
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(coalesce(original->'custom_fields','[]'::jsonb)) v
   WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements(captured) f WHERE f->>'id'=v->>'definition_id'))
 OR EXISTS(SELECT 1 FROM jsonb_each_text(original->'field_pairs') p
   WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements(captured) f WHERE f->>'id'=p.key)
   OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(captured) f WHERE f->>'id'=p.value)) THEN
  RETURN original-'field_pairs'-'field_definitions';
 END IF;
 RETURN original;
END $$;

-- The existing statement-level 190019 fence serializes these overlap checks,
-- including new global rows, with concurrent definition/record writers.
CREATE OR REPLACE FUNCTION validate_custom_field_conversion_pair() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.conversion_key IS NULL THEN RETURN NEW; END IF;
 IF EXISTS(SELECT 1 FROM custom_field_definitions d
   WHERE d.id<>NEW.id AND d.conversion_key=NEW.conversion_key
   AND (NEW.company_id IS NULL OR custom_field_applies_to_company(d.company_id,NEW.company_id))
   AND d.object_type=NEW.object_type) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='conversion key already belongs to a field in the owned-or-retained-global scope';
 END IF;
 IF EXISTS(SELECT 1 FROM custom_field_definitions d
   WHERE d.id<>NEW.id AND d.conversion_key=NEW.conversion_key
   AND (NEW.company_id IS NULL OR custom_field_applies_to_company(d.company_id,NEW.company_id))
   AND d.object_type<>NEW.object_type AND (
     d.field_type IS DISTINCT FROM NEW.field_type OR
     (NEW.field_type='number' AND to_jsonb(d)->'precision' IS DISTINCT FROM to_jsonb(NEW)->'precision') OR
     (NEW.field_type IN ('select','multiselect','checkbox') AND d.options::jsonb IS DISTINCT FROM NEW.options::jsonb))) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='conversion key counterpart must use the same type, precision and ordered options in the owned-or-retained-global scope';
 END IF;
 RETURN NEW;
END $$;
