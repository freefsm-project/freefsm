package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SessionRevocationReceipt never contains bearer material, device labels or
// another user's session identity. SessionID is present only for an own target.
type SessionRevocationReceipt struct {
	OperationID string    `json:"operation_id"`
	Kind        string    `json:"kind"`
	SessionID   int64     `json:"session_id,omitempty"`
	Code        string    `json:"code"`
	AcceptedAt  time.Time `json:"accepted_at"`
}

var ErrInvalidSessionOperation = errors.New("invalid session operation identity")

// SubmitSessionRevocation binds intrinsic own-session security actions to the
// same registry as field mutations. Self revocation consumes authority; replay
// never revives it and a fresh client can only inspect the safe receipt.
func (s *Service) SubmitSessionRevocation(ctx context.Context, a Actor, operation, kind string, target int64) (SessionRevocationReceipt, error) {
	var result SessionRevocationReceipt
	id, err := uuid.Parse(operation)
	if err != nil || id == uuid.Nil {
		return result, ErrInvalidSessionOperation
	}
	if kind != "logout" && kind != "revoke" && kind != "revoke_all" || target < 0 || kind == "revoke" && target == 0 || kind != "revoke" && target != 0 {
		return result, ErrConflict
	}
	result.OperationID, result.Kind = id.String(), kind
	var outcome error
	err = s.transact(ctx, a, false, true, func(tx pgx.Tx, p Principal) error {
		if a.Channel != "api" || a.SessionHash == "" {
			return ErrForbidden
		}
		intent := struct {
			Kind   string
			Target int64
		}{kind, target}
		if err := admitOperation(ctx, tx, a, result.OperationID, "session.revocation", intent); err != nil {
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT receipt FROM session_revocation_receipts WHERE operation_id=$1 AND user_id=$2 AND client_key=$3`, result.OperationID, p.UserID, operationClientKey(a)).Scan(&raw)
		if err == nil {
			if err := json.Unmarshal(raw, &result); err != nil {
				return err
			}
			if result.Code == "not_found" {
				outcome = ErrNotFound
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if kind == "logout" {
			if err := tx.QueryRow(ctx, `SELECT id FROM sessions WHERE user_id=$1 AND token_hash=$2 AND kind='mobile'`, p.UserID, a.SessionHash).Scan(&target); err != nil {
				return err
			}
		}
		result.Code = "accepted"
		if target != 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=$1 AND user_id=$2)`, target, p.UserID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				result.Code, outcome = "not_found", ErrNotFound
			} else {
				result.SessionID = target
			}
		}
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&result.AcceptedAt); err != nil {
			return err
		}
		if outcome == nil {
			tag, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=$3 WHERE user_id=$1 AND ($2::bigint=0 OR id=$2) AND revoked_at IS NULL`, p.UserID, target, result.AcceptedAt)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				action, typ, effect := "user.sessions.revoke", "user", p.UserID
				if kind == "logout" {
					action, typ, effect = "user.sessions.logout", "session", target
				}
				if err := audit(ctx, tx, a, p, action, Own, typ, effect, map[string]any{"operation_id": result.OperationID, "session_id": target, "count": tag.RowsAffected()}); err != nil {
					return err
				}
			}
		}
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO session_revocation_receipts(operation_id,user_id,client_key,receipt) VALUES($1,$2,$3,$4)`, result.OperationID, p.UserID, operationClientKey(a), raw)
		return err
	})
	if err != nil {
		return SessionRevocationReceipt{}, err
	}
	return result, outcome
}

// An authenticated fresh session of the original user may reconcile a logout
// or revoke-all receipt. This is read-only recovery, not client rebinding or
// permission for the revoked bearer to authenticate (ACC-02/AUTH-03).
func (s *Service) SessionRevocationReceipt(ctx context.Context, a Actor, operation string) (SessionRevocationReceipt, error) {
	var result SessionRevocationReceipt
	id, err := uuid.Parse(operation)
	if err != nil || id == uuid.Nil {
		return result, ErrNotFound
	}
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT receipt FROM session_revocation_receipts WHERE operation_id=$1 AND user_id=$2`, id.String(), p.UserID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &result)
	})
	return result, err
}
