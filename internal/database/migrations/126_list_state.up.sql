-- Intrinsic user presentation state only: no grants, domain data or audit
-- history are changed/backfilled. Empty values retain a Reset CAS tombstone.
CREATE TABLE list_states (
    company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    list_key TEXT NOT NULL CHECK (list_key IN (
        'search','customers','contacts','locations','assets','jobs',
        'projects','estimates','invoices','leases','items'
    )),
    values_query TEXT NOT NULL CHECK (octet_length(values_query) <= 16384),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    PRIMARY KEY (company_id, user_id, list_key)
);
