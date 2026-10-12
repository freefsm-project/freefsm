-- Fail closed before changing any recorded Item or captured line. Operators
-- resolve the listed identities explicitly and rerun this same migration.
CREATE FUNCTION item_tax_valid(value text) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE rate numeric;
BEGIN
 IF value='' THEN RETURN true; END IF;
 IF btrim(value) !~ '^([0-9]+([.][0-9]*)?|[.][0-9]+)[[:space:]]*%?$' THEN RETURN false; END IF;
 rate:=btrim(replace(value,'%',''))::numeric;
 RETURN rate>=0 AND rate<=100;
EXCEPTION WHEN OTHERS THEN RETURN false;
END $$;
DO $$ DECLARE invalid_ids text; BEGIN
 SELECT string_agg(id::text, ', ' ORDER BY id) INTO invalid_ids FROM items
 WHERE btrim(name)='' OR type NOT IN ('service','product')
 OR unit_price<0 OR unit_cost<0
 OR unit_price::text IN ('NaN','Infinity','-Infinity')
 OR unit_cost::text IN ('NaN','Infinity','-Infinity')
 OR (sku<>'' AND btrim(sku)<>sku) OR NOT item_tax_valid(tax_rate);
 IF invalid_ids IS NOT NULL THEN
  RAISE EXCEPTION 'Item cutover requires explicit resolution of invalid name, type, price, cost, tax or ambiguous SKU; Item IDs: %', invalid_ids;
 END IF;
END $$;
DROP INDEX IF EXISTS idx_items_name;
ALTER TABLE items ADD CONSTRAINT item_name_nonblank CHECK (btrim(name)<>'');
ALTER TABLE items ADD CONSTRAINT item_price_nonnegative CHECK (unit_price>=0 AND unit_price::text NOT IN ('NaN','Infinity','-Infinity'));
ALTER TABLE items ADD CONSTRAINT item_cost_nonnegative CHECK (unit_cost>=0 AND unit_cost::text NOT IN ('NaN','Infinity','-Infinity'));
ALTER TABLE items ADD CONSTRAINT item_sku_unambiguous CHECK (sku=btrim(sku));
ALTER TABLE items ADD CONSTRAINT item_tax_valid CHECK (item_tax_valid(tax_rate));

-- Preserve every claimed nonblank SKU, including after a SKU edit/archive.
CREATE TABLE item_sku_claims (sku text PRIMARY KEY, item_id bigint NOT NULL REFERENCES items(id));
INSERT INTO item_sku_claims SELECT sku,id FROM items WHERE sku<>'';
CREATE FUNCTION retain_item_sku() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner_id bigint;
BEGIN
 IF NEW.sku<>'' THEN
  INSERT INTO item_sku_claims(sku,item_id) VALUES(NEW.sku,NEW.id) ON CONFLICT DO NOTHING;
  SELECT item_id INTO owner_id FROM item_sku_claims WHERE sku=NEW.sku;
  IF owner_id<>NEW.id THEN RAISE EXCEPTION 'SKU already claimed' USING ERRCODE='23505'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER retain_item_sku AFTER INSERT OR UPDATE OF sku ON items FOR EACH ROW EXECUTE FUNCTION retain_item_sku();
