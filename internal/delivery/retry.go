package delivery

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const RetryWindow = 24 * time.Hour

// The first durable claim anchors the retry window, not process uptime or a
// manual retry cycle. Existing events are authoritative; missing legacy facts
// do not establish a fresh automatic retry allowance. Require a complete claim
// ledger matching the lifetime counter so a new manual claim cannot masquerade
// as the original attempt when older events are unavailable.
func retryDeadline(ctx context.Context, tx pgx.Tx, id int64) (*time.Time, error) {
	var first *time.Time
	err := tx.QueryRow(ctx, `SELECT CASE WHEN count(*)=(SELECT lifetime_attempt_count FROM document_deliveries WHERE id=$1) THEN min(created_at) END FROM document_delivery_events WHERE delivery_id=$1 AND event_kind='state' AND to_state='sending'`, id).Scan(&first)
	if first != nil {
		deadline := first.Add(RetryWindow)
		return &deadline, err
	}
	return nil, err
}

// Expire automatic work before claim, including work recovered from a crashed
// worker. The latest Queued event distinguishes an explicit manual authorization
// from an automatic retry without resetting either recorded attempts or time.
func expireAutomaticRetries(ctx context.Context, tx pgx.Tx, now time.Time) (int, error) {
	rows, err := tx.Query(ctx, `SELECT d.id,d.company_id FROM document_deliveries d
	 WHERE state='queued' AND lifetime_attempt_count>0
	 AND NOT coalesce((SELECT e.evidence->>'manual'='true' FROM document_delivery_events e WHERE e.delivery_id=d.id AND e.event_kind='state' AND e.to_state='queued' ORDER BY e.id DESC LIMIT 1),false)
	 AND (greatest(lifetime_attempt_count,cycle_attempt_count)>=$1 OR
	 (SELECT count(*) FROM document_delivery_events e WHERE e.delivery_id=d.id AND e.event_kind='state' AND e.to_state='sending')<>lifetime_attempt_count OR
	 coalesce((SELECT min(e.created_at) FROM document_delivery_events e WHERE e.delivery_id=d.id AND e.event_kind='state' AND e.to_state='sending')<=$2,true))
	 FOR UPDATE OF d`, MaxAttempts, now.Add(-RetryWindow))
	if err != nil {
		return 0, err
	}
	type expired struct{ id, company int64 }
	var deliveries []expired
	for rows.Next() {
		var d expired
		if err = rows.Scan(&d.id, &d.company); err != nil {
			rows.Close()
			return 0, err
		}
		deliveries = append(deliveries, d)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	for _, d := range deliveries {
		if _, err = tx.Exec(ctx, `UPDATE document_deliveries SET state='failed',failed_at=$2,updated_at=$2,last_error='Automatic retry allowance exhausted or original 24-hour window unavailable/expired' WHERE id=$1`, d.id, now); err != nil {
			return 0, err
		}
		if err = event(ctx, tx, d.id, d.company, nil, "state", "queued", "failed", "", "", map[string]any{"reason": "automatic_retry_limit"}); err != nil {
			return 0, err
		}
	}
	return len(deliveries), nil
}
