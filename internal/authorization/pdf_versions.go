package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/jackc/pgx/v5"
)

type PDFSource struct {
	FieldLines   []string
	Token        string
	ReviewToken  string
	Settings     *ent.CompanySettings
	Snapshot     []byte
	SettledCents int64
}
type PDFVersion struct {
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
	var settings, snapshot, review []byte
	// Only rendering presentation participates: editing future numbering/defaults
	// or collaboration does not invalidate an already generated version.
	settlement := ""
	if kind == "invoice" {
		settlement = `, 'allocations',(SELECT jsonb_agg(to_jsonb(a)||jsonb_build_object('reversed',EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=a.payment_id)) ORDER BY a.payment_id) FROM payment_invoice_allocations a WHERE a.invoice_id=d.id),'credit_applications',(SELECT jsonb_agg(to_jsonb(a)||jsonb_build_object('reversed',EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)) ORDER BY a.id) FROM credit_applications a WHERE a.invoice_id=d.id)`
	}
	query := `SELECT presentation,snapshot,jsonb_build_object('configuration_revision',cs.configuration_revision,'document',to_jsonb(d),'customer',to_jsonb(c),'job',to_jsonb(j),'asset',to_jsonb(asset),'status',to_jsonb(st)) FROM ` + kind + `s d LEFT JOIN customers c ON c.id=d.customer_id LEFT JOIN jobs j ON j.id=d.job_id
 LEFT JOIN assets asset ON asset.id=j.asset_id AND asset.customer_id=j.customer_id AND EXISTS(SELECT 1 FROM job_asset_links al WHERE al.job_id=j.id AND al.asset_id=asset.id AND al.operational AND al.unlinked_at IS NULL)
 LEFT JOIN statuses st ON st.id=d.status_id JOIN company_settings cs ON cs.company_id=d.company_id CROSS JOIN LATERAL (SELECT jsonb_build_object('business_name',cs.business_name,'address',cs.address,'city',cs.city,'state',cs.state,'zip',cs.zip,'phone',cs.phone,'email',cs.email,'tax_id',cs.tax_id,'invoice_color',cs.invoice_color,'invoice_footer',cs.invoice_footer,'invoice_logo_path',cs.invoice_logo_path,'pdf_show_line_item_descriptions',cs.pdf_show_line_item_descriptions,'timezone',cs.timezone,'date_format',cs.date_format,'invoice_payment_terms',CASE WHEN d.terms IS NULL THEN cs.invoice_payment_terms ELSE d.terms END) AS presentation) presentation CROSS JOIN LATERAL (SELECT jsonb_build_object('document',to_jsonb(d)-'updated_at','customer',jsonb_build_object('display_name',c.display_name,'company_name',c.company_name,'email',c.email,'phone',c.phone),'job',jsonb_build_object('job_type',j.job_type,'subtitle',j.subtitle,'start_time',j.start_time,'due_date',j.due_date),'asset',jsonb_build_object('manufacturer',asset.manufacturer,'model',asset.model,'serial_number',asset.serial_number),'status',to_jsonb(st),'presentation',presentation` + settlement + `) AS snapshot) source WHERE d.id=$1 AND d.company_id=$2`
	if err := q.QueryRow(ctx, query, id, p.CompanyID).Scan(&settings, &snapshot, &review); err != nil {
		return PDFSource{}, err
	}
	fieldLines, err := pdfFieldLines(ctx, q, kind, id, p.CompanyID)
	if err != nil {
		return PDFSource{}, err
	}
	if len(fieldLines) > 0 {
		var captured map[string]json.RawMessage
		if err = json.Unmarshal(snapshot, &captured); err != nil {
			return PDFSource{}, err
		}
		captured["custom_field_lines"], err = json.Marshal(fieldLines)
		if err != nil {
			return PDFSource{}, err
		}
		snapshot, err = json.Marshal(captured)
		if err != nil {
			return PDFSource{}, err
		}
	}
	review, err = json.Marshal(struct {
		State  json.RawMessage
		RoleID int64
		Grants []Grant
	}{review, p.RoleID, p.Grants})
	if err != nil {
		return PDFSource{}, err
	}
	cs := new(ent.CompanySettings)
	if err := json.Unmarshal(settings, cs); err != nil {
		return PDFSource{}, err
	}
	type allocation struct {
		Amount   int64 `json:"amount_cents"`
		Reversed bool  `json:"reversed"`
	}
	var financial struct {
		Allocations  []allocation `json:"allocations"`
		Applications []allocation `json:"credit_applications"`
	}
	if err := json.Unmarshal(snapshot, &financial); err != nil {
		return PDFSource{}, err
	}
	var settled int64
	for _, entry := range append(financial.Allocations, financial.Applications...) {
		if entry.Reversed {
			continue
		}
		if entry.Amount < 0 || entry.Amount > math.MaxInt64-settled {
			return PDFSource{}, fmt.Errorf("invalid settlement snapshot")
		}
		settled += entry.Amount
	}
	return PDFSource{Token: fmt.Sprintf("%x", sha256.Sum256(snapshot)), ReviewToken: fmt.Sprintf("%x", sha256.Sum256(review)), Settings: cs, Snapshot: snapshot, SettledCents: settled, FieldLines: fieldLines}, nil
}

func pdfFieldLines(ctx context.Context, q queryer, kind string, id, company int64) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT f.name,f.field_type,v->'value' FROM `+kind+`s d,jsonb_array_elements(d.custom_fields) v JOIN custom_field_definitions f ON f.id=(v->>'definition_id')::bigint WHERE d.id=$1 AND d.company_id=$2 AND f.include_pdf ORDER BY f.sort_order,f.id`, id, company)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var name, typ string
		var raw []byte
		if err = rows.Scan(&name, &typ, &raw); err != nil {
			return nil, err
		}
		var value any
		if err = json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		text := fmt.Sprint(value)
		if typ == "checkbox" {
			if text == "true" {
				text = "Yes"
			} else if text == "false" {
				text = "No"
			}
		}
		lines = append(lines, name+": "+text)
	}
	return lines, rows.Err()
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

func (s *Service) SavePDFVersion(ctx context.Context, a Actor, kind string, id int64, source, reviewToken, filename string, content []byte) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
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
		if current.Token != source || current.ReviewToken != reviewToken {
			return ErrConflict
		}
		if len(content) == 0 {
			return fmt.Errorf("empty PDF")
		}
		var version int64
		if err := tx.QueryRow(ctx, `INSERT INTO document_pdf_versions(company_id,document_type,document_id,source_token,filename,content,generated_by,source_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, p.CompanyID, kind, id, source, filename, content, a.UserID, current.Snapshot).Scan(&version); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, kind+".pdf.generate", scope, kind, id, map[string]any{"pdf_version": version})
	})
}

func (s *Service) PDFVersions(ctx context.Context, a Actor, kind string, id int64) ([]PDFVersion, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return nil, err
	}
	if _, err := pdfDocumentAccess(ctx, s.Pool, p, kind, id, "view"); err != nil {
		return nil, err
	}
	source, err := pdfSource(ctx, s.Pool, p, kind, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,generated_at,source_token<>$4,filename FROM document_pdf_versions WHERE company_id=$1 AND document_type=$2 AND document_id=$3 ORDER BY id DESC`, p.CompanyID, kind, id, source.Token)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := []PDFVersion{}
	for rows.Next() {
		var v PDFVersion
		if err := rows.Scan(&v.ID, &v.GeneratedAt, &v.Stale, &v.Filename); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

func (s *Service) DownloadPDFVersion(ctx context.Context, a Actor, kind string, id, version int64, confirm bool) (PDFVersion, error) {
	var v PDFVersion
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		scope, err := pdfDocumentAccess(ctx, tx, p, kind, id, "pdf.download")
		if err != nil {
			return err
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
		source, err := pdfSource(ctx, tx, p, kind, id)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT id,generated_at,source_token<>$5,filename,content FROM document_pdf_versions WHERE company_id=$1 AND document_type=$2 AND document_id=$3 AND (($4=0 AND source_token=$5) OR id=$4) ORDER BY id DESC LIMIT 1`, p.CompanyID, kind, id, version, source.Token).Scan(&v.ID, &v.GeneratedAt, &v.Stale, &v.Filename, &v.Content)
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
