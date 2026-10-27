package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Evidence is not a send and must remain recordable while outgoing mail is
// disabled. It is nevertheless a write: backup/restore must drain it, and a
// completed restore must refresh the connection generation before persistence.
func (s *Service) enterEvidence(ctx context.Context) (context.Context, func(), error) {
	release := func() {}
	if s.control != nil {
		var err error
		ctx, release, err = s.control.Enter(ctx)
		if err != nil {
			return ctx, nil, err
		}
	}
	if s.beforeWork != nil {
		if err := s.beforeWork(); err != nil {
			release()
			return ctx, nil, err
		}
	}
	return ctx, release, nil
}

// Provider callbacks may race ahead of the send response. Retain their original
// evidence and dispatch correlation, then project it only when that exact
// attempt's acceptance is committed. Bounce takes precedence over delivery.
func reconcileEarlyProviderEvidence(ctx context.Context, tx pgx.Tx, d Delivery, provider string) error {
	var state, eventID string
	var observed time.Time
	err := tx.QueryRow(ctx, `SELECT provider_state,provider_event_id,created_at FROM document_delivery_events WHERE delivery_id=$1 AND company_id=$2 AND event_kind='provider' AND attempt_token=$3 AND provider_identifier=$4 AND provider_state IS NOT NULL ORDER BY (provider_state='bounced') DESC,id DESC LIMIT 1`, d.ID, d.CompanyID, d.AttemptToken, provider).Scan(&state, &eventID, &observed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	column := "delivered_at"
	if state == "bounced" {
		column = "bounced_at"
	}
	if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE document_deliveries SET state=$1,%s=$2 WHERE id=$3 AND company_id=$4 AND state='accepted'`, column), state, observed, d.ID, d.CompanyID); err != nil {
		return err
	}
	return event(ctx, tx, d.ID, d.CompanyID, nil, "state", "accepted", state, provider, "", map[string]any{"provider_event_id": eventID, "attempt_token": d.AttemptToken})
}
