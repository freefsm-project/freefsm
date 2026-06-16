CREATE TABLE custom_field_definitions (
    id BIGSERIAL PRIMARY KEY,
    object_type TEXT NOT NULL,
    name TEXT NOT NULL,
    field_type TEXT NOT NULL,
    required BOOLEAN NOT NULL DEFAULT false,
    options TEXT NOT NULL DEFAULT '[]',
    sort_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_cfd_object_type ON custom_field_definitions(object_type, sort_order);

ALTER TABLE projects ADD COLUMN custom_fields JSONB NOT NULL DEFAULT '[]';
ALTER TABLE estimates ADD COLUMN custom_fields JSONB NOT NULL DEFAULT '[]';
ALTER TABLE invoices ADD COLUMN custom_fields JSONB NOT NULL DEFAULT '[]';
