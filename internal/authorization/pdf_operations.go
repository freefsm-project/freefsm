package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PDFIntent struct {
	OperationID string     `json:"operation_id"`
	JobID       int64      `json:"job_id"`
	SourceToken string     `json:"source_token"`
	DependsOn   []string   `json:"depends_on"`
	OccurredAt  *time.Time `json:"occurred_at,omitempty"`
}

// Receipts retain effect identity, never captured document content or PDF bytes.
type PDFReceipt struct {
	OperationID string `json:"operation_id"`
	Outcome     string `json:"outcome"`
	VersionID   int64  `json:"version_id,omitempty"`
}

type PDFRenderer func(string, PDFSource) (string, []byte, error)

// Attribution accompanies the accepted effect into the protected audit writer;
// the operation receipt is not a replacement for occurrence evidence.
type pdfGenerationAttribution struct {
	OperationID string
	OccurredAt  *time.Time
}

func (s *Service) ReviewedPDFSource(ctx context.Context, a Actor, kind string, id int64) (PDFSource, int64, error) {
	var source PDFSource
	var job int64
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := pdfDocumentAccess(ctx, tx, p, kind, id, "pdf.generate"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(job_id,0) FROM `+kind+`s WHERE id=$1`, id).Scan(&job); err != nil {
			return err
		}
		if job == 0 {
			return ErrNotFound
		}
		var err error
		source, err = pdfSource(ctx, tx, p, kind, id)
		return err
	})
	return source, job, err
}

func pdfReceipt(ctx context.Context, tx pgx.Tx, a Actor, id string) (PDFReceipt, error) {
	var receipt PDFReceipt
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT receipt FROM pdf_operations WHERE operation_id=$1 AND user_id=$2 AND client_key=$3`, id, a.UserID, operationClientKey(a)).Scan(&raw)
	if err == pgx.ErrNoRows {
		return receipt, ErrNotFound
	}
	if err != nil {
		return receipt, err
	}
	err = json.Unmarshal(raw, &receipt)
	return receipt, err
}

func (s *Service) PDFOperationReceipt(ctx context.Context, a Actor, id string) (PDFReceipt, error) {
	var receipt PDFReceipt
	if key, err := uuid.Parse(id); err != nil || key == uuid.Nil {
		return receipt, ErrNotFound
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, _ Principal) error {
		var err error
		receipt, err = pdfReceipt(ctx, tx, a, id)
		return err
	})
	return receipt, err
}

func (s *Service) GenerateReviewedPDF(ctx context.Context, a Actor, kind string, id int64, in PDFIntent, render PDFRenderer) (PDFReceipt, error) {
	r := PDFReceipt{OperationID: in.OperationID}
	if key, err := uuid.Parse(in.OperationID); err != nil || key == uuid.Nil || key.String() != in.OperationID || in.JobID <= 0 || in.SourceToken == "" || in.DependsOn == nil || len(in.DependsOn) > 100 || a.Channel != "api" {
		return r, ErrCommercialInput
	}
	if kind != "invoice" && kind != "estimate" {
		return r, ErrNotFound
	}
	for _, dep := range in.DependsOn {
		if key, err := uuid.Parse(dep); err != nil || key == uuid.Nil || key.String() != dep || dep == in.OperationID {
			return r, ErrCommercialInput
		}
	}
	if in.OccurredAt != nil && (in.OccurredAt.IsZero() || in.OccurredAt.After(time.Now())) {
		return r, ErrCommercialInput
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if err := admitOperation(ctx, tx, a, in.OperationID, kind+".pdf.generate", map[string]any{"document_id": id, "intent": in}); err != nil {
			return err
		}
		prior, err := pdfReceipt(ctx, tx, a, in.OperationID)
		if err == nil {
			r = prior
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		effect := func() error {
			for _, dep := range in.DependsOn {
				code, err := boundDependencyCode(ctx, sp, a, dep)
				if err == nil && code == "" {
					r.Outcome = "invalid_dependency"
					return nil
				}
				if err != nil {
					return err
				}
				if code != "accepted" {
					r.Outcome = "dependency_failed"
					return nil
				}
			}
			if _, err := pdfDocumentAccess(ctx, sp, p, kind, id, "pdf.generate"); err != nil {
				return err
			}
			var job int64
			if err := sp.QueryRow(ctx, `SELECT coalesce(job_id,0) FROM `+kind+`s WHERE id=$1`, id).Scan(&job); err != nil {
				return err
			}
			if job == 0 {
				return ErrNotFound
			}
			if job != in.JobID {
				return ErrConflict
			}
			source, err := pdfSource(ctx, sp, p, kind, id)
			if err != nil {
				return err
			}
			if in.SourceToken != source.ReviewToken {
				return ErrConflict
			}
			filename, content, err := render(kind, source)
			if err != nil {
				return err
			}
			return savePDFVersion(ctx, sp, p, a, kind, id, source, filename, content, &r.VersionID, &pdfGenerationAttribution{OperationID: in.OperationID, OccurredAt: in.OccurredAt})
		}()
		if effect != nil {
			_ = sp.Rollback(ctx)
			r.VersionID = 0
			switch {
			case errors.Is(effect, ErrConflict):
				r.Outcome = "source_drift"
			case errors.Is(effect, ErrForbidden):
				r.Outcome = "forbidden"
			case errors.Is(effect, ErrNotFound):
				r.Outcome = "not_found"
			default:
				return effect // renderer/storage/audit failure is nonterminal
			}
		} else {
			if err := sp.Commit(ctx); err != nil {
				return err
			}
			if r.Outcome == "" {
				r.Outcome = "accepted"
			}
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO pdf_operations(operation_id,user_id,client_key,receipt) VALUES($1,$2,$3,$4)`, in.OperationID, a.UserID, operationClientKey(a), raw)
		return err
	})
	return r, err
}
