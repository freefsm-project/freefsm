package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/jackc/pgx/v5"
)

type PDFSource struct {
	LogoUnavailable bool
	Logo            []byte
	Asset           *ent.Asset
	FieldLines      []string
	Token           string
	ReviewToken     string
	Settings        *ent.CompanySettings
	Snapshot        []byte
	SettledCents    int64
	AllocatedCents  int64
	AppliedCents    int64
}
type PDFVersion struct {
	SourceKnown bool
	Source      []byte
	ID          int64
	GeneratedAt time.Time
	Stale       bool
	Filename    string
	Content     []byte
}

func pdfDocumentAccess(ctx context.Context, q queryer, p Principal, kind string, id int64, action string) (Scope, error) {
	if action != "view" && action != "pdf.generate" && action != "pdf.download" && action != "delivery.request" {
		return "", ErrForbidden
	}
	if kind != "invoice" && kind != "estimate" {
		return "", ErrNotFound
	}
	var job *int64
	var label string
	var termsRecorded bool
	var archived, hidden bool
	err := q.QueryRow(ctx, `SELECT job_id,deleted_at IS NOT NULL,conversion_hidden_at IS NOT NULL,number_label,terms IS NOT NULL FROM `+kind+`s WHERE id=$1 AND company_id=$2`, id, p.CompanyID).Scan(&job, &archived, &hidden, &label, &termsRecorded)
	if err == pgx.ErrNoRows || hidden {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	assigned := func() (bool, error) {
		if job == nil {
			return false, nil
		}
		var allowed bool
		err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=$1 AND a.user_id=$2 AND j.company_id=$3 AND j.deleted_at IS NULL)`, *job, p.UserID, p.CompanyID).Scan(&allowed)
		return allowed, err
	}
	scope := All
	if !p.Allows(kind+".view", All) {
		if !p.Allows(kind+".view", Assigned) || job == nil {
			return "", ErrNotFound
		}
		allowed, err := assigned()
		if err != nil {
			return "", err
		}
		if !allowed {
			return "", ErrNotFound
		}
		scope = Assigned
	}
	if archived && !p.Allows(kind+".archived.view", All) {
		return "", ErrNotFound
	}
	if action != "view" {
		if p.Allows(kind+"."+action, All) && p.Allows(kind+".view", All) {
			scope = All
		} else if p.Allows(kind+"."+action, Assigned) && p.Allows(kind+".view", Assigned) {
			allowed, err := assigned()
			if err != nil {
				return "", err
			}
			if !allowed {
				return "", ErrForbidden
			}
			scope = Assigned
		} else {
			return "", ErrForbidden
		}
	}
	if archived && (action == "pdf.generate" || action == "delivery.request") {
		return "", ErrForbidden
	}
	if action == "delivery.request" {
		var customerActive bool
		if err := q.QueryRow(ctx, `SELECT c.deleted_at IS NULL FROM `+kind+`s o JOIN customers c ON c.id=o.customer_id AND c.company_id=o.company_id WHERE o.id=$1 AND o.company_id=$2`, id, p.CompanyID).Scan(&customerActive); err != nil {
			return "", err
		}
		if !customerActive {
			return "", ErrForbidden
		}
	}
	if action == "pdf.generate" && (label == "" || !termsRecorded) {
		return "", ErrConflict
	}
	if action == "delivery.request" && kind == "invoice" {
		var void bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE i.id=$1 AND s.category_key='invoice:void')`, id).Scan(&void); err != nil {
			return "", err
		}
		if void {
			return "", ErrForbidden
		}
	}
	return scope, nil
}

func pdfSource(ctx context.Context, q queryer, p Principal, kind string, id int64) (PDFSource, error) {
	var settings, snapshot []byte
	var presentationRevision int64
	var documentRevision int64
	var relatedRevisions []byte
	// Only rendering presentation participates: editing future numbering/defaults
	// or collaboration does not invalidate an already generated version.
	settlement := ""
	if kind == "invoice" {
		settlement = `, 'allocations',(SELECT jsonb_agg(to_jsonb(a)||jsonb_build_object('reversed',EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=a.payment_id)) ORDER BY a.payment_id) FROM payment_invoice_allocations a WHERE a.invoice_id=d.id),'credit_applications',(SELECT jsonb_agg(to_jsonb(a)||jsonb_build_object('reversed',EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)) ORDER BY a.id) FROM credit_applications a WHERE a.invoice_id=d.id)`
	}
	statusJoin := `LEFT JOIN statuses st ON st.id=d.status_id`
	if kind == "invoice" {
		statusJoin = `LEFT JOIN invoice_effective_status es ON es.invoice_id=d.id LEFT JOIN statuses st ON st.id=es.status_id`
	}
	query := `SELECT presentation,snapshot,cs.pdf_presentation_revision,pdf_review_revision($3||'s',d.id),
 jsonb_build_array(pdf_review_revision('customers',c.id),pdf_review_revision('jobs',j.id),pdf_review_revision('statuses',st.id),
 CASE WHEN $3='invoice' THEN jsonb_build_array(pdf_review_revision('job_asset',j.id),pdf_review_revision('assets',asset.id),
 (SELECT jsonb_agg(jsonb_build_array(al.id,pdf_review_revision('job_asset_links',al.id)) ORDER BY al.id) FROM job_asset_links al WHERE al.job_id=j.id AND al.asset_id=j.asset_id)) END,
 coalesce((SELECT revision FROM pdf_field_source_revisions WHERE document_type=$3 AND document_id=d.id),1))
 FROM ` + kind + `s d LEFT JOIN customers c ON c.id=d.customer_id LEFT JOIN jobs j ON j.id=d.job_id
 LEFT JOIN assets asset ON asset.id=j.asset_id AND asset.customer_id=j.customer_id AND asset.deleted_at IS NULL AND EXISTS(SELECT 1 FROM job_asset_links al WHERE al.job_id=j.id AND al.asset_id=asset.id AND al.operational AND al.unlinked_at IS NULL)
 ` + statusJoin + ` JOIN company_settings cs ON cs.company_id=d.company_id
 CROSS JOIN LATERAL (SELECT jsonb_build_object('business_name',cs.business_name,'address',cs.address,'city',cs.city,'state',cs.state,'zip',cs.zip,'phone',cs.phone,'email',cs.email,'tax_id',cs.tax_id,'invoice_color',cs.invoice_color,'invoice_footer',cs.invoice_footer,'invoice_logo_path',cs.invoice_logo_path,'pdf_show_line_item_descriptions',cs.pdf_show_line_item_descriptions,'timezone',cs.timezone,'date_format',cs.date_format,'invoice_payment_terms',d.terms) AS presentation) presentation
 CROSS JOIN LATERAL (SELECT jsonb_build_object(
 'source_format',2,
 'document',(SELECT jsonb_object_agg(key,value) FROM jsonb_each(to_jsonb(d)) WHERE key=ANY(ARRAY['id','customer_id','job_id','number_label','invoice_number','title','notes','terms','line_items','tax_rate','financial','invoice_date','due_date','valid_through','created_at','status_id'])),
 'customer',CASE WHEN c.id IS NOT NULL THEN jsonb_build_object('id',c.id,'display_name',c.display_name,'company_name',c.company_name,'email',c.email,'phone',c.phone) END,
 'job',CASE WHEN j.id IS NOT NULL THEN jsonb_build_object('id',j.id,'job_type',j.job_type,'subtitle',j.subtitle,'start_time',j.start_time,'due_date',j.due_date) END,
 'asset',CASE WHEN asset.id IS NOT NULL AND $3='invoice' THEN jsonb_build_object('id',asset.id,'manufacturer',asset.manufacturer,'model',asset.model,'serial_number',asset.serial_number) END,
 'status',CASE WHEN st.id IS NOT NULL THEN jsonb_build_object('id',st.id,'name',st.name,'color',st.color,'category_key',st.category_key) END,
 'custom_field_values',(SELECT coalesce(jsonb_agg(jsonb_build_object('name',f.name,'type',f.field_type,'value',v->'value') ORDER BY f.sort_order,f.id),'[]'::jsonb) FROM jsonb_array_elements(d.custom_fields) v JOIN custom_field_definitions f ON f.id=(v->>'definition_id')::bigint WHERE f.include_pdf),
 'presentation',presentation` + settlement + `) AS snapshot) source WHERE d.id=$1 AND d.company_id=$2`
	if err := q.QueryRow(ctx, query, id, p.CompanyID, kind).Scan(&settings, &snapshot, &presentationRevision, &documentRevision, &relatedRevisions); err != nil {
		return PDFSource{}, err
	}
	cs := new(ent.CompanySettings)
	if err := json.Unmarshal(settings, cs); err != nil {
		return PDFSource{}, err
	}
	var logo []byte
	var logoUnavailable bool
	if cs.InvoiceLogoPath != "" {
		var err error
		logo, err = os.ReadFile(cs.InvoiceLogoPath)
		if err != nil {
			// Current branding is unavailable, so no current-source version can
			// be generated. Retained exact bytes remain selectable with a stale
			// warning; a missing branding file must not turn Download into render.
			logoUnavailable = true
		}
	}
	var captured map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &captured); err != nil {
		return PDFSource{}, err
	}
	captured["logo"], _ = json.Marshal(logo)
	if logoUnavailable {
		captured["logo_unavailable"] = json.RawMessage("true")
	}
	snapshot, err := json.Marshal(captured)
	if err != nil {
		return PDFSource{}, err
	}
	var fields struct {
		Values []struct {
			Name  string
			Type  string
			Value any
		} `json:"custom_field_values"`
	}
	if err := json.Unmarshal(snapshot, &fields); err != nil {
		return PDFSource{}, err
	}
	var fieldLines []string
	for _, field := range fields.Values {
		text := fmt.Sprint(field.Value)
		if field.Type == "checkbox" {
			if text == "true" {
				text = "Yes"
			} else if text == "false" {
				text = "No"
			}
		}
		fieldLines = append(fieldLines, field.Name+": "+text)
	}
	type allocation struct {
		Amount   int64 `json:"amount_cents"`
		Reversed bool  `json:"reversed"`
	}
	var financial struct {
		Asset        *ent.Asset   `json:"asset"`
		Allocations  []allocation `json:"allocations"`
		Applications []allocation `json:"credit_applications"`
	}
	if err := json.Unmarshal(snapshot, &financial); err != nil {
		return PDFSource{}, err
	}
	var settled, allocated, applied int64
	for index, entry := range append(financial.Allocations, financial.Applications...) {
		if entry.Reversed {
			continue
		}
		if entry.Amount < 0 || entry.Amount > math.MaxInt64-settled {
			return PDFSource{}, fmt.Errorf("invalid settlement snapshot")
		}
		settled += entry.Amount
		if index < len(financial.Allocations) {
			allocated += entry.Amount
		} else {
			applied += entry.Amount
		}
	}
	token, known := pdfSourceFingerprint(kind, snapshot)
	if !known {
		return PDFSource{}, fmt.Errorf("incomplete captured PDF source")
	}
	// Rendering equivalence can return to its former value; the review clocks
	// cannot. Keep their opaque identity out of immutable content provenance.
	// Append-only settlement facts cannot return to an earlier identity after a
	// reversal, even when their rendered totals return to the earlier amounts.
	review := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d:%s:%s:%s", token, presentationRevision, documentRevision, relatedRevisions, captured["allocations"], captured["credit_applications"])))
	return PDFSource{LogoUnavailable: logoUnavailable, Logo: logo, Asset: financial.Asset, Token: token, ReviewToken: fmt.Sprintf("%x", review), Settings: cs, Snapshot: snapshot, SettledCents: settled, AllocatedCents: allocated, AppliedCents: applied, FieldLines: fieldLines}, nil
}

func (s *Service) DocumentPDFSource(ctx context.Context, a Actor, kind string, id int64) (PDFSource, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return PDFSource{}, err
	}
	if _, err := pdfDocumentAccess(ctx, s.Pool, p, kind, id, "pdf.generate"); err != nil {
		return PDFSource{}, err
	}
	return pdfSource(ctx, s.Pool, p, kind, id)
}

func (s *Service) PDFPermission(ctx context.Context, a Actor, kind string, id int64, action string) error {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return err
	}
	_, err = pdfDocumentAccess(ctx, s.Pool, p, kind, id, action)
	return err
}

func (s *Service) SavePDFVersion(ctx context.Context, a Actor, kind string, id int64, source PDFSource, filename string, content []byte) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var version int64
		return savePDFVersion(ctx, tx, p, a, kind, id, source, filename, content, &version, nil)
	})
}

func savePDFVersion(ctx context.Context, tx pgx.Tx, p Principal, a Actor, kind string, id int64, source PDFSource, filename string, content []byte, version *int64, attribution *pdfGenerationAttribution) error {
	scope, err := pdfDocumentAccess(ctx, tx, p, kind, id, "pdf.generate")
	if err != nil {
		return err
	}
	// Keep presentation and document stable until source comparison and commit.
	if _, err := tx.Exec(ctx, `SELECT id FROM company_settings WHERE company_id=$1 FOR SHARE`, p.CompanyID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM `+kind+`s WHERE id=$1 FOR SHARE`, id); err != nil {
		return err
	}
	for _, query := range []string{
		`SELECT id FROM customers WHERE id=(SELECT customer_id FROM ` + kind + `s WHERE id=$1) FOR SHARE`,
		`SELECT id FROM jobs WHERE id=(SELECT job_id FROM ` + kind + `s WHERE id=$1) FOR SHARE`,
		`SELECT id FROM assets WHERE id=(SELECT asset_id FROM jobs WHERE id=(SELECT job_id FROM ` + kind + `s WHERE id=$1)) FOR SHARE`,
		`SELECT id FROM statuses WHERE id=(SELECT status_id FROM ` + kind + `s WHERE id=$1) FOR SHARE`,
	} {
		if _, err := tx.Exec(ctx, query, id); err != nil {
			return err
		}
	}
	if scope == Assigned {
		if _, err := tx.Exec(ctx, `SELECT job_id FROM job_assignments WHERE user_id=$1 AND job_id=(SELECT job_id FROM `+kind+`s WHERE id=$2) FOR SHARE`, p.UserID, id); err != nil {
			return err
		}
	}
	scope, err = pdfDocumentAccess(ctx, tx, p, kind, id, "pdf.generate")
	if err != nil {
		return err
	}
	current, err := pdfSource(ctx, tx, p, kind, id)
	if err != nil {
		return err
	}
	if current.Token != source.Token || current.ReviewToken != source.ReviewToken {
		return ErrConflict
	}
	if len(content) == 0 {
		return fmt.Errorf("empty PDF")
	}
	var accepted time.Time
	if err := tx.QueryRow(ctx, `INSERT INTO document_pdf_versions(company_id,document_type,document_id,source_token,filename,content,generated_by,source_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,generated_at`, p.CompanyID, kind, id, source.Token, filename, content, a.UserID, source.Snapshot).Scan(version, &accepted); err != nil {
		return err
	}
	detail := map[string]any{"pdf_version": *version, "accepted_at": accepted, "occurred_at": accepted, "occurrence_source": "server"}
	if attribution != nil {
		detail["operation_id"] = attribution.OperationID
		if attribution.OccurredAt != nil {
			detail["occurred_at"] = *attribution.OccurredAt
			detail["occurrence_source"] = "client"
		}
	}
	return audit(ctx, tx, a, p, kind+".pdf.generate", scope, kind, id, detail)
}

func (s *Service) PDFVersions(ctx context.Context, a Actor, kind string, id int64) ([]PDFVersion, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return nil, err
	}
	if _, err := pdfDocumentAccess(ctx, s.Pool, p, kind, id, "view"); err != nil {
		return nil, err
	}
	if a.Channel == "api" {
		if err := fieldPDFAnchor(ctx, s.Pool, kind, id); err != nil {
			return nil, err
		}
	}
	source, err := pdfSource(ctx, s.Pool, p, kind, id)
	if err != nil {
		return nil, err
	}
	return readPDFVersions(ctx, s.Pool, p, kind, id, source.Token, 0)
}

func (s *Service) DownloadPDFVersion(ctx context.Context, a Actor, kind string, id, version int64, confirm bool) (PDFVersion, error) {
	var v PDFVersion
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		scope, err := pdfDocumentAccess(ctx, tx, p, kind, id, "pdf.download")
		if err != nil {
			return err
		}
		if a.Channel == "api" {
			if err := fieldPDFAnchor(ctx, tx, kind, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `SELECT id FROM `+kind+`s WHERE id=$1 AND company_id=$2 FOR SHARE`, id, p.CompanyID); err != nil {
			return err
		}
		if scope == Assigned {
			if _, err := tx.Exec(ctx, `SELECT id FROM jobs WHERE id=(SELECT job_id FROM `+kind+`s WHERE id=$1) FOR SHARE`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT job_id FROM job_assignments WHERE user_id=$1 AND job_id=(SELECT job_id FROM `+kind+`s WHERE id=$2) FOR SHARE`, p.UserID, id); err != nil {
				return err
			}
		}
		scope, err = pdfDocumentAccess(ctx, tx, p, kind, id, "pdf.download")
		if err != nil {
			return err
		}
		if a.Channel == "api" {
			if err := fieldPDFAnchor(ctx, tx, kind, id); err != nil {
				return err
			}
		}
		source, err := pdfSource(ctx, tx, p, kind, id)
		if err != nil {
			return err
		}
		v, err = selectedPDFVersion(ctx, tx, p, kind, id, version, source.Token)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if v.Stale && !confirm {
			return ErrConflict
		}
		if v.Stale {
			return audit(ctx, tx, a, p, kind+".pdf.download", scope, kind, id, map[string]any{"pdf_version": v.ID, "stale_confirmed": true})
		}
		return nil
	})
	return v, err
}
