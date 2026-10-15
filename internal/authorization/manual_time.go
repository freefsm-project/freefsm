package authorization

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type ManualTimeEntry struct {
	UserID, JobID     int64
	ClockIn, ClockOut *time.Time
	Notes             string
}

type TimeSubject struct {
	ID   int64
	Name string
}

// The selector grants only identities needed to choose an authorized subject,
// never directory, Role, credentials or another user's time history.
func (s *Service) ManualTimeSubjects(ctx context.Context, a Actor) ([]TimeSubject, error) {
	var result []TimeSubject
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		broad := p.Allows("time.create", All) || p.Allows("time.create", Assigned)
		if !broad && !p.Allows("time.create", Own) {
			return ErrForbidden
		}
		rows, err := tx.Query(ctx, `SELECT id,name FROM users WHERE company_id=$1 AND ($2 OR id=$3) ORDER BY name,id`, p.CompanyID, broad, p.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v TimeSubject
			if err := rows.Scan(&v.ID, &v.Name); err != nil {
				return err
			}
			result = append(result, v)
		}
		return rows.Err()
	})
	return result, err
}

func (s *Service) CreateManualTimeEntry(ctx context.Context, a Actor, in ManualTimeEntry) (int64, error) {
	var id int64
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" {
			return ErrForbidden
		}
		assigned := false
		if in.JobID > 0 {
			if _, err := parentAccess(ctx, tx, p, "job", in.JobID, "", true); err != nil {
				return err
			}
			var active bool
			if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM jobs WHERE id=$1 AND company_id=$2`, in.JobID, p.CompanyID).Scan(&active); err != nil {
				return err
			}
			if !active {
				return ErrForbidden
			}
			var err error
			assigned, err = assignedJobAccess(ctx, tx, p.UserID, in.JobID, true)
			if err != nil {
				return err
			}
		}
		scope := timeEntryGrant(p, "time.create", p.UserID == in.UserID, assigned)
		if scope == "" {
			return ErrForbidden
		}
		var subject bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND company_id=$2)`, in.UserID, p.CompanyID).Scan(&subject); err != nil {
			return err
		}
		if !subject {
			return ErrNotFound
		}
		if err := checkTimeSave(ctx, tx, a, 0); err != nil {
			return err
		}
		now := time.Now().UTC()
		if in.ClockIn == nil || in.ClockOut == nil || !in.ClockIn.Before(*in.ClockOut) || in.ClockOut.After(now) || in.JobID < 0 || in.ClockIn.Nanosecond()%1000 != 0 || in.ClockOut.Nanosecond()%1000 != 0 {
			return &ClockConflict{"time_order"}
		}
		if err := checkTimeInterval(ctx, tx, in.UserID, 0, *in.ClockIn, in.ClockOut); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO time_entries(company_id,user_id,job_id,is_manual,clock_in,clock_out,notes,created_at,updated_at) VALUES($1,$2,nullif($3,0),true,$4,$5,$6,$7,$7) RETURNING id`, p.CompanyID, in.UserID, in.JobID, in.ClockIn, in.ClockOut, in.Notes, now).Scan(&id); err != nil {
			return err
		}
		if err := recordTimeHistory(ctx, tx, p.UserID, id, "manual_created", "", nil, now); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "time.create", scope, "time_entry", id, map[string]any{"subject_user_id": in.UserID, "clock_in": in.ClockIn, "clock_out": in.ClockOut})
	})
	return id, err
}
