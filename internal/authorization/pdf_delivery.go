package authorization

import (
	"context"

	"github.com/google/uuid"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/jackc/pgx/v5"
)

type PDFDeliverySelection struct {
	CurrentToken string
	Version      PDFVersion
	Source       []byte
}

// BindDeliveryPDFOperation checks a prerequisite inside Delivery's admission and
// publication transaction. A receipt supplies an exact version, never a default.
func (s *Service) BindDeliveryPDFOperation(ctx context.Context, tx pgx.Tx, a Actor, deliveryID, operationID, kind string, document, version int64) error {
	if operationID == "" {
		return nil
	}
	key, err := uuid.Parse(operationID)
	if err != nil || key == uuid.Nil || key.String() != operationID || operationID == deliveryID {
		return &JobExecutionConflict{Code: "invalid_dependency"}
	}
	code, err := boundDependencyCode(ctx, tx, a, operationID)
	if err != nil {
		return err
	}
	if code == "" {
		return &JobExecutionConflict{Code: "invalid_dependency"}
	}
	if code != "accepted" {
		return &JobExecutionConflict{Code: "dependency_failed"}
	}
	var matches bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
	 SELECT 1 FROM operation_admissions a JOIN pdf_operations p USING(operation_id)
	 JOIN document_pdf_versions v ON v.id=(p.receipt->>'version_id')::bigint
	 WHERE a.operation_id=$1 AND a.operation_type=$2 AND v.document_type=$3
	 AND v.document_id=$4 AND v.id=$5 AND p.user_id=$6 AND p.client_key=$7
	)`, operationID, kind+".pdf.generate", kind, document, version, a.UserID, operationClientKey(a)).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return &JobExecutionConflict{Code: "invalid_dependency"}
	}
	return nil
}

// Retry authorizes the already frozen communication, without reloading live
// attachment bytes or substituting a PDF. File Download remains independent.
func (s *Service) AuthorizeDeliveryRetry(ctx context.Context, tx pgx.Tx, a Actor, company int64, kind string, id int64, extras bool) error {
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return err
	}
	if p.CompanyID != company {
		return ErrForbidden
	}
	for _, permission := range []string{"delivery.request", "pdf.download"} {
		if _, err := pdfDocumentAccess(ctx, tx, p, kind, id, permission); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT o.id FROM `+kind+`s o JOIN customers c ON c.id=o.customer_id WHERE o.id=$1 AND o.company_id=$2 FOR SHARE OF o,c`, id, company); err != nil {
		return err
	}
	if extras {
		ref, err := objectref.Parse(kind, id)
		if err != nil {
			return err
		}
		if _, err = fileAccess(ctx, tx, p, a, ref, "file.download", 0, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) AuditDeliveryRetry(ctx context.Context, tx pgx.Tx, a Actor, kind string, id, delivery int64, reason string, attribution ...map[string]any) error {
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return err
	}
	scope, err := pdfDocumentAccess(ctx, tx, p, kind, id, "delivery.request")
	if err != nil {
		return err
	}
	detail := map[string]any{"delivery_id": delivery, "reason": reason}
	for _, fields := range attribution {
		for key, value := range fields {
			detail[key] = value
		}
	}
	return audit(ctx, tx, a, p, kind+".delivery.retry", scope, kind, id, detail)
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
