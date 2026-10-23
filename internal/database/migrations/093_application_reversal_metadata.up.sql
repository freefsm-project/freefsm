-- Nullable additions preserve unknown historical metadata and immutable originals.
ALTER TABLE settlement_reversals ADD COLUMN reference text;
ALTER TABLE settlement_reversals ADD COLUMN notes text;
