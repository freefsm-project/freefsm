package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// publicationClientKey snapshots the authenticated origin inside publication's
// authority transaction. Mobile expiry renewal does not change this identity.
// Neither a transfer ID nor an editable device label can identify a client.
func publicationClientKey(ctx context.Context, tx pgx.Tx, actor authorization.Actor) (string, error) {
	switch actor.Channel {
	case "", "web":
		// Browser receipts already reconcile across sign-in for the same user.
		return "web", nil
	case "api":
		var id int64
		err := tx.QueryRow(ctx, `SELECT id FROM sessions WHERE user_id=$1 AND token_hash=$2 AND kind='mobile' AND revoked_at IS NULL AND expires_at>clock_timestamp()`, actor.UserID, actor.SessionHash).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", authorization.ErrForbidden
		}
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("api:%d", id), nil
	default:
		return "", authorization.ErrForbidden
	}
}
