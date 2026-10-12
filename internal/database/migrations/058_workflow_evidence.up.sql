CREATE TABLE IF NOT EXISTS workflow_migration_evidence (
 snapshot text PRIMARY KEY,
 evidence jsonb NOT NULL,
 mappings jsonb NOT NULL,
 reviewed_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION protect_workflow_migration_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'workflow migration evidence is immutable'; END $$;
CREATE TRIGGER workflow_migration_evidence_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON workflow_migration_evidence FOR EACH STATEMENT EXECUTE FUNCTION protect_workflow_migration_evidence();
