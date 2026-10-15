package statusflow

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

type CommercialIntent struct {
	authorization.CommercialIntent
	StatusID        int64  `json:"status_id"`
	MeaningRevision string `json:"meaning_revision"`
}

type commercialIntentKey struct{}

// ApplyCommercial executes the reviewed lifecycle intent inside the existing
// commercial operation transaction. Replays never invoke the transition again.
func (s *Service) ApplyCommercial(ctx context.Context, actor authorization.Actor, typ ObjectType, id int64, finalize bool, in CommercialIntent) (authorization.CommercialReceipt, error) {
	intent, err := json.Marshal(struct {
		Action string           `json:"action"`
		Input  CommercialIntent `json:"input"`
	}{map[bool]string{true: "finalize", false: "status"}[finalize], in})
	if err != nil {
		return authorization.CommercialReceipt{}, err
	}
	return authorization.New(s.db).CommercialWorkflowOperation(ctx, actor, string(typ), id, finalize, in.CommercialIntent, intent, func(ctx context.Context, tx pgx.Tx, p authorization.Principal) error {
		var meaning string
		var category CategoryKey
		err := tx.QueryRow(ctx, `SELECT s.meaning_revision::text,s.category_key FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.id=$1 AND w.company_id=$2 AND w.object_type=$3 FOR SHARE OF s`, in.StatusID, p.CompanyID, typ).Scan(&meaning, &category)
		if errors.Is(err, pgx.ErrNoRows) {
			return authorization.ErrConflict
		}
		if err != nil {
			return err
		}
		if in.MeaningRevision == "" || meaning != in.MeaningRevision {
			return authorization.ErrConflict
		}
		permission := string(typ) + ".status"
		if finalize {
			permission = "invoice.finalize"
			var current CategoryKey
			if err := tx.QueryRow(ctx, `SELECT s.category_key FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE i.id=$1`, id).Scan(&current); err != nil {
				return err
			}
			if current != InvoiceDraft || category != InvoiceInvoiced {
				return authorization.ErrConflict
			}
		}
		ctx = context.WithValue(ctx, commercialIntentKey{}, in)
		err = s.transitionTx(ctx, tx, Actor{ID: p.UserID, CompanyID: p.CompanyID, principal: &p, permission: permission}, typ, id, in.StatusID)
		switch {
		case errors.Is(err, ErrActiveSettlement), errors.Is(err, ErrInvalidTransition):
			return authorization.ErrConflict
		case errors.Is(err, ErrPaymentDerived), errors.Is(err, ErrWrongType):
			return authorization.ErrCommercialInput
		case errors.Is(err, ErrForbidden):
			return authorization.ErrForbidden
		case errors.Is(err, ErrNotFound):
			return authorization.ErrNotFound
		}
		return err
	})
}
