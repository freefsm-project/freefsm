package statusflow

import (
	"context"
	"errors"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/jackc/pgx/v5"
)

// AcceptanceHook advances an unchanged document after its delivery is accepted.
// The delivery service supplies the transaction so acceptance, transition, and
// activity are committed or rolled back together.
type AcceptanceHook struct{ service *Service }

func NewAcceptanceHook(service *Service) AcceptanceHook { return AcceptanceHook{service: service} }

func (h AcceptanceHook) OnAccepted(ctx context.Context, tx pgx.Tx, d delivery.Delivery) error {
	typ := ObjectType(d.DocumentType)
	var sent CategoryKey
	switch typ {
	case Estimate:
		sent = EstimateSent
	case Invoice:
		sent = InvoiceSent
	default:
		return nil
	}
	var current *int64
	query := `SELECT status_id FROM ` + string(typ) + `s WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL FOR UPDATE`
	if err := tx.QueryRow(ctx, query, d.DocumentID, d.CompanyID).Scan(&current); err != nil {
		return mapNoRows(err)
	}
	if !sameStatus(current, d.ExpectedStatusID) {
		return nil
	}
	var target int64
	if err := tx.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id
		WHERE w.company_id=$1 AND w.object_type=$2 AND s.category_key=$3 AND s.is_category_default`, d.CompanyID, typ, sent).Scan(&target); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := allowStatusUpdates(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE `+string(typ)+`s SET status_id=$1,updated_at=now() WHERE id=$2`, target, d.DocumentID); err != nil {
		return err
	}
	return activity(ctx, tx, Actor{ID: d.ActorID, CompanyID: d.CompanyID}, "status_transitioned", typ, d.DocumentID, map[string]any{"from_status_id": current, "to_status_id": target, "category_key": sent, "trusted_source": "document_delivery"})
}

func sameStatus(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
