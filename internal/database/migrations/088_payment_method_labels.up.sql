-- Nullable historical metadata means unknown, never a reconstructed fact.
ALTER TABLE invoice_payments ADD COLUMN method_label text;
ALTER TABLE invoice_payments ADD COLUMN other_description text;
ALTER TABLE invoice_payments ADD COLUMN channel text;

CREATE TABLE payment_method_labels (
 company_id bigint NOT NULL REFERENCES companies(id),
 method text NOT NULL CHECK(method IN ('cash','check','credit_card','transfer','other')),
 label text NOT NULL CHECK(length(btrim(label)) BETWEEN 1 AND 80),
 PRIMARY KEY(company_id,method)
);
CREATE UNIQUE INDEX payment_method_labels_name ON payment_method_labels(company_id,lower(btrim(label)));
-- These are overrides for fixed settlement identities, not a second method registry.
