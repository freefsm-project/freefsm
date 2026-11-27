package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// DocumentListFilter is shared by the operational list and its full CSV. Sort
// always includes ID; pagination belongs to the consumer after selection.
type DocumentListFilter struct {
	Search     string `json:"search,omitempty"`
	StatusID   int64  `json:"status_id,omitempty"`
	CustomerID int64  `json:"customer_id,omitempty"`
	Valid      string `json:"valid,omitempty"`
	Sort       string `json:"sort,omitempty"`
}

type DocumentListRow struct {
	ID, Number, JobID, CustomerID, StatusID                         int64
	NumberLabel, Title, Customer, StatusName, StatusColor, Category string
	CreatedAt                                                       time.Time
	InvoiceDate, DueDate, ValidThrough                              string
	// These UI-only facets are never populated by an export snapshot.
	Financial       json.RawMessage
	SettlementState string
	Reference       DocumentListReference
}

type DocumentListReport struct {
	Filter   DocumentListFilter
	Rows     []DocumentListRow
	Total    int
	AsOf     time.Time
	Timezone string
}

type DocumentListReference struct {
	ID, JobID, CustomerID int64
	CustomerVisible       bool
}

func NormalizeDocumentListFilter(kind string, f DocumentListFilter) (DocumentListFilter, error) {
	return normalizeDocumentFilter(kind, f, true)
}

// NormalizeDocumentCollectionFilter uses the browser collection's Unicode
// character limit. Reports/exports retain their historical 1,000-byte limit.
func NormalizeDocumentCollectionFilter(kind string, f DocumentListFilter) (DocumentListFilter, error) {
	return normalizeDocumentFilter(kind, f, false)
}

func normalizeDocumentFilter(kind string, f DocumentListFilter, export bool) (DocumentListFilter, error) {
	if kind != "estimate" && kind != "invoice" {
		return f, ErrForbidden
	}
	f.Search = strings.TrimSpace(f.Search)
	oversized := utf8.RuneCountInString(f.Search) > 500
	if export {
		oversized = len(f.Search) > 1000
	}
	if f.StatusID < 0 || f.CustomerID < 0 || !utf8.ValidString(f.Search) || oversized || (f.Valid != "" && (kind != "estimate" || f.Valid != "current")) {
		return f, fmt.Errorf("%w: invalid document list filter", ErrConflict)
	}
	if f.Sort == "" {
		f.Sort = "created_desc"
	}
	switch f.Sort {
	case "created_desc", "created_asc", "title_asc", "number_asc":
	default:
		return f, fmt.Errorf("%w: invalid document list sort", ErrConflict)
	}
	return f, nil
}

func authorizeDocumentReport(p Principal, kind string, scope Scope, export bool) error {
	if (kind != "estimate" && kind != "invoice") || (scope != All && scope != Assigned) || !p.Allows(kind+".view", scope) || export && !p.Allows(kind+".export", scope) {
		return ErrForbidden
	}
	return nil
}

func documentReportClock(ctx context.Context, tx pgx.Tx, company int64) (time.Time, string, error) {
	var at time.Time
	var zone string
	err := tx.QueryRow(ctx, `SELECT transaction_timestamp(),coalesce(nullif(timezone,''),'UTC') FROM company_settings WHERE company_id=$1`, company).Scan(&at, &zone)
	if err != nil {
		return at, zone, err
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return at, zone, err
	}
	return at, zone, nil
}

// Customer selection is authorized even when the document query returns no rows.
func documentReportCustomer(ctx context.Context, tx pgx.Tx, p Principal, id int64) error {
	if id == 0 {
		return nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customers c WHERE c.id=$1 AND c.company_id=$2 AND (($3 AND (c.deleted_at IS NULL OR $6)) OR (c.deleted_at IS NULL AND $4 AND EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.customer_id=c.id AND j.company_id=c.company_id AND j.deleted_at IS NULL AND a.user_id=$5))))`, id, p.CompanyID, p.Allows("customer.view", All), p.Allows("customer.view", Context), p.UserID, p.Allows("customer.archived.view", All)).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

func AuthorizeDocumentListFilter(ctx context.Context, tx pgx.Tx, p Principal, kind string, f DocumentListFilter, scope Scope, export bool) error {
	if err := authorizeDocumentReport(p, kind, scope, export); err != nil {
		return err
	}
	var err error
	if f, err = normalizeDocumentFilter(kind, f, export); err != nil {
		return err
	}
	if err := documentReportCustomer(ctx, tx, p, f.CustomerID); err != nil {
		return err
	}
	if f.StatusID != 0 {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.id=$1 AND w.company_id=$2 AND w.object_type=$3)`, f.StatusID, p.CompanyID, kind).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
	}
	return nil
}

// DocumentListSnapshot performs every read on the caller's fenced transaction.
// Explicit Assigned selection stays bounded even with All View grants.
func DocumentListSnapshot(ctx context.Context, tx pgx.Tx, p Principal, kind string, filter DocumentListFilter, scope Scope, export bool) (DocumentListReport, error) {
	out := DocumentListReport{Rows: []DocumentListRow{}}
	f, err := normalizeDocumentFilter(kind, filter, export)
	if err != nil {
		return out, err
	}
	if err := AuthorizeDocumentListFilter(ctx, tx, p, kind, f, scope, export); err != nil {
		return out, err
	}
	out.Filter = f
	out.AsOf, out.Timezone, err = documentReportClock(ctx, tx, p.CompanyID)
	if err != nil {
		return DocumentListReport{}, err
	}
	statusJoin := `LEFT JOIN statuses s ON s.id=d.status_id`
	number, dates, valid, settlement := `coalesce(d.document_number,0)`, `''::text,''::text,coalesce(d.valid_through::text,'')`, `($7='' OR d.valid_through IS NULL OR d.valid_through>=($8::timestamptz AT TIME ZONE $9)::date)`, `CASE WHEN $13::boolean THEN '' ELSE '' END`
	if kind == "invoice" {
		statusJoin = `LEFT JOIN invoice_effective_status es ON es.invoice_id=d.id LEFT JOIN statuses s ON s.id=es.status_id`
		number, dates, valid = `d.invoice_number`, `d.invoice_date::text,coalesce(nullif(d.due_date,'0001-01-01'::date)::text,''),''::text`, `($7='' AND $8::timestamptz IS NOT NULL AND $9::text<>'')`
		settlement = `CASE WHEN $13 THEN d.settlement_state ELSE '' END`
	}
	order := map[string]string{"created_desc": "d.created_at DESC,d.id DESC", "created_asc": "d.created_at,d.id", "title_asc": "lower(d.title),d.id", "number_asc": number + ",d.id"}[f.Sort]
	// The assignment expression also controls optional UI pricing and Customer
	// context, independently of the selected document scope.
	query := `SELECT d.id,` + number + `,coalesce(d.number_label,''),d.title,coalesce(d.job_id,0),d.customer_id,coalesce(s.id,0),coalesce(s.name,''),coalesce(s.color,''),coalesce(s.category_key,''),d.created_at,` + dates + `,
	 CASE WHEN ($10 AND (c.deleted_at IS NULL OR $15)) OR (c.deleted_at IS NULL AND $11 AND anchor.assigned) THEN c.display_name ELSE '' END,
	 CASE WHEN $12 OR ($14 AND anchor.assigned) THEN d.financial ELSE NULL END,` + settlement + `,(($10 AND (c.deleted_at IS NULL OR $15)) OR (c.deleted_at IS NULL AND $11 AND anchor.assigned))
	 FROM ` + kind + `s d ` + statusJoin + ` JOIN customers c ON c.id=d.customer_id AND c.company_id=d.company_id
	 CROSS JOIN LATERAL (SELECT EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=d.job_id AND j.company_id=d.company_id AND j.customer_id=d.customer_id AND j.deleted_at IS NULL AND a.user_id=$2) assigned) anchor
	 WHERE d.company_id=$1 AND d.deleted_at IS NULL AND d.conversion_hidden_at IS NULL AND ($3 OR anchor.assigned)
	 AND ($4='' OR strpos(lower(d.title),lower($4))>0) AND ($5::bigint=0 OR s.id=$5) AND ($6::bigint=0 OR d.customer_id=$6) AND ` + valid + ` ORDER BY ` + order
	rows, err := tx.Query(ctx, query, p.CompanyID, p.UserID, scope == All, f.Search, f.StatusID, f.CustomerID, f.Valid, out.AsOf, out.Timezone, p.Allows("customer.view", All), p.Allows("customer.view", Context), !export && p.Allows("pricing.view", All), !export && p.Allows("settlement.view", All), !export && p.Allows("pricing.view", Assigned), p.Allows("customer.archived.view", All))
	if err != nil {
		return DocumentListReport{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var r DocumentListRow
		if err := rows.Scan(&r.ID, &r.Number, &r.NumberLabel, &r.Title, &r.JobID, &r.CustomerID, &r.StatusID, &r.StatusName, &r.StatusColor, &r.Category, &r.CreatedAt, &r.InvoiceDate, &r.DueDate, &r.ValidThrough, &r.Customer, &r.Financial, &r.SettlementState, &r.Reference.CustomerVisible); err != nil {
			return DocumentListReport{}, err
		}
		r.Reference.ID, r.Reference.JobID, r.Reference.CustomerID = r.ID, r.JobID, r.CustomerID
		out.Rows = append(out.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return DocumentListReport{}, err
	}
	out.Total = len(out.Rows)
	return out, nil
}

func (s *Service) DocumentList(ctx context.Context, a Actor, kind string, f DocumentListFilter, scope Scope) (DocumentListReport, error) {
	var out DocumentListReport
	if a.Channel == "api" {
		return out, ErrForbidden
	}
	err := s.WithinSnapshotPermission(ctx, a, kind+".view", scope, func(tx pgx.Tx, p Principal) error {
		var err error
		out, err = DocumentListSnapshot(ctx, tx, p, kind, f, scope, false)
		return err
	})
	if err != nil {
		return DocumentListReport{}, err
	}
	return out, nil
}

func documentReportAnchor(ctx context.Context, tx pgx.Tx, p Principal, job, customer int64, scope Scope) error {
	if scope == All {
		return nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=$1 AND j.company_id=$2 AND j.deleted_at IS NULL AND a.user_id=$3 AND j.customer_id=$4)`, job, p.CompanyID, p.UserID, customer).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

func CheckDocumentListReference(ctx context.Context, tx pgx.Tx, p Principal, kind string, ref DocumentListReference, scope Scope, export bool) error {
	if err := authorizeDocumentReport(p, kind, scope, export); err != nil {
		return err
	}
	var job, customer int64
	err := tx.QueryRow(ctx, `SELECT coalesce(job_id,0),customer_id FROM `+kind+`s WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL AND conversion_hidden_at IS NULL`, ref.ID, p.CompanyID).Scan(&job, &customer)
	if err == pgx.ErrNoRows {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	for _, anchor := range []struct{ job, customer int64 }{{ref.JobID, ref.CustomerID}, {job, customer}} {
		if err := documentReportAnchor(ctx, tx, p, anchor.job, anchor.customer, scope); err != nil {
			return err
		}
	}
	if ref.CustomerVisible {
		for _, anchor := range []struct{ customer, job int64 }{{ref.CustomerID, ref.JobID}, {customer, job}} {
			if err := documentReportCustomer(ctx, tx, p, anchor.customer); err != nil {
				return err
			}
			if !p.Allows("customer.view", All) {
				// Context never substitutes a different Job for the first link
				// that justified the captured Customer name.
				if err := documentReportAnchor(ctx, tx, p, anchor.job, anchor.customer, Assigned); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
