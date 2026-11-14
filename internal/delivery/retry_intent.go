package delivery

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// RetryIntent names an existing frozen communication, never replacement content.
type RetryIntent struct {
	OperationID  string     `json:"operation_id"`
	DocumentType string     `json:"document_type"`
	DocumentID   int64      `json:"document_id"`
	JobID        int64      `json:"job_id"`
	DeliveryID   int64      `json:"delivery_id"`
	Reason       string     `json:"reason"`
	OccurredAt   *time.Time `json:"occurred_at,omitempty"`
}

func (s *Service) Retry(ctx context.Context, in RetryIntent) (authorization.DeliveryReceipt, error) {
	request, ok := authorization.FromContext(ctx)
	if !ok {
		return authorization.DeliveryReceipt{}, ErrForbidden
	}
	if (in.DocumentType != "estimate" && in.DocumentType != "invoice") || in.DocumentID <= 0 || in.JobID <= 0 || in.DeliveryID <= 0 || strings.TrimSpace(in.Reason) == "" {
		return authorization.DeliveryReceipt{}, authorization.ErrCommercialInput
	}
	ctx, release, err := s.control.EnterEmail(ctx)
	if err != nil {
		return authorization.DeliveryReceipt{}, err
	}
	defer release()
	return request.Service.AcceptDeliveryRetry(ctx, request.Actor, in.OperationID, in.DeliveryID, in, in.OccurredAt, func(tx pgx.Tx, p authorization.Principal) error {
		err := s.manualRetryTx(ctx, tx, Actor{ID: request.Actor.UserID, CompanyID: p.CompanyID}, in.DeliveryID, strings.TrimSpace(in.Reason), &in)
		switch {
		case errors.Is(err, ErrNotFound):
			return authorization.ErrNotFound
		case errors.Is(err, ErrForbidden):
			return authorization.ErrForbidden
		case errors.Is(err, ErrInvalid):
			return &authorization.JobExecutionConflict{Code: "invalid_state"}
		}
		return err
	})
}
