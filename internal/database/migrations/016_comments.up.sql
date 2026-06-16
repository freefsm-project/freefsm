CREATE TABLE comments (
    id BIGSERIAL PRIMARY KEY,
    object_type TEXT NOT NULL,
    object_id BIGINT NOT NULL,
    author_id BIGINT NOT NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_comments_object ON comments(object_type, object_id);
