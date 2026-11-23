package authorization

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrJobOperationalFilter = errors.New("invalid Job operational filter")

type JobOperationalFilter struct {
	Search     string `json:"search"`
	StatusID   int64  `json:"status_id"`
	CustomerID int64  `json:"customer_id"`
	Sort       string `json:"sort"`
}

// JobOperationalRow contains no pricing or financial facets.
type JobOperationalRow struct {
	ID, CustomerID, StatusID                           int64
	Title, Subtitle, Customer, Status, Category, Color string
	Start, End, ArrivalStart, ArrivalEnd, Due          *time.Time
	CreatedAt                                          time.Time
	AssigneeIDs                                        []int64
	Assignees                                          []string
	Reference                                          JobOperationalReference
}

type JobOperationalReference struct {
	JobID, CustomerID int64
}

type JobOperationalReport struct {
	Filter   JobOperationalFilter
	Rows     []JobOperationalRow
	AsOf     time.Time
	Timezone string
	// UIBillingTypes is an independently authorized UI facet populated only by
	// the web service wrapper, never by the operational/export selector.
	UIBillingTypes map[int64]string `json:"-"`
}

func NormalizeJobOperationalFilter(f JobOperationalFilter) (JobOperationalFilter, error) {
	f.Search = strings.TrimSpace(f.Search)
	if f.Sort == "" {
		f.Sort = "schedule_desc"
	}
	if f.StatusID < 0 || f.CustomerID < 0 || f.Sort != "schedule_desc" {
		return f, ErrJobOperationalFilter
	}
	return f, nil
}

func authorizeOperational(p Principal, scope Scope) error {
	if (scope != Assigned && scope != All) || !p.Allows("job.view", scope) {
		return ErrForbidden
	}
	return nil
}

// AuthorizeJobOperationalExport checks the explicit export scope even with broad View.
func AuthorizeJobOperationalExport(p Principal, scope Scope) error {
	if err := authorizeOperational(p, scope); err != nil {
		return err
	}
	if !p.Allows("job.export", scope) {
		return ErrForbidden
	}
	return nil
}

// CheckJobOperationalFilter validates selected identities even for empty results.
func CheckJobOperationalFilter(ctx context.Context, tx pgx.Tx, p Principal, f JobOperationalFilter, scope Scope) error {
	if err := authorizeOperational(p, scope); err != nil {
		return err
	}
	if _, err := NormalizeJobOperationalFilter(f); err != nil {
		return err
	}
	if f.CustomerID != 0 {
		if err := checkOperationalCustomer(ctx, tx, p, f.CustomerID, 0); err != nil {
			return err
		}
	}
	if f.StatusID != 0 {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM statuses WHERE id=$1 AND company_id=$2 AND category_key LIKE 'job:%')`, f.StatusID, p.CompanyID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
	}
	return nil
}

// A zero Job checks a selected Customer through any active assigned first link;
// a row reference requires the exact original Job anchor.
func checkOperationalCustomer(ctx context.Context, tx pgx.Tx, p Principal, customer, job int64) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customers c WHERE c.id=$1 AND c.company_id=$2
 AND (($3 AND (c.deleted_at IS NULL OR $4)) OR ($5 AND c.deleted_at IS NULL AND EXISTS(
 SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.customer_id=c.id AND j.company_id=c.company_id
 AND j.deleted_at IS NULL AND a.user_id=$6 AND ($7::bigint=0 OR j.id=$7)))))`,
		customer, p.CompanyID, p.Allows("customer.view", All), p.Allows("customer.archived.view", All), p.Allows("customer.view", Context), p.UserID, job).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

func JobOperationalSnapshot(ctx context.Context, tx pgx.Tx, p Principal, filter JobOperationalFilter, scope Scope) (JobOperationalReport, error) {
	var out JobOperationalReport
	f, err := NormalizeJobOperationalFilter(filter)
	if err != nil {
		return out, err
	}
	if err = CheckJobOperationalFilter(ctx, tx, p, f, scope); err != nil {
		return out, err
	}
	out.Filter = f
	if err = tx.QueryRow(ctx, `SELECT transaction_timestamp(),coalesce(nullif(timezone,''),'UTC') FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&out.AsOf, &out.Timezone); err != nil {
		return JobOperationalReport{}, err
	}
	if _, err = time.LoadLocation(out.Timezone); err != nil {
		return JobOperationalReport{}, err
	}
	rows, err := tx.Query(ctx, `SELECT j.id,j.job_type,coalesce(j.subtitle,''),j.customer_id,coalesce(j.status_id,0),
 coalesce(c.display_name,''),coalesce(st.name,''),coalesce(st.category_key,''),coalesce(st.color,''),
 j.start_time,j.end_time,j.arrival_window_start,j.arrival_window_end,j.due_date,j.created_at,
 coalesce((SELECT array_agg(u.id ORDER BY u.id) FROM job_assignments a JOIN users u ON u.id=a.user_id AND u.company_id=j.company_id WHERE a.job_id=j.id),'{}'::bigint[]),
 coalesce((SELECT array_agg(u.name || CASE WHEN u.is_active THEN '' ELSE ' (unavailable)' END ORDER BY u.id) FROM job_assignments a JOIN users u ON u.id=a.user_id AND u.company_id=j.company_id WHERE a.job_id=j.id),'{}'::text[]),coalesce(c.id,0)
 FROM jobs j LEFT JOIN statuses st ON st.id=j.status_id AND st.company_id=j.company_id
 LEFT JOIN customers c ON c.id=j.customer_id AND c.company_id=j.company_id
 AND (($7 AND (c.deleted_at IS NULL OR $8)) OR ($9 AND c.deleted_at IS NULL AND EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.user_id=$2)))
 WHERE j.company_id=$1 AND j.deleted_at IS NULL AND ($3 OR EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.user_id=$2))
 AND ($4='' OR strpos(lower(j.job_type),lower($4))>0 OR strpos(lower(coalesce(j.subtitle,'')),lower($4))>0)
 AND ($5::bigint=0 OR j.status_id=$5) AND ($6::bigint=0 OR j.customer_id=$6)
 ORDER BY j.start_time DESC,j.created_at DESC,j.id DESC`, p.CompanyID, p.UserID, scope == All, f.Search, f.StatusID, f.CustomerID, p.Allows("customer.view", All), p.Allows("customer.archived.view", All), p.Allows("customer.view", Context))
	if err != nil {
		return JobOperationalReport{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var r JobOperationalRow
		if err := rows.Scan(&r.ID, &r.Title, &r.Subtitle, &r.CustomerID, &r.StatusID, &r.Customer, &r.Status, &r.Category, &r.Color, &r.Start, &r.End, &r.ArrivalStart, &r.ArrivalEnd, &r.Due, &r.CreatedAt, &r.AssigneeIDs, &r.Assignees, &r.Reference.CustomerID); err != nil {
			return JobOperationalReport{}, err
		}
		r.Reference.JobID = r.ID
		// The linked Customer identity is itself a projected facet.
		r.CustomerID = r.Reference.CustomerID
		out.Rows = append(out.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return JobOperationalReport{}, err
	}
	return out, nil
}

func CheckJobOperationalReference(ctx context.Context, tx pgx.Tx, p Principal, ref JobOperationalReference, scope Scope) error {
	if err := AuthorizeJobOperationalExport(p, scope); err != nil {
		return err
	}
	var customer int64
	var assigned bool
	err := tx.QueryRow(ctx, `SELECT customer_id,EXISTS(SELECT 1 FROM job_assignments WHERE job_id=j.id AND user_id=$3) FROM jobs j WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL`, ref.JobID, p.CompanyID, p.UserID).Scan(&customer, &assigned)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if scope == Assigned && !assigned {
		return ErrForbidden
	}
	if ref.CustomerID != 0 {
		// Context access must still be anchored by this exact Job, not a sibling.
		if !p.Allows("customer.view", All) && (!assigned || customer != ref.CustomerID || !p.Allows("customer.view", Context)) {
			return ErrForbidden
		}
		if err := checkOperationalCustomer(ctx, tx, p, ref.CustomerID, ref.JobID); err != nil {
			return err
		}
		if err := checkOperationalCustomer(ctx, tx, p, customer, ref.JobID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) JobOperational(ctx context.Context, a Actor, f JobOperationalFilter, scope Scope) (JobOperationalReport, error) {
	var out JobOperationalReport
	if a.Channel == "api" {
		return out, ErrForbidden
	}
	err := s.WithinSnapshotPermission(ctx, a, "job.view", scope, func(tx pgx.Tx, p Principal) error {
		var err error
		out, err = JobOperationalSnapshot(ctx, tx, p, f, scope)
		if err != nil {
			return err
		}
		if len(out.Rows) == 0 || (!p.Allows("pricing.view", All) && !p.Allows("pricing.view", Assigned)) {
			return nil
		}
		ids := make([]int64, 0, len(out.Rows))
		for _, row := range out.Rows {
			ids = append(ids, row.ID)
		}
		rows, err := tx.Query(ctx, `SELECT j.id,j.billing_type FROM jobs j WHERE j.company_id=$1 AND j.id=ANY($2::bigint[]) AND j.deleted_at IS NULL
 AND ($3 OR EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.user_id=$4))`, p.CompanyID, ids, p.Allows("pricing.view", All), p.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.UIBillingTypes = make(map[int64]string)
		for rows.Next() {
			var id int64
			var billing string
			if err := rows.Scan(&id, &billing); err != nil {
				return err
			}
			out.UIBillingTypes[id] = billing
		}
		return rows.Err()
	})
	if err != nil {
		return JobOperationalReport{}, err
	}
	return out, nil
}
