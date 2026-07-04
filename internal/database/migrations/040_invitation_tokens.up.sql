CREATE TABLE invitation_tokens (
    id BIGSERIAL PRIMARY KEY,
    company_id BIGINT,
    token_hash TEXT NOT NULL UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_invitation_tokens_hash ON invitation_tokens(token_hash);
CREATE INDEX idx_invitation_tokens_user ON invitation_tokens(user_id);
