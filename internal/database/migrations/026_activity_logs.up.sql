CREATE TABLE activity_logs (
    id BIGSERIAL PRIMARY KEY,
    company_id BIGINT REFERENCES companies(id),
    actor_id BIGINT NOT NULL REFERENCES users(id),
    action TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id BIGINT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_activity_object ON activity_logs(object_type, object_id, created_at DESC);
CREATE INDEX idx_activity_created ON activity_logs(created_at DESC);
