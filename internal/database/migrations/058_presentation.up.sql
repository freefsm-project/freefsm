-- Legacy themes lived only in browser cookies. Absence of a row means unknown,
-- not an invented historical System choice. Import only an explicit cookie.
CREATE TABLE user_presentation (
    user_id bigint PRIMARY KEY REFERENCES users(id),
    theme text NOT NULL CHECK (theme IN ('system','light','dark'))
);
