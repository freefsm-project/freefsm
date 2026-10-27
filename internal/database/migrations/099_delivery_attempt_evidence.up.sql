-- Retained acceptance/dispatch events own exact provider-attempt correlation.
-- Installed event metadata remains unknown, never inferred from current rows.
ALTER TABLE document_delivery_events
 ADD COLUMN attempt_token uuid,
 ADD COLUMN message_id text,
 ADD COLUMN provider_state text CHECK(provider_state IN ('delivered','bounced')),
 ADD COLUMN transport_event_id bigint REFERENCES document_delivery_events(id);
CREATE INDEX delivery_acceptance_correlation ON document_delivery_events(delivery_id,provider_identifier,id)
 WHERE event_kind='state' AND to_state='accepted';
