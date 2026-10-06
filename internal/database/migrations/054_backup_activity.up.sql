ALTER TABLE activity_logs ALTER COLUMN actor_id DROP NOT NULL;
ALTER TABLE activity_logs ADD COLUMN event_key TEXT;
CREATE UNIQUE INDEX activity_logs_event_key_key ON activity_logs (event_key);
