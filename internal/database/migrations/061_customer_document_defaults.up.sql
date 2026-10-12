CREATE TABLE customer_document_defaults (
 customer_id bigint PRIMARY KEY REFERENCES customers(id),
 revision bigint NOT NULL DEFAULT 1,
 defaults jsonb NOT NULL DEFAULT '{}'
);
