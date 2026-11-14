package delivery

import (
	"context"
	"errors"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// RetryTarget names the existing immutable communication and its original parent.
// It contains no replacement PDF, File or message inputs.
type RetryTarget struct {
	DocumentType string `json:"document_type"`
	DocumentID   int64  `json:"document_id"`
	JobID        int64  `json:"job_id"`
	DeliveryID   int64  `json:"delivery_id"`
}

type RetryReview struct {
	RetryTarget
	Action string `json:"action"`
}

// ReviewRetry establishes current action authority for offline staging. It is
// not admission: Retry still checks current authority and lifecycle at commit.
// In particular, this never selects a new PDF or reads a live File.
func (s *Service) ReviewRetry(ctx context.Context, target RetryTarget) (RetryReview, error) {
	var out RetryReview
	request, ok := authorization.FromContext(ctx)
	if !ok {
		return out, authorization.ErrForbidden
	}
	if (target.DocumentType != "estimate" && target.DocumentType != "invoice") || target.DocumentID <= 0 || target.JobID <= 0 || target.DeliveryID <= 0 {
		return out, authorization.ErrCommercialInput
	}
	err := request.Service.DeliveryTransaction(ctx, request.Actor, func(tx pgx.Tx, p authorization.Principal) error {
		d := Delivery{CompanyID: p.CompanyID}
		var extras bool
		err := tx.QueryRow(ctx, `SELECT document_type,document_id,coalesce(jsonb_array_length(attachment_snapshots),0)>0 FROM document_deliveries WHERE id=$1 AND company_id=$2`, target.DeliveryID, p.CompanyID).Scan(&d.DocumentType, &d.DocumentID, &extras)
		if err != nil {
			return err
		}
		if d.DocumentType != target.DocumentType || d.DocumentID != target.DocumentID {
			return authorization.ErrNotFound
		}
		job, err := documentJobID(ctx, tx, d)
		if err != nil {
			return err
		}
		if job == nil || *job != target.JobID {
			return authorization.ErrNotFound
		}
		if err := request.Service.AuthorizeDeliveryRetry(ctx, tx, request.Actor, p.CompanyID, d.DocumentType, d.DocumentID, extras); err != nil {
			return err
		}
		out = RetryReview{RetryTarget: target, Action: "delivery.retry"}
		return nil
	})
	if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		err = authorization.ErrNotFound
	}
	return out, err
}
