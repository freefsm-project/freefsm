-- Empty means the recorded legacy interpretation. No historical document,
-- settlement projection, PDF or provenance snapshot is recalculated/backfilled.
ALTER TABLE estimates ADD COLUMN financial text NOT NULL DEFAULT '';
ALTER TABLE invoices ADD COLUMN financial text NOT NULL DEFAULT '';

-- Existing immutable receipts keep their installed numeric/float request
-- canonicalization. New operations explicitly write version1. This is metadata
-- on the existing ledger; admission UUIDs, intents and receipts are untouched.
ALTER TABLE commercial_operations ADD COLUMN intent_version smallint NOT NULL DEFAULT 0 CHECK(intent_version IN (0,1));
ALTER TABLE commercial_operations ALTER COLUMN intent_version SET DEFAULT 1;

-- The original NUMERIC(5,2) rate silently quantized new percentage inputs.
-- Preserve each recorded decimal spelling exactly while allowing exact rates.
ALTER TABLE estimates ALTER COLUMN tax_rate DROP DEFAULT;
ALTER TABLE estimates ALTER COLUMN tax_rate TYPE text USING tax_rate::text;
ALTER TABLE estimates ALTER COLUMN tax_rate SET DEFAULT '0';
ALTER TABLE invoices ALTER COLUMN tax_rate DROP DEFAULT;
ALTER TABLE invoices ALTER COLUMN tax_rate TYPE text USING tax_rate::text;
ALTER TABLE invoices ALTER COLUMN tax_rate SET DEFAULT '0';

CREATE FUNCTION commercial_decimal_key(value text) RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE negative boolean; whole text; fraction text; result text;
BEGIN
 IF value IS NULL THEN RETURN NULL; END IF;
 value:=btrim(value);
 -- Older finite numeric captures may use scientific notation. Exact fields
 -- use plain decimal strings and do not depend on PostgreSQL numeric scale.
 IF value ~ '[eE]' THEN value:=(value::numeric)::text; END IF;
 IF value !~ '^-?[0-9]+(\.[0-9]+)?$' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Invalid commercial decimal'; END IF;
 negative:=left(value,1)='-'; IF negative THEN value:=substr(value,2); END IF;
 whole:=coalesce(nullif(ltrim(split_part(value,'.',1),'0'),''),'0');
 fraction:=rtrim(split_part(value,'.',2),'0');
 result:=whole || CASE WHEN fraction='' THEN '' ELSE '.'||fraction END;
 IF negative AND result<>'0' THEN result:='-'||result; END IF;
 RETURN result;
END $$;

-- Monetary comparison excludes descriptive line metadata, but preserves order.
-- Numeric normalization permits unchanged older numeric captures to acquire
-- exact decimal transport fields without claiming a monetary edit.
CREATE FUNCTION commercial_monetary_lines(lines jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT coalesce(jsonb_agg(jsonb_build_object(
  'quantity',commercial_decimal_key(coalesce(nullif(x->>'quantity_decimal',''),x->>'quantity')),
  'unit_price',commercial_decimal_key(coalesce(nullif(x->>'unit_price_decimal',''),x->>'unit_price','0')),
  'taxable',coalesce((x->>'taxable')::boolean,false),
  'discount',CASE WHEN x->'discount_adjustment' IS NOT NULL AND x->'discount_adjustment'<>'null'::jsonb THEN jsonb_build_object('kind',x->'discount_adjustment'->>'kind','value',commercial_decimal_key(x->'discount_adjustment'->>'value')) WHEN coalesce(commercial_decimal_key(x->>'discount'),'0')<>'0' THEN jsonb_build_object('kind','fixed','value',commercial_decimal_key(x->>'discount')) ELSE NULL END,
  'surcharge',CASE WHEN x->'surcharge_adjustment' IS NOT NULL AND x->'surcharge_adjustment'<>'null'::jsonb THEN jsonb_build_object('kind',x->'surcharge_adjustment'->>'kind','value',commercial_decimal_key(x->'surcharge_adjustment'->>'value')) WHEN coalesce(commercial_decimal_key(x->>'surcharge'),'0')<>'0' THEN jsonb_build_object('kind','fixed','value',commercial_decimal_key(x->>'surcharge')) ELSE NULL END
 ) ORDER BY position),'[]'::jsonb) FROM jsonb_array_elements(lines) WITH ORDINALITY AS entries(x,position)
$$;

-- Existing whole-row revision, settlement locks and statement fences include
-- this column automatically. A versioned snapshot cannot be silently downgraded.
CREATE FUNCTION commercial_financial_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND OLD.financial<>'' AND NEW.financial='' THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Accepted commercial arithmetic cannot be discarded';
 END IF;
 IF TG_OP='UPDATE' AND OLD.financial<>'' AND NEW.financial=OLD.financial AND
  (commercial_monetary_lines(NEW.line_items) IS DISTINCT FROM commercial_monetary_lines(OLD.line_items)
   OR commercial_decimal_key(regexp_replace(btrim(NEW.tax_rate),'%$','')) IS DISTINCT FROM commercial_decimal_key(regexp_replace(btrim(OLD.tax_rate),'%$',''))) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Monetary edits require a newly accepted commercial calculation';
 END IF;
 IF NEW.financial<>'' THEN
  IF (NEW.financial::jsonb->>'version')::integer IS DISTINCT FROM 1
   OR coalesce((NEW.financial::jsonb->'result'->>'pretax_cents')::bigint,-1) < 0
   OR coalesce((NEW.financial::jsonb->'result'->>'total_cents')::bigint,-1) < 0
   OR (NEW.financial::jsonb->'result'->>'total_cents')::numeric IS DISTINCT FROM
      (NEW.financial::jsonb->'result'->>'pretax_cents')::numeric+(NEW.financial::jsonb->'result'->>'tax_cents')::numeric
   OR jsonb_array_length(NEW.financial::jsonb->'input'->'lines') IS DISTINCT FROM jsonb_array_length(NEW.financial::jsonb->'result'->'lines')
   OR (NEW.financial::jsonb->'result'->>'pretax_cents')::numeric IS DISTINCT FROM
      (SELECT coalesce(sum((x->>'adjusted_cents')::numeric),0) FROM jsonb_array_elements(NEW.financial::jsonb->'result'->'lines') x)
   OR (NEW.financial::jsonb->'result'->>'tax_cents')::numeric IS DISTINCT FROM
      (SELECT coalesce(sum((x->>'tax_cents')::numeric),0) FROM jsonb_array_elements(NEW.financial::jsonb->'result'->'lines') x)
   OR commercial_decimal_key(NEW.financial::jsonb->'input'->>'tax_rate') IS DISTINCT FROM commercial_decimal_key(regexp_replace(btrim(NEW.tax_rate),'%$',''))
   OR commercial_monetary_lines(NEW.line_items) IS DISTINCT FROM commercial_monetary_lines(
      (SELECT coalesce(jsonb_agg(jsonb_build_object('unit_price_decimal',x->>'unit_price','quantity_decimal',x->>'quantity','taxable',x->'taxable','discount_adjustment',x->'discount','surcharge_adjustment',x->'surcharge') ORDER BY position),'[]'::jsonb)
       FROM jsonb_array_elements(NEW.financial::jsonb->'input'->'lines') WITH ORDINALITY AS entries(x,position))) THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Invalid accepted commercial arithmetic';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER commercial_financial_guard BEFORE INSERT OR UPDATE ON estimates FOR EACH ROW EXECUTE FUNCTION commercial_financial_guard();
CREATE TRIGGER commercial_financial_guard BEFORE INSERT OR UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION commercial_financial_guard();
