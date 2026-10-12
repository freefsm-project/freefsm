-- Explicit retrospective evidence is separate from the immutable original.
CREATE TABLE conversion_field_reviews (
 cycle_id uuid PRIMARY KEY REFERENCES estimate_invoice_conversion_cycles(id),
 evidence jsonb NOT NULL, reason text NOT NULL CHECK(btrim(reason)<>''),
 reviewed_by bigint NOT NULL REFERENCES users(id), reviewed_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER immutable_conversion_field_reviews BEFORE UPDATE OR DELETE ON conversion_field_reviews
FOR EACH ROW EXECUTE FUNCTION guard_job_field_operation();
CREATE FUNCTION conversion_field_snapshot(target uuid) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT c.source_snapshot || coalesce(r.evidence,'{}'::jsonb)
 FROM estimate_invoice_conversion_cycles c LEFT JOIN conversion_field_reviews r ON r.cycle_id=c.id WHERE c.id=target
$$;
