-- Attempts are retry dispositions, never successful generation markers. Existing
-- approved obligations and their identifiers remain untouched.
CREATE TABLE lease_billing_attempts (
 obligation_id bigint PRIMARY KEY REFERENCES lease_billing_obligations(id),
 attempted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 failure text NOT NULL
);
