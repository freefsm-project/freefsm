CREATE TABLE IF NOT EXISTS dashboard_layouts (
    id BIGSERIAL PRIMARY KEY,
    company_id BIGINT NULL,
    user_id BIGINT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope TEXT NOT NULL DEFAULT 'user',
    name TEXT NOT NULL DEFAULT '',
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, scope)
);

CREATE INDEX IF NOT EXISTS idx_dashboard_layouts_company_scope_default ON dashboard_layouts(company_id, scope, is_default);

CREATE TABLE IF NOT EXISTS dashboard_widgets (
    id BIGSERIAL PRIMARY KEY,
    layout_id BIGINT NOT NULL REFERENCES dashboard_layouts(id) ON DELETE CASCADE,
    widget_type TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL DEFAULT 0,
    hidden BOOLEAN NOT NULL DEFAULT FALSE,
    config TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(layout_id, widget_type)
);

CREATE INDEX IF NOT EXISTS idx_dashboard_widgets_layout_position ON dashboard_widgets(layout_id, position);
