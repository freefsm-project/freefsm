package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ClockReceipt struct {
	OperationID string    `json:"operation_id"`
	EntryID     int64     `json:"entry_id,omitempty"`
	Code        string    `json:"code"`
	AcceptedAt  time.Time `json:"accepted_at"`
}
type ClockLease struct {
	ID        string    `json:"id"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ClockLeaseReceipt is independent of the staging resource and Time Entry
// command receipts. A rejected issuance has no lease representation.
type ClockLeaseReceipt struct {
	OperationID string    `json:"operation_id"`
	Code        string    `json:"code"`
	AcceptedAt  time.Time `json:"accepted_at"`
	*ClockLease
}

// Leases attest a staging window, never future authority. Current session,
// grants and Job eligibility are checked again by SubmitClock at acceptance.
func (s *Service) ClockLease(ctx context.Context, a Actor, operation string) (ClockLeaseReceipt, error) {
	id, err := uuid.Parse(operation)
	if err != nil || id == uuid.Nil {
		return ClockLeaseReceipt{}, &ClockConflict{"invalid_operation_id"}
	}
	l := ClockLeaseReceipt{OperationID: id.String()}
	var outcome error
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if err := admitOperation(ctx, tx, a, l.OperationID, "clock.lease.issue", struct{}{}); err != nil {
			if errors.Is(err, ErrConflict) {
				return &ClockConflict{"operation_intent_changed"}
			}
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT receipt FROM clock_lease_receipts WHERE operation_id=$1 AND user_id=$2 AND client_key=$3`, l.OperationID, p.UserID, operationClientKey(a)).Scan(&raw)
		if err == nil {
			if err := json.Unmarshal(raw, &l); err != nil {
				return err
			}
			outcome = clockOutcome(l.Code)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&l.AcceptedAt); err != nil {
			return err
		}
		l.Code = "accepted"
		if a.Channel != "api" || a.SessionHash == "" || !p.Allows("time.clock_in", Own) && !p.Allows("time.clock_out", Own) {
			l.Code, outcome = "forbidden", ErrForbidden
		} else {
			l.ClockLease = &ClockLease{ID: uuid.NewString()}
			if err := tx.QueryRow(ctx, `INSERT INTO time_clock_leases(id,user_id,client_key,issued_at,expires_at) VALUES($1,$2,$3,$4,$4::timestamptz+interval '24 hours') RETURNING issued_at,expires_at`, l.ID, p.UserID, operationClientKey(a), l.AcceptedAt).Scan(&l.IssuedAt, &l.ExpiresAt); err != nil {
				return err
			}
			if err := audit(ctx, tx, a, p, "time.clock_lease.issue", Own, "user", p.UserID, map[string]any{"operation_id": l.OperationID, "lease_id": l.ID}); err != nil {
				return err
			}
		}
		raw, err = json.Marshal(l)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO clock_lease_receipts(operation_id,user_id,client_key,receipt) VALUES($1,$2,$3,$4)`, l.OperationID, p.UserID, operationClientKey(a), raw)
		return err
	})
	if err != nil {
		return ClockLeaseReceipt{}, err
	}
	return l, outcome
}

func (s *Service) ClockLeaseReceipt(ctx context.Context, a Actor, operation string) (ClockLeaseReceipt, error) {
	var result ClockLeaseReceipt
	id, err := uuid.Parse(operation)
	if err != nil || id == uuid.Nil {
		return result, ErrNotFound
	}
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT receipt FROM clock_lease_receipts WHERE operation_id=$1 AND user_id=$2 AND client_key=$3`, id.String(), p.UserID, operationClientKey(a)).Scan(&raw)
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

func clockCode(err error) string {
	if err == nil {
		return "accepted"
	}
	var conflict *ClockConflict
	if errors.As(err, &conflict) {
		return conflict.Code
	}
	if errors.Is(err, ErrForbidden) {
		return "forbidden"
	}
	if errors.Is(err, ErrNotFound) {
		return "not_found"
	}
	return ""
}
func clockOutcome(code string) error {
	switch code {
	case "accepted":
		return nil
	case "forbidden":
		return ErrForbidden
	case "not_found":
		return ErrNotFound
	default:
		return &ClockConflict{code}
	}
}

// Receipts expose only a stable effect identity and disposition, including
// after losing Job access. Failures of storage or audit remain nonterminal.
func (s *Service) SubmitClock(ctx context.Context, a Actor, out bool, in ClockCommand) (ClockReceipt, error) {
	result := ClockReceipt{OperationID: in.OperationID}
	op, err := uuid.Parse(in.OperationID)
	if err != nil || op == uuid.Nil {
		return result, &ClockConflict{"invalid_operation_id"}
	}
	in.OperationID = op.String()
	result.OperationID = in.OperationID
	// A matching URL is transport routing, not new historical intent. Retain
	// it only for a contradictory target so the rejected command stays bound.
	if in.URLJobID == in.JobID {
		in.URLJobID = 0
	}
	data, err := json.Marshal(struct {
		Out     bool
		Command ClockCommand
	}{out, in})
	if err != nil {
		return result, &ClockConflict{"invalid_gps"}
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	var outcome error
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if err := admitOperation(ctx, tx, a, in.OperationID, "clock", digest); err != nil {
			if errors.Is(err, ErrConflict) {
				return &ClockConflict{"operation_intent_changed"}
			}
			return err
		}
		var saved, client string
		err := tx.QueryRow(ctx, `SELECT intent_hash,client_key,coalesce(entry_id,0),code,accepted_at FROM time_clock_receipts WHERE user_id=$1 AND operation_id=$2`, p.UserID, in.OperationID).Scan(&saved, &client, &result.EntryID, &result.Code, &result.AcceptedAt)
		if err == nil {
			if saved != digest || client != a.Channel+":"+a.SessionHash {
				return &ClockConflict{"operation_intent_changed"}
			}
			outcome = clockOutcome(result.Code)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		effect, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		entry, applyErr := applyClock(ctx, effect, p, a, out, in)
		result.Code = clockCode(applyErr)
		if applyErr != nil {
			_ = effect.Rollback(ctx)
			if result.Code == "" {
				return applyErr
			}
			outcome = applyErr
		} else {
			if err := effect.Commit(ctx); err != nil {
				return err
			}
			result.EntryID = entry.ID
		}
		return tx.QueryRow(ctx, `INSERT INTO time_clock_receipts(user_id,operation_id,client_key,intent_hash,entry_id,code,accepted_at) VALUES($1,$2,$3,$4,nullif($5,0),$6,clock_timestamp()) RETURNING accepted_at`, p.UserID, in.OperationID, a.Channel+":"+a.SessionHash, digest, result.EntryID, result.Code).Scan(&result.AcceptedAt)
	})
	if err != nil {
		return ClockReceipt{}, err
	}
	return result, outcome
}

func (s *Service) ClockReceipt(ctx context.Context, a Actor, id string) (ClockReceipt, error) {
	result := ClockReceipt{OperationID: id}
	if _, err := uuid.Parse(id); err != nil {
		return result, ErrNotFound
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		err := tx.QueryRow(ctx, `SELECT coalesce(entry_id,0),code,accepted_at FROM time_clock_receipts WHERE user_id=$1 AND operation_id=$2 AND client_key=$3`, p.UserID, id, a.Channel+":"+a.SessionHash).Scan(&result.EntryID, &result.Code, &result.AcceptedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return result, err
}

func (s *Service) ActiveClock(ctx context.Context, a Actor) (*ClockEntry, error) {
	var result *ClockEntry
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows("time.view", Own) && !p.Allows("time.clock_out", Own) {
			return ErrForbidden
		}
		e := &ClockEntry{}
		err := tx.QueryRow(ctx, `SELECT id,user_id,clock_in FROM time_entries WHERE company_id=$1 AND user_id=$2 AND clock_out IS NULL AND voided_at IS NULL`, p.CompanyID, p.UserID).Scan(&e.ID, &e.UserID, &e.ClockIn)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			result = e
		}
		return err
	})
	return result, err
}
