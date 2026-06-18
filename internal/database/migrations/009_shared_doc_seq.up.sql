CREATE SEQUENCE IF NOT EXISTS shared_doc_seq;

SELECT setval('shared_doc_seq', GREATEST(
    COALESCE((SELECT MAX(id) FROM estimates), 0),
    COALESCE((SELECT MAX(id) FROM invoices), 0),
    1
));

ALTER TABLE estimates ALTER COLUMN id SET DEFAULT nextval('shared_doc_seq');
ALTER TABLE invoices ALTER COLUMN id SET DEFAULT nextval('shared_doc_seq');
