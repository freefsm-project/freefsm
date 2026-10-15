package statusflow

import (
	"context"
	"errors"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AcceptanceHook advances an unchanged document after its delivery is accepted.
// The delivery service supplies the transaction so acceptance, transition, and
// activity are committed or rolled back together.
type AcceptanceHook struct{ service *Service }

func NewAcceptanceHook(service *Service) AcceptanceHook { return AcceptanceHook{service: service} }

func (h AcceptanceHook) OnAccepted(ctx context.Context, tx pgx.Tx, d delivery.Delivery) error {
	// The delivery service acquires this before locking its outbox row. Also
	// fence standalone hook callers before any workflow/document row locks.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
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
	var statusRevision, meaningRevision uuid.UUID
	var category CategoryKey
	query := `SELECT o.status_id,s.category_key,o.status_revision,s.meaning_revision FROM ` + string(typ) + `s o JOIN statuses s ON s.id=o.status_id WHERE o.id=$1 AND o.company_id=$2 AND o.deleted_at IS NULL AND o.conversion_hidden_at IS NULL FOR UPDATE OF o,s`
	if err := tx.QueryRow(ctx, query, d.DocumentID, d.CompanyID).Scan(&current, &category, &statusRevision, &meaningRevision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // Sending succeeded even if the document is no longer active.
		}
		return err
	}
	if !sameStatus(current, d.ExpectedStatusID) || d.ExpectedStatusRevision == nil || d.ExpectedMeaningRevision == nil || statusRevision != *d.ExpectedStatusRevision || meaningRevision != *d.ExpectedMeaningRevision {
		return nil
	}
	if (typ == Estimate && category != EstimateDraft && category != EstimateEstimate) || (typ == Invoice && category != InvoiceInvoiced) {
		return nil
	}
	if typ == Invoice {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT invoice_has_active_settlement($1)`, d.DocumentID).Scan(&active); err != nil {
			return err
		}
		if active {
			// Sending remains eligible; its acceptance must not mutate the
			// settlement-locked preserved manual workflow.
			return nil
		}
	}
	var target int64
	if err := tx.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id
		WHERE w.company_id=$1 AND w.object_type=$2 AND s.category_key=$3 AND s.is_category_default FOR UPDATE OF s`, d.CompanyID, typ, sent).Scan(&target); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // Missing automation destination cannot undo SMTP acceptance.
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
