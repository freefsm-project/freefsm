-- Historical actors cannot safely be assigned to current users. Refuse a
-- downgrade while these facts exist rather than deleting or misattributing them.
ALTER TABLE activity_logs ALTER COLUMN actor_id SET NOT NULL;
DROP INDEX activity_logs_event_key_key;
ALTER TABLE activity_logs DROP COLUMN event_key;
