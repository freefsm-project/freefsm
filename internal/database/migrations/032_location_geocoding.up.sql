ALTER TABLE locations
    ADD COLUMN latitude DOUBLE PRECISION,
    ADD COLUMN longitude DOUBLE PRECISION,
    ADD COLUMN geocoded_at TIMESTAMPTZ,
    ADD COLUMN geocode_source TEXT;
