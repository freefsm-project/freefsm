package authorization

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Creation bounds are inclusive company-local dates, never Invoice dates.
type EstimateCohortFilter struct {
	CreatedFrom string `json:"created_from,omitempty"`
	CreatedTo   string `json:"created_to,omitempty"`
	CustomerID  int64  `json:"customer_id,omitempty"`
	Category    string `json:"category,omitempty"`
	Conversion  string `json:"conversion,omitempty"` // active or none
	Sort        string `json:"sort,omitempty"`
}

type EstimateCohortReference struct {
	EstimateID, EstimateJobID, InvoiceID, InvoiceJobID int64
	CycleID                                            string
	Hidden                                             bool
}

type EstimateCohortRow struct {
	EstimateID, EstimateNumber               int64
	NumberLabel, Title, Category, StatusName string
	CreatedAt                                time.Time
	Hidden, ActiveConversion                 bool
	EstimateURL, InvoiceURL                  string
	InvoiceID                                int64
	InvoiceNumberLabel                       string
	Reference                                EstimateCohortReference
}

type EstimateCohortTotals struct {
	Total, ActiveConversions int
	Categories               map[string]int
}

type EstimateCohortReport struct {
	Filter      EstimateCohortFilter
	Rows        []EstimateCohortRow
	Totals      EstimateCohortTotals
	AsOf        time.Time
	Timezone    string
	Description string
}

func NormalizeEstimateCohortFilter(f EstimateCohortFilter) (EstimateCohortFilter, error) {
	f.CreatedFrom, f.CreatedTo = strings.TrimSpace(f.CreatedFrom), strings.TrimSpace(f.CreatedTo)
	for _, d := range []string{f.CreatedFrom, f.CreatedTo} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return f, fmt.Errorf("invalid creation date")
			}
		}
	}
	if f.CreatedFrom != "" && f.CreatedTo != "" && f.CreatedFrom > f.CreatedTo || f.CustomerID < 0 {
		return f, fmt.Errorf("invalid cohort filter")
	}
	switch f.Category {
	case "", "estimate:draft", "estimate:estimate", "estimate:sent", "estimate:accepted", "estimate:rejected", "estimate:completed":
	default:
		return f, fmt.Errorf("invalid Estimate category")
	}
	switch f.Conversion {
	case "", "active", "none":
	default:
		return f, fmt.Errorf("invalid conversion filter")
	}
	if f.Sort == "" {
		f.Sort = "created_desc"
	}
	switch f.Sort {
	case "created_desc", "created_asc", "title_asc", "number_asc":
	default:
		return f, fmt.Errorf("invalid cohort sort")
	}
	return f, nil
}

func AuthorizeEstimateCohortFilter(ctx context.Context, tx pgx.Tx, p Principal, f EstimateCohortFilter, scope Scope, export bool) error {
	if err := authorizeDocumentReport(p, "estimate", scope, export); err != nil {
		return err
	}
	if _, err := NormalizeEstimateCohortFilter(f); err != nil {
		return err
	}
	if export || f.Conversion != "" {
		if err := authorizeDocumentReport(p, "invoice", scope, export); err != nil {
			return err
		}
	}
	return documentReportCustomer(ctx, tx, p, f.CustomerID)
}

// EstimateCohortSnapshot is purpose-specific provenance access: ordinary parent
// access still rejects hidden records. Both source and counterpart must qualify
// independently, before rows, categories, or conversion totals are computed.
func EstimateCohortSnapshot(ctx context.Context, tx pgx.Tx, p Principal, filter EstimateCohortFilter, scope Scope, export bool) (EstimateCohortReport, error) {
	out := EstimateCohortReport{Rows: []EstimateCohortRow{}, Totals: EstimateCohortTotals{Categories: map[string]int{}}, Description: "Current state of the Estimate creation cohort; not historical as-of stages."}
	f, err := NormalizeEstimateCohortFilter(filter)
	if err != nil {
		return out, err
	}
	if err := AuthorizeEstimateCohortFilter(ctx, tx, p, f, scope, export); err != nil {
		return out, err
	}
	out.Filter = f
	out.AsOf, out.Timezone, err = documentReportClock(ctx, tx, p.CompanyID)
	if err != nil {
		return EstimateCohortReport{}, err
	}
	invoiceAllowed := authorizeDocumentReport(p, "invoice", scope, export) == nil
	order := map[string]string{"created_desc": "e.created_at DESC,e.id DESC", "created_asc": "e.created_at,e.id", "title_asc": "lower(e.title),e.id", "number_asc": "coalesce(e.document_number,0),e.id"}[f.Sort]
	rows, err := tx.Query(ctx, `SELECT e.id,coalesce(e.document_number,0),coalesce(e.number_label,''),e.title,e.created_at,coalesce(s.category_key,''),coalesce(s.name,''),e.conversion_hidden_at IS NOT NULL,coalesce(e.job_id,0),coalesce(c.id::text,''),coalesce(i.id,0),coalesce(i.job_id,0),coalesce(i.number_label,'')
	FROM estimates e LEFT JOIN statuses s ON s.id=e.status_id
	LEFT JOIN estimate_invoice_conversion_cycles c ON c.estimate_id=e.id AND c.company_id=e.company_id AND c.reverted_at IS NULL
	LEFT JOIN invoices i ON i.id=c.invoice_id AND i.company_id=e.company_id
	WHERE e.company_id=$1 AND e.deleted_at IS NULL
	AND ($3 OR EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=e.job_id AND j.company_id=e.company_id AND j.deleted_at IS NULL AND a.user_id=$2))
	AND (c.id IS NULL AND e.conversion_hidden_at IS NULL OR c.id IS NOT NULL AND $4 AND i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL AND ($3 OR EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=i.job_id AND j.company_id=i.company_id AND j.deleted_at IS NULL AND a.user_id=$2)))
	AND ($5='' OR e.created_at >= (nullif($5,'')::date::timestamp AT TIME ZONE $7))
	AND ($6='' OR e.created_at < ((nullif($6,'')::date+1)::timestamp AT TIME ZONE $7))
	AND ($8::bigint=0 OR e.customer_id=$8) AND ($9='' OR s.category_key=$9)
	AND ($10='' OR $10='active' AND c.id IS NOT NULL OR $10='none' AND c.id IS NULL)
	ORDER BY `+order, p.CompanyID, p.UserID, scope == All, invoiceAllowed, f.CreatedFrom, f.CreatedTo, out.Timezone, f.CustomerID, f.Category, f.Conversion)
	if err != nil {
		return EstimateCohortReport{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var r EstimateCohortRow
		if err := rows.Scan(&r.EstimateID, &r.EstimateNumber, &r.NumberLabel, &r.Title, &r.CreatedAt, &r.Category, &r.StatusName, &r.Hidden, &r.Reference.EstimateJobID, &r.Reference.CycleID, &r.InvoiceID, &r.Reference.InvoiceJobID, &r.InvoiceNumberLabel); err != nil {
			return EstimateCohortReport{}, err
		}
		r.ActiveConversion = r.Reference.CycleID != ""
		r.Reference.EstimateID, r.Reference.InvoiceID, r.Reference.Hidden = r.EstimateID, r.InvoiceID, r.Hidden
		if !r.Hidden {
			r.EstimateURL = fmt.Sprintf("/estimates/%d", r.EstimateID)
		}
		if r.ActiveConversion {
			r.InvoiceURL = fmt.Sprintf("/invoices/%d", r.InvoiceID)
			out.Totals.ActiveConversions++
		}
		out.Totals.Categories[r.Category]++
		out.Rows = append(out.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return EstimateCohortReport{}, err
	}
	out.Totals.Total = len(out.Rows)
	return out, nil
}

func (s *Service) EstimateCohort(ctx context.Context, a Actor, f EstimateCohortFilter, scope Scope) (EstimateCohortReport, error) {
	var out EstimateCohortReport
	if a.Channel == "api" {
		return out, ErrForbidden
	}
	err := s.WithinSnapshotPermission(ctx, a, "estimate.view", scope, func(tx pgx.Tx, p Principal) error {
		var err error
		out, err = EstimateCohortSnapshot(ctx, tx, p, f, scope, false)
		return err
	})
	if err != nil {
		return EstimateCohortReport{}, err
	}
	return out, nil
}

// Rechecks bind the precise cycle (including its absence), hidden state, and
// original AND current Job anchors. Revert/reconvert cannot revive an artifact.
func CheckEstimateCohortReference(ctx context.Context, tx pgx.Tx, p Principal, ref EstimateCohortReference, scope Scope, export bool) error {
	if err := authorizeDocumentReport(p, "estimate", scope, export); err != nil {
		return err
	}
	var job, invoice, invoiceJob int64
	var cycle string
	var hidden, invoiceActive bool
	err := tx.QueryRow(ctx, `SELECT coalesce(e.job_id,0),e.conversion_hidden_at IS NOT NULL,coalesce(c.id::text,''),coalesce(i.id,0),coalesce(i.job_id,0),coalesce(i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL AND i.id IS NOT NULL,false)
	FROM estimates e LEFT JOIN estimate_invoice_conversion_cycles c ON c.estimate_id=e.id AND c.company_id=e.company_id AND c.reverted_at IS NULL LEFT JOIN invoices i ON i.id=c.invoice_id AND i.company_id=e.company_id WHERE e.id=$1 AND e.company_id=$2 AND e.deleted_at IS NULL`, ref.EstimateID, p.CompanyID).Scan(&job, &hidden, &cycle, &invoice, &invoiceJob, &invoiceActive)
	if err == pgx.ErrNoRows {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if cycle != ref.CycleID || invoice != ref.InvoiceID || hidden != ref.Hidden || hidden && cycle == "" {
		return ErrForbidden
	}
	for _, anchor := range []int64{ref.EstimateJobID, job} {
		if err := cohortProvenanceAnchor(ctx, tx, p, anchor, scope); err != nil {
			return err
		}
	}
	if cycle != "" {
		if !invoiceActive {
			return ErrForbidden
		}
		if err := authorizeDocumentReport(p, "invoice", scope, export); err != nil {
			return err
		}
		for _, anchor := range []int64{ref.InvoiceJobID, invoiceJob} {
			if err := cohortProvenanceAnchor(ctx, tx, p, anchor, scope); err != nil {
				return err
			}
		}
	}
	return nil
}

// Provenance retains source facts through Job Customer reassignment. Unlike an
// operational list anchor, it requires the document's own active assigned Job,
// not equality between the retained Customer and the Job's current Customer.
func cohortProvenanceAnchor(ctx context.Context, tx pgx.Tx, p Principal, job int64, scope Scope) error {
	if scope == All {
		return nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=$1 AND j.company_id=$2 AND j.deleted_at IS NULL AND a.user_id=$3)`, job, p.CompanyID, p.UserID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}
