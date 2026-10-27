-- Accepted manual commands remain identifiable after later retries. Existing
-- events without operation identity are left untouched.
CREATE UNIQUE INDEX delivery_manual_retry_operation ON document_delivery_events
 (company_id,actor_id,(evidence->>'manual_retry_key'))
 WHERE event_kind='state' AND evidence ? 'manual_retry_key';
