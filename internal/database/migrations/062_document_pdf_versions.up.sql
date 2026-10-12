CREATE TABLE document_pdf_versions (
 id bigserial PRIMARY KEY,
 company_id bigint NOT NULL REFERENCES companies(id),
 document_type text NOT NULL CHECK(document_type IN ('invoice','estimate')),
 document_id bigint NOT NULL,
 source_token text NOT NULL,
 source_snapshot jsonb NOT NULL,
 filename text NOT NULL,
 content bytea NOT NULL,
 generated_by bigint NOT NULL REFERENCES users(id),
 generated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX document_pdf_versions_document ON document_pdf_versions(company_id,document_type,document_id,id DESC);
CREATE FUNCTION protect_pdf_version() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'PDF versions are immutable'; END $$;
CREATE TRIGGER protect_pdf_version BEFORE UPDATE OR DELETE OR TRUNCATE ON document_pdf_versions FOR EACH STATEMENT EXECUTE FUNCTION protect_pdf_version();
