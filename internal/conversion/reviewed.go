package conversion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/jackc/pgx/v5"
)

// ReviewedIntent is shared by the web form and field/reference client. A revert
// names the exact cycle the human reviewed; neither direction performs unwind.
type ReviewedIntent struct {
	authorization.CommercialIntent
	CycleID       string           `json:"cycle_id,omitempty"`
	InvoiceFields *json.RawMessage `json:"custom_fields,omitempty"`
}

func (s *Service) ExecuteReviewed(ctx context.Context, kind string, id int64, in ReviewedIntent) (authorization.CommercialReceipt, error) {
	bound, ok := authorization.FromContext(ctx)
	if !ok || (kind != "estimate" && kind != "invoice") {
		return authorization.CommercialReceipt{}, authorization.ErrForbidden
	}
	var convert authorization.ConversionAuthority
	var revert authorization.ReversionAuthority
	var actor Actor
	var actorName string
	intent, err := json.Marshal(map[string]any{"action": kind + ".conversion", "input": in})
	if err != nil {
		return authorization.CommercialReceipt{}, err
	}
	return bound.Service.CommercialTransitionOperation(ctx, bound.Actor, kind, id, in.CommercialIntent, intent, authorization.CommercialTransition{
		Check: func(ctx context.Context, tx pgx.Tx) error {
			var err error
			if kind == "estimate" {
				convert, err = bound.Service.AuthorizeConversion(ctx, tx, bound.Actor, id)
				if err != nil {
					return err
				}
				if in.CycleID != "" {
					return authorization.ErrConversionCycle
				}
				var hidden bool
				if err = tx.QueryRow(ctx, `SELECT conversion_hidden_at IS NOT NULL FROM estimates WHERE id=$1`, id).Scan(&hidden); err != nil {
					return err
				}
				if hidden {
					return authorization.ErrConversionCycle
				}
				if in.InvoiceFields != nil {
					if err = bound.Service.AuthorizeConversionValues(ctx, tx, convert); err != nil {
						return err
					}
				}
			} else {
				revert, err = bound.Service.AuthorizeReviewedReversion(ctx, tx, bound.Actor, id)
				if err != nil {
					return err
				}
				var cycle string
				if err = tx.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM estimate_invoice_conversion_cycles WHERE invoice_id=$1 AND reverted_at IS NULL),'')`, id).Scan(&cycle); err != nil {
					return err
				}
				if in.CycleID == "" || cycle != in.CycleID {
					return authorization.ErrConversionCycle
				}
				if in.InvoiceFields != nil {
					return authorization.ErrCommercialInput
				}
			}
			actor.ID = bound.Actor.UserID
			return tx.QueryRow(ctx, `SELECT company_id,name FROM users WHERE id=$1`, actor.ID).Scan(&actor.CompanyID, &actorName)
		},
		Write: func(ctx context.Context, tx pgx.Tx) (authorization.CommercialReceipt, error) {
			var result Result
			var err error
			targetKind := "invoice"
			if kind == "estimate" {
				var fields *string
				if in.InvoiceFields != nil {
					value := string(*in.InvoiceFields)
					fields = &value
				}
				result, err = s.convertTx(ctx, tx, actor, ConvertRequest{EstimateID: id, InvoiceFields: fields}, actorName)
				if err == nil {
					err = bound.Service.AuditConvertedDocument(ctx, tx, convert, result.InvoiceID)
				}
				if err == nil && fields != nil {
					err = bound.Service.AuditConversionValues(ctx, tx, convert, result.InvoiceID)
				}
			} else {
				targetKind = "estimate"
				result, err = s.revertTx(ctx, tx, actor, RevertRequest{InvoiceID: id}, actorName)
				if err == nil {
					err = bound.Service.AuditRevertedDocument(ctx, tx, revert, result.EstimateID)
				}
			}
			if err != nil {
				switch {
				case errors.Is(err, ErrSettlement):
					err = fmt.Errorf("%w: %v", authorization.ErrCommercialInput, err)
				case errors.Is(err, ErrArchived), errors.Is(err, ErrHistoricalFieldReview):
					err = fmt.Errorf("%w: %v", authorization.ErrConflict, err)
				case errors.Is(err, ErrNotFound):
					err = authorization.ErrNotFound
				}
				return authorization.CommercialReceipt{}, err
			}
			target := result.InvoiceID
			if kind == "invoice" {
				target = result.EstimateID
			}
			receipt := authorization.CommercialReceipt{ID: target, DocumentType: targetKind, CycleID: result.CycleID.String()}
			if err = tx.QueryRow(ctx, `SELECT number_label FROM `+targetKind+`s WHERE id=$1`, target).Scan(&receipt.Number); err != nil {
				return receipt, err
			}
			receipt.Revision, err = saveaction.Revision(ctx, tx, actor.ID, fmt.Sprintf("/%ss/%d", targetKind, target))
			return receipt, err
		},
	})
}
