-- NULL means the legacy document did not record its own terms; do not invent them.
ALTER TABLE invoices ADD COLUMN terms text;
ALTER TABLE estimates ADD COLUMN terms text;
