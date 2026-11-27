package exports

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// RecoverySelection is authorized intent for a new review, never authority to
// replay acceptance or download old bytes. Callers generate a new operation ID.
type RecoverySelection struct {
	Kind    string
	Options json.RawMessage
}

// RecoverySelection exposes filter-preserving recovery for fixed operational and
// financial report adapters. Legacy receipt redaction remains unchanged. Expiry limits
// artifact bytes, not recovery intent, but never bypasses current whole-snapshot
// authority. No part of the selection escapes on any failed check.
func (s *Service) RecoverySelection(ctx context.Context, actor authorization.Actor, id int64) (RecoverySelection, error) {
	var out RecoverySelection
	err := authorization.New(s.Pool).WithinActor(ctx, actor, func(tx pgx.Tx, p authorization.Principal) error {
		var kind, state string
		var options json.RawMessage
		var blocked bool
		var count int64
		err := tx.QueryRow(ctx, `SELECT kind,options,state,blocked,coalesce(row_count,-1) FROM export_jobs WHERE id=$1 AND requester_id=$2 AND company_id=$3`, id, actor.UserID, p.CompanyID).Scan(&kind, &options, &state, &blocked, &count)
		if errors.Is(err, pgx.ErrNoRows) {
			return authorization.ErrNotFound
		}
		if err != nil {
			return err
		}
		if blocked {
			return ErrUnavailable
		}
		d, ok := s.definitions[kind]
		if !ok {
			return ErrUnavailable
		}
		switch d.(type) {
		case Leases, Jobs, Workload, Documents, EstimateCohorts,
			InvoiceAging, CustomerFinancials, SettlementActivity, CurrentCredit, PaymentsCredits:
		default:
			return ErrUnavailable
		}
		if state != "Ready" && state != "Failed" {
			return ErrNotReady
		}
		if err := authorizeDefinition(ctx, tx, p, d, options); err != nil {
			return ErrUnavailable
		}
		if state == "Ready" {
			if count < 0 {
				return ErrUnavailable
			}
			for ordinal := int64(1); ordinal <= count; ordinal++ {
				var ref json.RawMessage
				if err := tx.QueryRow(ctx, `SELECT reference FROM export_records WHERE export_id=$1 AND ordinal=$2`, id, ordinal).Scan(&ref); err != nil {
					return ErrUnavailable
				}
				if err := d.Recheck(ctx, tx, p, ref); err != nil {
					return ErrUnavailable
				}
			}
		}
		out = RecoverySelection{Kind: kind, Options: append(json.RawMessage(nil), options...)}
		return nil
	})
	if err != nil {
		return RecoverySelection{}, err
	}
	return out, nil
}
