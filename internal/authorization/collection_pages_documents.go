package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// DocumentListPage keeps document-specific status, date, scope and UI facet
// semantics. Report/export DocumentListSnapshot remains an unbounded selector.
func (s *Service) DocumentListPage(ctx context.Context, a Actor, kind string, filter DocumentListFilter, scope Scope, page, size int) (DocumentListReport, error) {
	var out DocumentListReport
	limit, offset, err := collectionPageBounds(page, size)
	if err != nil {
		return out, err
	}
	if a.Channel == "api" {
		return out, ErrForbidden
	}
	f, err := NormalizeDocumentCollectionFilter(kind, filter)
	if err != nil {
		return out, err
	}
	err = s.WithinSnapshotPermission(ctx, a, kind+".view", scope, func(tx pgx.Tx, p Principal) error {
		if err := AuthorizeDocumentListFilter(ctx, tx, p, kind, f, scope, false); err != nil {
			return err
		}
		out.Filter = f
		var err error
		out.AsOf, out.Timezone, err = documentReportClock(ctx, tx, p.CompanyID)
		if err != nil {
			return err
		}
		statusJoin := ` LEFT JOIN statuses s ON s.id=d.status_id`
		number, dates := `coalesce(d.document_number,0)`, `''::text,''::text,coalesce(d.valid_through::text,'')`
		valid := `($7='' OR d.valid_through IS NULL OR d.valid_through>=($8::timestamptz AT TIME ZONE $9)::date)`
		settlement := `CASE WHEN $13::boolean THEN '' ELSE '' END`
		if kind == "invoice" {
			statusJoin = ` LEFT JOIN invoice_effective_status es ON es.invoice_id=d.id LEFT JOIN statuses s ON s.id=es.status_id`
			number, dates = `d.invoice_number`, `d.invoice_date::text,coalesce(nullif(d.due_date,'0001-01-01'::date)::text,''),''::text`
			valid = `($7='' AND $8::timestamptz IS NOT NULL AND $9::text<>'')`
			settlement = `CASE WHEN $13 THEN d.settlement_state ELSE '' END`
		}
		order := map[string]string{"created_desc": "d.created_at DESC,d.id DESC", "created_asc": "d.created_at,d.id", "title_asc": "lower(d.title),d.id", "number_asc": number + ",d.id"}[f.Sort]
		anchor := ` CROSS JOIN LATERAL (SELECT EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id
 WHERE j.id=d.job_id AND j.company_id=d.company_id AND j.customer_id=d.customer_id AND j.deleted_at IS NULL AND a.user_id=$2) assigned) anchor`
		from := ` FROM ` + kind + `s d` + statusJoin + ` JOIN customers c ON c.id=d.customer_id AND c.company_id=d.company_id` + anchor
		where := ` WHERE d.company_id=$1 AND d.deleted_at IS NULL AND d.conversion_hidden_at IS NULL AND ($3 OR anchor.assigned)
 AND ($4='' OR strpos(lower(d.title),lower($4))>0) AND ($5::bigint=0 OR s.id=$5) AND ($6::bigint=0 OR d.customer_id=$6) AND ` + valid
		args := []any{p.CompanyID, p.UserID, scope == All, f.Search, f.StatusID, f.CustomerID, f.Valid, out.AsOf, out.Timezone}
		if err := tx.QueryRow(ctx, `SELECT count(*)`+from+where, args...).Scan(&out.Total); err != nil {
			return err
		}
		args = append(args, p.Allows("customer.view", All), p.Allows("customer.view", Context), p.Allows("pricing.view", All),
			p.Allows("settlement.view", All), p.Allows("pricing.view", Assigned), p.Allows("customer.archived.view", All), limit, offset)
		rows, err := tx.Query(ctx, `/* collection-page:document */ WITH page AS MATERIALIZED (
 SELECT d.id,s.id page_status_id,s.name page_status_name,s.color page_status_color,s.category_key page_category`+from+where+` ORDER BY `+order+` LIMIT $16 OFFSET $17
 ) SELECT d.id,`+number+`,coalesce(d.number_label,''),d.title,coalesce(d.job_id,0),d.customer_id,coalesce(page.page_status_id,0),coalesce(page.page_status_name,''),coalesce(page.page_status_color,''),coalesce(page.page_category,''),d.created_at,`+dates+`,
 CASE WHEN ($10 AND (c.deleted_at IS NULL OR $15)) OR (c.deleted_at IS NULL AND $11 AND anchor.assigned) THEN c.display_name ELSE '' END,
 CASE WHEN $12 OR ($14 AND anchor.assigned) THEN d.financial ELSE NULL END,`+settlement+`,
 (($10 AND (c.deleted_at IS NULL OR $15)) OR (c.deleted_at IS NULL AND $11 AND anchor.assigned))`+
			` FROM page JOIN `+kind+`s d ON d.id=page.id JOIN customers c ON c.id=d.customer_id AND c.company_id=d.company_id`+anchor+` ORDER BY `+order, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Rows = []DocumentListRow{}
		for rows.Next() {
			var r DocumentListRow
			if err := rows.Scan(&r.ID, &r.Number, &r.NumberLabel, &r.Title, &r.JobID, &r.CustomerID, &r.StatusID, &r.StatusName, &r.StatusColor, &r.Category,
				&r.CreatedAt, &r.InvoiceDate, &r.DueDate, &r.ValidThrough, &r.Customer, &r.Financial, &r.SettlementState, &r.Reference.CustomerVisible); err != nil {
				return err
			}
			r.Reference.ID, r.Reference.JobID, r.Reference.CustomerID = r.ID, r.JobID, r.CustomerID
			out.Rows = append(out.Rows, r)
		}
		return rows.Err()
	})
	if err != nil {
		return DocumentListReport{}, err
	}
	return out, nil
}
