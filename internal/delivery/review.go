package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/mail"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type MessageIntent struct {
	PDFVersionID    int64    `json:"pdf_version_id"`
	ConfirmStalePDF bool     `json:"confirm_stale_pdf"`
	To              []string `json:"to"`
	CC              []string `json:"cc"`
	BCC             []string `json:"bcc"`
	Subject         string   `json:"subject"`
	TextBody        string   `json:"text_body"`
	HTMLBody        string   `json:"html_body"`
	ExtraFileIDs    []int64  `json:"extra_file_ids"`
}
type ReviewedIntent struct {
	MessageIntent
	OperationID    string     `json:"operation_id"`
	ReviewToken    string     `json:"review_token"`
	JobID          int64      `json:"job_id"`
	OccurredAt     *time.Time `json:"occurred_at,omitempty"`
	PDFOperationID string     `json:"pdf_operation_id,omitempty"`
}
type FileReview struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
}
type Review struct {
	DocumentID     int64           `json:"document_id"`
	JobID          int64           `json:"job_id"`
	ReviewToken    string          `json:"review_token"`
	From           string          `json:"from"`
	PDFVersionID   int64           `json:"pdf_version_id"`
	PDFStale       bool            `json:"pdf_stale"`
	PDFSourceKnown bool            `json:"pdf_source_known"`
	PDFSHA256      string          `json:"pdf_sha256"`
	Files          []FileReview    `json:"files"`
	Settings       json.RawMessage `json:"settings"`
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func compositionSettings(ctx context.Context, tx pgx.Tx, company int64, kind string) ([]byte, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT jsonb_build_object('sender',smtp_from,'tracking_enabled',email_tracking_enabled,'copy_defaults',email_auto_cc,'subject_template',CASE WHEN $2='invoice' THEN invoice_email_subject ELSE estimate_email_subject END,'text_template',CASE WHEN $2='invoice' THEN invoice_email_body ELSE estimate_email_body END,'html_template',CASE WHEN $2='invoice' THEN invoice_email_html ELSE estimate_email_html END,'business_name',business_name) FROM company_settings WHERE company_id=$1 FOR SHARE`, company, kind).Scan(&raw)
	return raw, err
}

func (s *Service) reviewTx(ctx context.Context, tx pgx.Tx, request authorization.Request, p authorization.Principal, document DocumentRef, in MessageIntent) (Review, QueueRequest, error) {
	var out Review
	var queued QueueRequest
	if in.PDFVersionID <= 0 || len(in.To) == 0 || in.CC == nil || in.BCC == nil || strings.TrimSpace(in.Subject) == "" || (in.TextBody == "" && in.HTMLBody == "") || in.ExtraFileIDs == nil {
		return out, queued, authorization.ErrCommercialInput
	}
	for _, group := range [][]string{in.To, in.CC, in.BCC} {
		for _, recipient := range group {
			a, e := mail.ParseAddress(recipient)
			if e != nil || a.Address != recipient || strings.ContainsAny(recipient, "\r\n") {
				return out, queued, authorization.ErrCommercialInput
			}
		}
	}
	selected, err := request.Service.BindDeliveryPDF(ctx, tx, request.Actor, p.CompanyID, document.Type, document.ID, in.PDFVersionID)
	if err != nil {
		return out, queued, err
	}
	if selected.Version.Stale && !in.ConfirmStalePDF {
		return out, queued, authorization.ErrConflict
	}
	files, err := request.Service.BindDeliveryFiles(ctx, tx, request.Actor, document.Type, document.ID, in.ExtraFileIDs, len(selected.Version.Content))
	if err != nil {
		return out, queued, err
	}
	out.Settings, err = compositionSettings(ctx, tx, p.CompanyID, document.Type)
	if err != nil {
		return out, queued, err
	}
	var settings struct {
		Sender string `json:"sender"`
	}
	if err = json.Unmarshal(out.Settings, &settings); err != nil {
		return out, queued, err
	}
	out.From = settings.Sender
	if err = tx.QueryRow(ctx, `SELECT coalesce(job_id,0) FROM `+document.Type+`s WHERE id=$1 AND company_id=$2`, document.ID, p.CompanyID).Scan(&out.JobID); err != nil {
		return out, queued, err
	}
	if request.Actor.Channel == "api" && out.JobID == 0 {
		return out, queued, authorization.ErrNotFound
	}
	out.DocumentID = document.ID
	out.PDFVersionID = selected.Version.ID
	out.PDFStale = selected.Version.Stale
	out.PDFSourceKnown = selected.Version.SourceKnown
	out.PDFSHA256 = digest(selected.Version.Content)
	out.Files = []FileReview{}
	for _, f := range files {
		out.Files = append(out.Files, FileReview{f.ID, f.Name, f.ContentType, len(f.Data), digest(f.Data)})
	}
	raw, err := json.Marshal(struct {
		Document      DocumentRef
		Intent        MessageIntent
		Review        Review
		CurrentSource string
	}{document, in, out, selected.CurrentToken})
	if err != nil {
		return out, queued, err
	}
	out.ReviewToken = digest(raw)
	queued = QueueRequest{PDFVersionID: in.PDFVersionID, ConfirmStalePDF: in.ConfirmStalePDF, Document: document, ExtraFileIDs: in.ExtraFileIDs, Snapshot: Snapshot{To: in.To, CC: in.CC, BCC: in.BCC, Subject: in.Subject, TextBody: in.TextBody, HTMLBody: in.HTMLBody, PDF: selected.Version.Content, PDFFilename: selected.Version.Filename}}
	return out, queued, nil
}

func (s *Service) Review(ctx context.Context, document DocumentRef, in MessageIntent) (Review, error) {
	var out Review
	request, ok := authorization.FromContext(ctx)
	if !ok {
		return out, ErrForbidden
	}
	err := request.Service.DeliveryTransaction(ctx, request.Actor, func(tx pgx.Tx, p authorization.Principal) error {
		var err error
		out, _, err = s.reviewTx(ctx, tx, request, p, document, in)
		return err
	})
	return out, err
}

func (s *Service) QueueReviewed(ctx context.Context, document DocumentRef, in ReviewedIntent) (authorization.DeliveryReceipt, error) {
	request, ok := authorization.FromContext(ctx)
	if !ok {
		return authorization.DeliveryReceipt{}, ErrForbidden
	}
	if in.ReviewToken == "" || in.JobID <= 0 {
		return authorization.DeliveryReceipt{}, authorization.ErrCommercialInput
	}
	ctx, release, err := s.control.EnterEmail(ctx)
	if err != nil {
		return authorization.DeliveryReceipt{}, err
	}
	defer release()
	return request.Service.AcceptDeliveryOperation(ctx, request.Actor, in.OperationID, document.Type, document.ID, in, in.OccurredAt, func(tx pgx.Tx, p authorization.Principal) ([]int64, error) {
		if err := request.Service.BindDeliveryPDFOperation(ctx, tx, request.Actor, in.OperationID, in.PDFOperationID, document.Type, document.ID, in.PDFVersionID); err != nil {
			return nil, err
		}
		// A legacy outbox key is not evidence that this API/user/client intent
		// was accepted. Only the original bound receipt can reconcile it.
		var legacy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM document_deliveries WHERE company_id=$1 AND idempotency_key=$2)`, p.CompanyID, in.OperationID).Scan(&legacy); err != nil {
			return nil, err
		}
		if legacy {
			return nil, ErrIdempotencyConflict
		}
		review, r, err := s.reviewTx(ctx, tx, request, p, document, in.MessageIntent)
		if err != nil {
			return nil, err
		}
		if review.ReviewToken != in.ReviewToken || review.JobID != in.JobID {
			return nil, authorization.ErrConflict
		}
		r.Key = uuid.MustParse(in.OperationID)
		first, err := s.queueTx(ctx, tx, Actor{ID: request.Actor.UserID, CompanyID: p.CompanyID}, r)
		if err != nil {
			return nil, err
		}
		ids := []int64{first.ID}
		recipients := canonicalAddresses(append(append(append([]string{}, in.To...), in.CC...), in.BCC...))
		for _, recipient := range recipients[1:] {
			var id int64
			if err := tx.QueryRow(ctx, `SELECT id FROM document_deliveries WHERE company_id=$1 AND idempotency_key=$2`, p.CompanyID, uuid.NewSHA1(r.Key, []byte(recipient))).Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, nil
	})
}
