-- Pricing remains the recorded editable snapshot. Never reselect Items, reprice,
-- infer a source for free text, discard unknown historical fields, or grant Roles.
-- Ambiguous legacy values stop this transaction pending product Administrator
-- review. Line identifiers are projected on read and stored on explicit save;
-- they are not claims about a historical selection's actor or time.
DO $$
DECLARE job_row record; line jsonb; key text; seen text[];
BEGIN
 FOR job_row IN SELECT id,line_items FROM jobs ORDER BY id LOOP
  BEGIN
   IF jsonb_typeof(job_row.line_items) <> 'array' OR job_row.line_items IS NULL THEN
    RAISE EXCEPTION 'unsupported pricing shape';
   END IF;
   seen := ARRAY[]::text[];
   FOR line IN SELECT value FROM jsonb_array_elements(job_row.line_items) LOOP
    IF jsonb_typeof(line) <> 'object' THEN RAISE EXCEPTION 'unsupported line shape'; END IF;
    FOR key IN SELECT jsonb_object_keys(line) LOOP
     IF key NOT IN ('snapshot_id','item_id','title','description','unit_price','quantity','taxable','tax_rate','discount','surcharge') THEN
      RAISE EXCEPTION 'unreviewed historical field';
     END IF;
    END LOOP;
    IF jsonb_typeof(line->'title') IS DISTINCT FROM 'string' OR job_title_is_blank(line->>'title') THEN RAISE EXCEPTION 'invalid title'; END IF;
    IF jsonb_typeof(line->'quantity') IS DISTINCT FROM 'number' OR (line->>'quantity')::numeric <= 0 THEN RAISE EXCEPTION 'invalid quantity'; END IF;
    FOREACH key IN ARRAY ARRAY['unit_price','discount','surcharge','item_id'] LOOP
     IF line ? key AND line->key <> 'null'::jsonb THEN
      IF jsonb_typeof(line->key) <> 'number' OR (line->>key)::numeric < 0 THEN RAISE EXCEPTION 'invalid numeric value'; END IF;
     END IF;
    END LOOP;
    IF line ? 'item_id' AND line->'item_id' <> 'null'::jsonb AND ((line->>'item_id')::numeric <> trunc((line->>'item_id')::numeric) OR (line->>'item_id')::numeric > 9223372036854775807) THEN RAISE EXCEPTION 'invalid source identity'; END IF;
    IF line ? 'description' AND jsonb_typeof(line->'description') NOT IN ('string','null') THEN RAISE EXCEPTION 'invalid description'; END IF;
    IF line ? 'taxable' AND jsonb_typeof(line->'taxable') NOT IN ('boolean','null') THEN RAISE EXCEPTION 'invalid tax treatment'; END IF;
    IF line ? 'tax_rate' AND line->'tax_rate' <> 'null'::jsonb THEN
     IF jsonb_typeof(line->'tax_rate') <> 'string' THEN RAISE EXCEPTION 'invalid tax rate'; END IF;
     IF NOT item_tax_valid(line->>'tax_rate') THEN RAISE EXCEPTION 'invalid tax rate'; END IF;
    END IF;
    IF line ? 'snapshot_id' AND line->'snapshot_id' <> 'null'::jsonb THEN
     IF jsonb_typeof(line->'snapshot_id') <> 'string' THEN RAISE EXCEPTION 'invalid line identity'; END IF;
     IF line->>'snapshot_id' <> '' THEN
      IF line->>'snapshot_id' = ANY(seen) THEN RAISE EXCEPTION 'duplicate line identity'; END IF;
      seen := array_append(seen,line->>'snapshot_id');
     END IF;
    END IF;
   END LOOP;
  EXCEPTION WHEN OTHERS THEN
   RAISE EXCEPTION 'Job pricing cutover requires Administrator review of Job ID %; original records have not been changed',job_row.id;
  END;
 END LOOP;
END $$;
