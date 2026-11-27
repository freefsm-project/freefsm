package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type JobOperationalPage struct {
	JobOperationalReport
	Total int
}

// JobOperationalPage preserves the operational list's reference/assignment and
// independently authorized UI Pricing facets, enriching only the SQL page.
func (s *Service) JobOperationalPage(ctx context.Context, a Actor, filter JobOperationalFilter, scope Scope, page, size int) (JobOperationalPage, error) {
	var out JobOperationalPage
	limit, offset, err := collectionPageBounds(page, size)
	if err != nil {
		return out, err
	}
	if a.Channel == "api" {
		return out, ErrForbidden
	}
	f, err := NormalizeJobOperationalFilter(filter)
	if err != nil {
		return out, err
	}
	err = s.WithinSnapshotPermission(ctx, a, "job.view", scope, func(tx pgx.Tx, p Principal) error {
		if err := CheckJobOperationalFilter(ctx, tx, p, f, scope); err != nil {
			return err
		}
		out.Filter = f
		var err error
		out.AsOf, out.Timezone, err = documentReportClock(ctx, tx, p.CompanyID)
		if err != nil {
			return err
		}
		where := ` FROM jobs j WHERE j.company_id=$1 AND j.deleted_at IS NULL
 AND ($3 OR EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.user_id=$2))
 AND ($4='' OR strpos(lower(j.job_type),lower($4))>0 OR strpos(lower(coalesce(j.subtitle,'')),lower($4))>0)
 AND ($5::bigint=0 OR j.status_id=$5) AND ($6::bigint=0 OR j.customer_id=$6)`
		args := []any{p.CompanyID, p.UserID, scope == All, f.Search, f.StatusID, f.CustomerID}
		if err := tx.QueryRow(ctx, `SELECT count(*)`+where, args...).Scan(&out.Total); err != nil {
			return err
		}
		args = append(args, limit, offset, p.Allows("customer.view", All), p.Allows("customer.archived.view", All),
			p.Allows("customer.view", Context), p.Allows("pricing.view", All), p.Allows("pricing.view", Assigned))
		// MATERIALIZED keeps per-row assignment arrays, Customer projection and
		// pricing work outside the candidate scan/sort and behind the SQL LIMIT.
		rows, err := tx.Query(ctx, `/* collection-page:job */ WITH page AS MATERIALIZED (
 SELECT j.id`+where+` ORDER BY j.start_time DESC,j.created_at DESC,j.id DESC LIMIT $7 OFFSET $8
 ) SELECT j.id,j.job_type,coalesce(j.subtitle,''),coalesce(c.id,0),coalesce(j.status_id,0),
 coalesce(c.display_name,''),coalesce(st.name,''),coalesce(st.category_key,''),coalesce(st.color,''),
 j.start_time,j.end_time,j.arrival_window_start,j.arrival_window_end,j.due_date,j.created_at,
 coalesce((SELECT array_agg(u.id ORDER BY u.id) FROM job_assignments a JOIN users u ON u.id=a.user_id AND u.company_id=j.company_id WHERE a.job_id=j.id),'{}'::bigint[]),
 coalesce((SELECT array_agg(u.name || CASE WHEN u.is_active THEN '' ELSE ' (unavailable)' END ORDER BY u.id) FROM job_assignments a JOIN users u ON u.id=a.user_id AND u.company_id=j.company_id WHERE a.job_id=j.id),'{}'::text[]),
 CASE WHEN $12 OR ($13 AND anchor.assigned) THEN j.billing_type END
 FROM page JOIN jobs j ON j.id=page.id LEFT JOIN statuses st ON st.id=j.status_id AND st.company_id=j.company_id
 CROSS JOIN LATERAL (SELECT EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.user_id=$2) assigned) anchor
 LEFT JOIN customers c ON c.id=j.customer_id AND c.company_id=j.company_id
 AND (($9 AND (c.deleted_at IS NULL OR $10)) OR ($11 AND c.deleted_at IS NULL AND anchor.assigned))
 ORDER BY j.start_time DESC,j.created_at DESC,j.id DESC`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Rows = []JobOperationalRow{}
		out.UIBillingTypes = map[int64]string{}
		for rows.Next() {
			var r JobOperationalRow
			var billing *string
			if err := rows.Scan(&r.ID, &r.Title, &r.Subtitle, &r.CustomerID, &r.StatusID, &r.Customer, &r.Status, &r.Category, &r.Color,
				&r.Start, &r.End, &r.ArrivalStart, &r.ArrivalEnd, &r.Due, &r.CreatedAt, &r.AssigneeIDs, &r.Assignees, &billing); err != nil {
				return err
			}
			r.Reference = JobOperationalReference{JobID: r.ID, CustomerID: r.CustomerID}
			out.Rows = append(out.Rows, r)
			if billing != nil {
				out.UIBillingTypes[r.ID] = *billing
			}
		}
		return rows.Err()
	})
	if err != nil {
		return JobOperationalPage{}, err
	}
	return out, nil
}
