package authorization

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ScheduleRecord is an operational allowlist, never a master or pricing entity.
// All views consume the same fenced snapshot, including conflict measures.
type ScheduleRecord struct {
	InvoiceID                                                   int64
	InvoiceStatus, InvoiceColor                                 string
	ID                                                          int64
	Title, Customer, Location, Address, Status, Category, Color string
	Start, End, ArrivalStart, ArrivalEnd, Due                   *time.Time
	AssigneeIDs                                                 []int64
	Assignees                                                   []string
	Latitude, Longitude                                         *float64
	HasLocation, Conflict                                       bool
}

type ScheduleSnapshot struct {
	Jobs      []ScheduleRecord
	Directory []DispatchUser
}

func (s *Service) Schedule(ctx context.Context, a Actor, start, end time.Time) (ScheduleSnapshot, error) {
	var out ScheduleSnapshot
	err := s.WithinSnapshotPermission(ctx, a, "job.view", Assigned, func(tx pgx.Tx, p Principal) error {
		rows, err := tx.Query(ctx, `WITH visible AS MATERIALIZED (
 SELECT j.*, EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.user_id=$2) AS assigned
 FROM jobs j WHERE j.company_id=$1 AND j.deleted_at IS NULL
 AND ($3 OR EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.user_id=$2))
 ) SELECT j.id,j.job_type,coalesce(c.display_name,''),coalesce(l.title,''),
 coalesce(concat_ws(', ',nullif(l.address_1,''),nullif(l.address_2,''),nullif(l.city,''),nullif(l.state,''),nullif(l.zip_code,'')),''),
 coalesce(st.name,''),coalesce(st.category_key,''),coalesce(st.color,''),
 j.start_time,j.end_time,j.arrival_window_start,j.arrival_window_end,j.due_date,
 coalesce((SELECT array_agg(a.user_id ORDER BY a.user_id) FROM job_assignments a WHERE a.job_id=j.id),'{}'::bigint[]),
 coalesce((SELECT array_agg(u.name || CASE WHEN u.is_active THEN '' ELSE ' (unavailable)' END ORDER BY a.user_id) FROM job_assignments a JOIN users u ON u.id=a.user_id AND u.company_id=j.company_id WHERE a.job_id=j.id),'{}'::text[]),
 l.latitude,l.longitude,j.location_id IS NOT NULL,
 EXISTS(SELECT 1 FROM visible other WHERE other.id<>j.id AND other.start_time<j.end_time AND other.end_time>j.start_time
 AND EXISTS(SELECT 1 FROM job_assignments x JOIN job_assignments y ON y.user_id=x.user_id WHERE x.job_id=j.id AND y.job_id=other.id)),
 coalesce(inv.id,0),coalesce(inv.name,'Invoice'),coalesce(inv.color,'')
 FROM visible j
 LEFT JOIN customers c ON c.id=j.customer_id AND c.company_id=j.company_id
 AND (($6 AND (c.deleted_at IS NULL OR $7)) OR ($8 AND j.assigned AND c.deleted_at IS NULL))
 LEFT JOIN locations l ON l.id=j.location_id AND l.company_id=j.company_id AND l.object_type='customer' AND l.object_id=j.customer_id
 AND (($9 AND (l.deleted_at IS NULL OR $10)) OR ($11 AND j.assigned AND l.deleted_at IS NULL))
 LEFT JOIN statuses st ON st.id=j.status_id AND st.company_id=j.company_id
 LEFT JOIN LATERAL (
 SELECT i.id,ist.name,ist.color FROM invoices i
 LEFT JOIN invoice_effective_status effective ON effective.invoice_id=i.id
 LEFT JOIN statuses ist ON ist.id=effective.status_id
 WHERE i.job_id=j.id AND i.company_id=j.company_id AND i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL
 AND ($12 OR ($13 AND j.assigned)) ORDER BY i.created_at DESC,i.id DESC LIMIT 1
 ) inv ON true
 WHERE (j.start_time<$5 AND j.end_time>$4) OR j.start_time IS NULL OR j.end_time IS NULL OR j.end_time<=j.start_time
 OR (j.arrival_window_start IS NULL)<>(j.arrival_window_end IS NULL)
 OR j.arrival_window_start>j.start_time OR j.arrival_window_end<j.start_time
 ORDER BY j.start_time NULLS LAST,j.id`, p.CompanyID, p.UserID, p.Allows("job.view", All), start, end,
			p.Allows("customer.view", All), p.Allows("customer.archived.view", All), p.Allows("customer.view", Context),
			p.Allows("location.view", All), p.Allows("location.archived.view", All), p.Allows("location.view", Context), p.Allows("invoice.view", All), p.Allows("invoice.view", Assigned))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ScheduleRecord
			if err := rows.Scan(&r.ID, &r.Title, &r.Customer, &r.Location, &r.Address, &r.Status, &r.Category, &r.Color, &r.Start, &r.End, &r.ArrivalStart, &r.ArrivalEnd, &r.Due, &r.AssigneeIDs, &r.Assignees, &r.Latitude, &r.Longitude, &r.HasLocation, &r.Conflict, &r.InvoiceID, &r.InvoiceStatus, &r.InvoiceColor); err != nil {
				return err
			}
			out.Jobs = append(out.Jobs, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if p.Allows("directory.view", All) {
			users, err := tx.Query(ctx, `SELECT id,name FROM users WHERE company_id=$1 AND is_active ORDER BY name,id`, p.CompanyID)
			if err != nil {
				return err
			}
			defer users.Close()
			for users.Next() {
				var user DispatchUser
				if err := users.Scan(&user.ID, &user.Name); err != nil {
					return err
				}
				out.Directory = append(out.Directory, user)
			}
			return users.Err()
		}
		return nil
	})
	if err != nil {
		return ScheduleSnapshot{}, err
	}
	return out, nil
}
