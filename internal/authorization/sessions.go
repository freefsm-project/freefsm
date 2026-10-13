package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Logout is intrinsic even during mandatory password replacement. The token is
// proof only for its own session; repeats are no-ops with no duplicate audit.
func (s *Service) Logout(ctx context.Context, a Actor) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	var id, role int64
	err = tx.QueryRow(ctx, `SELECT s.id,coalesce(u.authorization_role_id,0) FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.user_id=$1 AND s.token_hash=$2 AND s.kind=CASE WHEN $3='api' THEN 'mobile' ELSE 'web' END AND s.revoked_at IS NULL FOR UPDATE OF s`, a.UserID, a.SessionHash, a.Channel).Scan(&id, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		return err
	}
	if err = audit(ctx, tx, a, Principal{RoleID: role}, "user.sessions.logout", Own, "session", id, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SessionSummary deliberately excludes bearer material and credential hashes.
type SessionSummary struct {
	ID               int64           `json:"id"`
	Kind             string          `json:"kind"`
	Device           string          `json:"device"`
	IssuedAt         time.Time       `json:"issued_at"`
	LastAuthorizedAt time.Time       `json:"last_authorized_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
	RevokedAt        *time.Time      `json:"revoked_at,omitempty"`
	Current          bool            `json:"current"`
	ReviewRequired   bool            `json:"review_required"`
	Legacy           bool            `json:"legacy_recorded_metadata"`
	LegacyEvidence   json.RawMessage `json:"legacy_evidence,omitempty"`
}

// RenewMobile shares the authority fence with revocation, rechecks account and
// session validity, and never inserts or clears revocation evidence.
func (s *Service) RenewMobile(ctx context.Context, a Actor) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, _ Principal) error {
		_, err := tx.Exec(ctx, `WITH instant AS (SELECT clock_timestamp() AS t) UPDATE sessions SET last_used_at=instant.t,expires_at=instant.t+interval '720 hours' FROM instant WHERE token_hash=$1 AND user_id=$2 AND kind='mobile' AND revoked_at IS NULL AND expires_at>instant.t`, a.SessionHash, a.UserID)
		return err
	})
}

// CheckTransactionIdentity is for domain-owned transactions. Call after taking
// the shared authority fence and again immediately before committing effects.
func CheckTransactionIdentity(ctx context.Context, tx pgx.Tx, a Actor) error {
	_, err := principal(ctx, tx, a, false)
	return err
}

func sessionSubject(ctx context.Context, tx pgx.Tx, p Principal, subject int64) error {
	if subject != p.UserID && !p.Protected {
		return ErrNotFound
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND company_id=$2)`, subject, p.CompanyID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func (s *Service) Sessions(ctx context.Context, a Actor, subject int64) ([]SessionSummary, error) {
	out := []SessionSummary{}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if err := sessionSubject(ctx, tx, p, subject); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,kind,coalesce(device_name,''),created_at,last_used_at,expires_at,revoked_at,token_hash=$2,EXISTS(SELECT 1 FROM session_upgrade_inventory i WHERE i.session_id=s.id AND i.disposition='review-required'),EXISTS(SELECT 1 FROM session_upgrade_inventory i WHERE i.session_id=s.id),(SELECT to_jsonb(i) FROM session_upgrade_inventory i WHERE i.session_id=s.id) FROM sessions s WHERE user_id=$1 ORDER BY created_at DESC,id DESC`, subject, a.SessionHash)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v SessionSummary
			if err := rows.Scan(&v.ID, &v.Kind, &v.Device, &v.IssuedAt, &v.LastAuthorizedAt, &v.ExpiresAt, &v.RevokedAt, &v.Current, &v.ReviewRequired, &v.Legacy, &v.LegacyEvidence); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// RevokeSessions uses the same fence as product actions; zero selects all of the
// subject's sessions. Repeated revocation preserves its original evidence.
func (s *Service) RevokeSessions(ctx context.Context, a Actor, subject, id int64) error {
	return s.transact(ctx, a, false, subject == a.UserID, func(tx pgx.Tx, p Principal) error {
		if err := sessionSubject(ctx, tx, p, subject); err != nil {
			return err
		}
		if id != 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE user_id=$1 AND id=$2)`, subject, id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1 AND ($2::bigint=0 OR id=$2) AND revoked_at IS NULL`, subject, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		scope := Own
		if subject != a.UserID {
			scope = Sealed
		}
		return audit(ctx, tx, a, p, "user.sessions.revoke", scope, "user", subject, map[string]any{"session_id": id, "count": tag.RowsAffected()})
	})
}
