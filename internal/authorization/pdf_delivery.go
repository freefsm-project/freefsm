package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type PDFDeliverySelection struct {
	CurrentToken string
	Version      PDFVersion
	Source       []byte
}

func selectDeliveryPDF(ctx context.Context, q queryer, p Principal, kind string, id, version int64) (PDFDeliverySelection, error) {
	var out PDFDeliverySelection
	if _, err := pdfDocumentAccess(ctx, q, p, kind, id, "delivery.request"); err != nil {
		return out, err
	}
	if _, err := pdfDocumentAccess(ctx, q, p, kind, id, "pdf.download"); err != nil {
		return out, err
	}
	source, err := pdfSource(ctx, q, p, kind, id)
	if err != nil {
		return out, err
	}
	out.Version, err = selectedPDFVersion(ctx, q, p, kind, id, version, source.Token)
	out.CurrentToken = source.ReviewToken
	out.Source = out.Version.Source
	if err == pgx.ErrNoRows {
		return out, ErrConflict
	}
	return out, err
}

func (s *Service) DeliveryPDF(ctx context.Context, a Actor, kind string, id, version int64) (PDFDeliverySelection, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return PDFDeliverySelection{}, err
	}
	return selectDeliveryPDF(ctx, s.Pool, p, kind, id, version)
}

// BindDeliveryPDF joins Delivery's transaction; ownership of the outbox and its
// immutable message snapshot stays with Delivery. The selected bytes are loaded
// from the immutable version rather than trusted from a caller-provided payload.
func (s *Service) BindDeliveryPDF(ctx context.Context, tx pgx.Tx, a Actor, company int64, kind string, id, version int64) (PDFDeliverySelection, error) {
	if version <= 0 {
		return PDFDeliverySelection{}, ErrConflict
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return PDFDeliverySelection{}, err
	}
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return PDFDeliverySelection{}, err
	}
	if p.CompanyID != company {
		return PDFDeliverySelection{}, ErrForbidden
	}
	scope, err := pdfDocumentAccess(ctx, tx, p, kind, id, "delivery.request")
	if err != nil {
		return PDFDeliverySelection{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM `+kind+`s WHERE id=$1 AND company_id=$2 FOR SHARE`, id, company); err != nil {
		return PDFDeliverySelection{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM company_settings WHERE company_id=$1 FOR SHARE`, company); err != nil {
		return PDFDeliverySelection{}, err
	}
	if scope == Assigned {
		if _, err := tx.Exec(ctx, `SELECT id FROM jobs WHERE id=(SELECT job_id FROM `+kind+`s WHERE id=$1) FOR SHARE`, id); err != nil {
			return PDFDeliverySelection{}, err
		}
		if _, err := tx.Exec(ctx, `SELECT job_id FROM job_assignments WHERE user_id=$1 AND job_id=(SELECT job_id FROM `+kind+`s WHERE id=$2) FOR SHARE`, a.UserID, id); err != nil {
			return PDFDeliverySelection{}, err
		}
	}
	return selectDeliveryPDF(ctx, tx, p, kind, id, version)
}

func (s *Service) AuditPDFDelivery(ctx context.Context, tx pgx.Tx, a Actor, kind string, id, version, delivery int64, stale bool) error {
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return err
	}
	scope, err := pdfDocumentAccess(ctx, tx, p, kind, id, "delivery.request")
	if err != nil {
		return err
	}
	return audit(ctx, tx, a, p, kind+".delivery.request", scope, kind, id, map[string]any{"pdf_version": version, "delivery_id": delivery, "stale_confirmed": stale})
}
