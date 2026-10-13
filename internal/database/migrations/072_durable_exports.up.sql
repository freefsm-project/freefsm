-- Durable export intent and transactional artifacts; no existing facts or grants change.
CREATE TABLE export_jobs (
 id bigserial PRIMARY KEY,
 requester_id bigint NOT NULL REFERENCES users(id),
 company_id bigint NOT NULL REFERENCES companies(id),
 operation_id uuid NOT NULL,
 kind text NOT NULL,
 options jsonb NOT NULL,
 state text NOT NULL DEFAULT 'Pending' CHECK(state IN ('Pending','Running','Ready','Failed')),
 accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 as_of timestamptz,
 ready_at timestamptz,
 row_count bigint,
 digest bytea,
 failure text NOT NULL DEFAULT '',
 blocked boolean NOT NULL DEFAULT false,
 UNIQUE(requester_id,operation_id),
 CHECK ((state='Ready') = (ready_at IS NOT NULL)),
 CHECK (state<>'Ready' OR (as_of IS NOT NULL AND row_count IS NOT NULL AND digest IS NOT NULL))
);
CREATE INDEX export_work ON export_jobs(id) WHERE state IN ('Pending','Running');
CREATE TABLE export_records (
 export_id bigint NOT NULL REFERENCES export_jobs(id),
 ordinal bigint NOT NULL,
 reference jsonb NOT NULL,
 csv bytea NOT NULL,
 PRIMARY KEY(export_id,ordinal)
);
