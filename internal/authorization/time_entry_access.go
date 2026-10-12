package authorization

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// TimeEntryView is a bounded subject projection, not directory access. Own is
// anchored to the recorded subject; Assigned requires a current active Job.
type TimeEntryView struct {
	UserID, JobID               int64
	SubjectName                 string
	CanCorrect, CanGPS, CanVoid bool
	correctionScope             Scope
}

func timeEntryGrant(p Principal, permission string, own, assigned bool) Scope {
	if p.Allows(permission, All) {
		return All
	}
	if own && p.Allows(permission, Own) {
		return Own
	}
	if assigned && p.Allows(permission, Assigned) {
		return Assigned
	}
	return ""
}

func readTimeEntryAccess(ctx context.Context, q queryer, p Principal, id int64, permission string, locking bool) (TimeEntryView, error) {
	var view TimeEntryView
	var company int64
	var sameJobCompany bool
	lock := ""
	if locking {
		lock = " FOR UPDATE OF e"
	}
	err := q.QueryRow(ctx, `SELECT coalesce(e.company_id,0),e.user_id,coalesce(e.job_id,0),coalesce(u.name,''),coalesce(j.company_id=e.company_id,false)
 FROM time_entries e LEFT JOIN users u ON u.id=e.user_id AND u.company_id=e.company_id LEFT JOIN jobs j ON j.id=e.job_id WHERE e.id=$1`+lock, id).Scan(&company, &view.UserID, &view.JobID, &view.SubjectName, &sameJobCompany)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && company != p.CompanyID {
		return view, ErrNotFound
	}
	if err != nil {
		return view, err
	}
	assigned := false
	if view.JobID > 0 && sameJobCompany {
		assigned, err = assignedJobAccess(ctx, q, p.UserID, view.JobID, locking)
		if err != nil {
			return view, err
		}
	}
	own := view.UserID == p.UserID
	if timeEntryGrant(p, "time.view", own, assigned) == "" {
		return view, ErrNotFound
	}
	if permission != "" && timeEntryGrant(p, permission, own, assigned) == "" {
		return view, ErrForbidden
	}
	view.correctionScope = timeEntryGrant(p, "time.correct", own, assigned)
	view.CanCorrect = view.correctionScope != ""
	view.CanGPS = timeEntryGrant(p, "time.gps.view", own, assigned) != ""
	view.CanVoid = timeEntryGrant(p, "time.void", own, assigned) != ""
	return view, nil
}

func (s *Service) TimeEntryAccess(ctx context.Context, a Actor, id int64, permission string) (TimeEntryView, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return TimeEntryView{}, err
	}
	return readTimeEntryAccess(ctx, s.Pool, p, id, permission, false)
}

type TimeEntryCorrection struct {
	ClockIn, ClockOut *time.Time
	Notes             *string
	JobID             *int64
	ClearJob          bool
}

// CorrectTimeEntry adapts the existing correction fields to current A/J/O
// authority. Subject identity/GPS and lifecycle operations are not changed.
func (s *Service) CorrectTimeEntry(ctx context.Context, a Actor, id int64, patch TimeEntryCorrection) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		before, err := readTimeEntryAccess(ctx, tx, p, id, "time.correct", true)
		if err != nil {
			return err
		}
		changeJob := patch.ClearJob || patch.JobID != nil
		var target *int64
		if !patch.ClearJob {
			target = patch.JobID
		}
		if changeJob && target != nil {
			if _, err := parentAccess(ctx, tx, p, "job", *target, "", true); err != nil {
				return err
			}
			var active bool
			if err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM jobs WHERE id=$1 AND company_id=$2`, *target, p.CompanyID).Scan(&active); err != nil {
				return err
			}
			if !active {
				return ErrForbidden
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE time_entries SET clock_in=coalesce($2,clock_in),clock_out=coalesce($3,clock_out),notes=coalesce($4,notes),job_id=CASE WHEN $5 THEN $6::bigint ELSE job_id END,is_manual=true,updated_at=clock_timestamp() WHERE id=$1`, id, patch.ClockIn, patch.ClockOut, patch.Notes, changeJob, target); err != nil {
			return err
		}
		if _, err := readTimeEntryAccess(ctx, tx, p, id, "time.correct", true); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrForbidden
			}
			return err
		}
		return audit(ctx, tx, a, p, "time.correct", before.correctionScope, "time_entry", id, map[string]any{"subject_user_id": before.UserID, "job_changed": changeJob})
	})
}
