-- A receipt belongs to its Customer; an Invoice is an optional initial target.
-- Keep the installed table name, identities, ownership FKs and immutable guards.
-- This does not replay legacy cutover, invent receipts, or regenerate Credit.
ALTER TABLE invoice_payments ALTER COLUMN invoice_id DROP NOT NULL;
