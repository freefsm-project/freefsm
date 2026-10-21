package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrOfflineOperation = errors.New("invalid offline authorization operation ID")
var ErrOfflineDisabled = errors.New("offline authorization disabled")

// OfflineAuthorization is a bounded client disclosure/staging window. It never
// substitutes for the current-authority checks on a domain mutation.
type OfflineAuthorization struct {
	OperationID string     `json:"operation_id"`
	Code        string     `json:"code"`
	AcceptedAt  time.Time  `json:"accepted_at"`
	ID          string     `json:"id,omitempty"`
	UserID      string     `json:"user_id,omitempty"`
	Epoch       string     `json:"epoch,omitempty"`
	IssuedAt    *time.Time `json:"issued_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// ResolveSyncedJob preserves the installed identity behind a nonreused sync ID.
// This lookup grants no access; the field service still checks current scope.
func (s *Service) ResolveSyncedJob(ctx context.Context, id string) (int64, error) {
	var installed int64
	parsed, err := uuid.Parse(id)
	if err != nil {
		return 0, ErrNotFound
	}
	err = s.Pool.QueryRow(ctx, `SELECT installed_id FROM job_sync_identities WHERE resource_type='job' AND opaque_id=$1`, parsed.String()).Scan(&installed)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return installed, err
}

func (s *Service) OfflinePolicy(ctx context.Context, a Actor) (int, error) {
	var hours int
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		return tx.QueryRow(ctx, `SELECT offline_hours FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&hours)
	})
	return hours, err
}

func (s *Service) IssueOfflineAuthorization(ctx context.Context, a Actor, operation string) (OfflineAuthorization, error) {
	var result OfflineAuthorization
	id, err := uuid.Parse(operation)
	if err != nil || id == uuid.Nil {
		return result, ErrOfflineOperation
	}
	result.OperationID = id.String()
	var outcome error
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if a.Channel != "api" || a.SessionHash == "" {
			return ErrForbidden
		}
		if err := admitOperation(ctx, tx, a, result.OperationID, "offline.authorization.issue", struct{}{}); err != nil {
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT receipt FROM offline_authorization_receipts WHERE operation_id=$1 AND user_id=$2 AND client_key=$3`, result.OperationID, p.UserID, operationClientKey(a)).Scan(&raw)
		if err == nil {
			if err = json.Unmarshal(raw, &result); err != nil {
				return err
			}
			if result.Code == "offline_disabled" {
				outcome = ErrOfflineDisabled
			} else if result.Code != "accepted" {
				outcome = ErrForbidden
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var hours int
		if err = tx.QueryRow(ctx, `SELECT offline_hours FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&hours); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&result.AcceptedAt); err != nil {
			return err
		}
		result.Code = "forbidden"
		if hours == 0 {
			result.Code, outcome = "offline_disabled", ErrOfflineDisabled
		} else if !p.Allows("job.view", Assigned) && !p.Allows("job.view", All) && !p.Allows("time.view", Own) {
			outcome = ErrForbidden
		} else {
			result.Code = "accepted"
			result.ID = uuid.NewString()
			result.UserID = strconv.FormatInt(p.UserID, 10)
			if err = tx.QueryRow(ctx, `SELECT epoch::text FROM session_instance WHERE singleton`).Scan(&result.Epoch); err != nil {
				return err
			}
			issued := result.AcceptedAt
			expires := issued.Add(time.Duration(hours) * time.Hour)
			result.IssuedAt = &issued
			result.ExpiresAt = &expires
			if err = audit(ctx, tx, a, p, "offline.authorization.issue", Own, "user", p.UserID, map[string]any{"operation_id": result.OperationID, "lease_id": result.ID, "hours": hours}); err != nil {
				return err
			}
		}
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO offline_authorization_receipts(operation_id,user_id,client_key,receipt) VALUES($1,$2,$3,$4)`, result.OperationID, p.UserID, operationClientKey(a), raw)
		return err
	})
	if err != nil {
		return OfflineAuthorization{}, err
	}
	return result, outcome
}

func (s *Service) OfflineAuthorizationReceipt(ctx context.Context, a Actor, operation string) (OfflineAuthorization, error) {
	var result OfflineAuthorization
	id, err := uuid.Parse(operation)
	if err != nil || id == uuid.Nil {
		return result, ErrNotFound
	}
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT receipt FROM offline_authorization_receipts WHERE operation_id=$1 AND user_id=$2 AND client_key=$3`, id.String(), p.UserID, operationClientKey(a)).Scan(&raw)
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
