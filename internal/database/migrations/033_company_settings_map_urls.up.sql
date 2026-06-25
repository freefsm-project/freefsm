ALTER TABLE company_settings
    ADD COLUMN map_tile_url TEXT NOT NULL DEFAULT 'https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png',
    ADD COLUMN geocoder_url TEXT NOT NULL DEFAULT '';
