-- Already-applied042 is never replayed and discarded historical facts cannot be
-- reconstructed. New columns stay unknown on installed records; no row rewrite.
ALTER TABLE invoice_payments ADD COLUMN IF NOT EXISTS legacy_receipt_id TEXT;
ALTER TABLE invoice_payments ALTER COLUMN actor_id DROP NOT NULL;
ALTER TABLE invoice_payments ALTER COLUMN created_at DROP NOT NULL;
CREATE TABLE IF NOT EXISTS settlement_legacy_evidence (
 payment_id UUID PRIMARY KEY REFERENCES invoice_payments(id),
 source JSONB NOT NULL,
 review_issues JSONB NOT NULL,
 captured_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='settlement_legacy_evidence'::regclass AND tgname='settlement_legacy_evidence_immutable') THEN
  CREATE TRIGGER settlement_legacy_evidence_immutable BEFORE UPDATE OR DELETE ON settlement_legacy_evidence FOR EACH ROW EXECUTE FUNCTION reject_settlement_mutation();
 END IF;
END $$;
