package authorization

import (
	"context"

	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/jackc/pgx/v5"
)

const EmailEnablePath = "/settings/enable-email"

func emailAuthority(a Actor, p Principal) error {
	if a.SessionHash == "" || !p.Protected || !p.Allows("email.enable", Sealed) {
		return ErrForbidden
	}
	return nil
}

// EnableEmail owns policy, immutable audit/effect and receipt in one durable
// transaction. TransactionFailure preserves COMMIT ambiguity for reconciliation.
func (s *Service) EnableEmail(ctx context.Context, actor Actor) error {
	a, err := saveaction.RequireExecution(ctx, actor.UserID, EmailEnablePath)
	if err != nil {
		return err
	}
	return s.transactOutcome(ctx, actor, false, false, func(tx pgx.Tx, p Principal) error {
		if err := emailAuthority(actor, p); err != nil {
			return err
		}
		if err := emailpolicy.Durable(ctx, tx); err != nil {
			return err
		}
		if err := saveaction.MatchExecution(ctx, tx, a); err != nil {
			return err
		}
		policy, err := emailpolicy.Read(ctx, tx, true)
		if err != nil {
			return err
		}
		if policy.Review() != a.Revision {
			return ErrConflict
		}
		if policy.Enabled {
			return saveaction.ConfirmNoChange(ctx, tx)
		}
		tag, err := tx.Exec(ctx, `UPDATE restore_email_policy SET enabled=true,revision=revision+1,last_enable_operation=$1 WHERE singleton AND epoch=$2 AND revision=$3 AND NOT enabled`, a.ID, policy.Epoch, policy.Revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return audit(ctx, tx, actor, p, "email.enable", Sealed, "restore_email_policy", 1, map[string]any{"phase": "committed", "epoch": policy.Epoch, "operation_id": a.ID, "reviewed_revision": policy.Revision})
	})
}

// ReconcileEmailEnable serializes against both running and paused admitted
// dispatches. Missing history stays unknown; receipt absence alone is not proof.
func (s *Service) ReconcileEmailEnable(ctx context.Context, actor Actor, a saveaction.Action) (string, error) {
	if _, err := saveaction.RequireExecution(saveaction.WithAction(ctx, a), actor.UserID, EmailEnablePath); err != nil {
		return "unknown", err
	}
	ctx = saveaction.WithoutAction(ctx)
	outcome := "unknown"
	err := s.transactOutcome(ctx, actor, false, false, func(tx pgx.Tx, p Principal) error {
		if err := emailAuthority(actor, p); err != nil {
			return err
		}
		if err := emailpolicy.Durable(ctx, tx); err != nil {
			return err
		}
		var err error
		outcome, err = saveaction.FenceExecution(ctx, tx, actor.UserID, a.ID, a.Path)
		if err != nil || outcome == "unknown" {
			return err
		}
		if err := saveaction.MatchExecution(ctx, tx, a); err != nil {
			return err
		}
		policy, err := emailpolicy.Read(ctx, tx, true)
		if err != nil {
			return err
		}
		if outcome == "rejected" && policy.Review() != a.Revision {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return "unknown", err
	}
	return outcome, nil
}

// RetryEmailEnable requires explicit same-intent review and CAS of the fenced
// token. Only an acknowledged transaction may dispatch this new token.
func (s *Service) RetryEmailEnable(ctx context.Context, actor Actor, a saveaction.Action, previousToken string) error {
	if _, err := saveaction.RequireExecution(saveaction.WithAction(ctx, a), actor.UserID, EmailEnablePath); err != nil {
		return err
	}
	ctx = saveaction.WithoutAction(ctx)
	return s.transactOutcome(ctx, actor, false, false, func(tx pgx.Tx, p Principal) error {
		if err := emailAuthority(actor, p); err != nil {
			return err
		}
		if err := emailpolicy.Durable(ctx, tx); err != nil {
			return err
		}
		// RetryExecution takes the action row before the policy row. A later
		// validation error rolls the token change back with the transaction.
		if err := saveaction.RetryExecution(ctx, tx, a, previousToken, a.IntentHash); err != nil {
			return err
		}
		if err := saveaction.MatchExecution(ctx, tx, a); err != nil {
			return err
		}
		policy, err := emailpolicy.Read(ctx, tx, true)
		if err != nil {
			return err
		}
		if policy.Review() != a.Revision {
			return ErrConflict
		}
		return nil
	})
}
