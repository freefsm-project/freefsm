package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// TimeExportSnapshot consumes the export engine's fenced transaction, never a
// second pool snapshot. Export scope bounds rows before grouping and totals.
func TimeExportSnapshot(ctx context.Context, tx pgx.Tx, p Principal, filter TimesheetFilter, permission string, scope Scope) (Timesheet, error) {
	if err := AuthorizeTimeExport(p, permission, scope); err != nil {
		return Timesheet{}, err
	}
	return readTimesheet(ctx, tx, p, filter, permission, scope)
}

func AuthorizeTimeExport(p Principal, permission string, scope Scope) error {
	if permission != "time.export" && permission != "time.gps.export" || scope != Own && scope != Assigned && scope != All {
		return ErrForbidden
	}
	if !p.Allows(permission, scope) || !p.Allows("time.view", scope) {
		return ErrForbidden
	}
	if permission == "time.gps.export" && !p.Allows("time.gps.view", scope) {
		return ErrForbidden
	}
	return nil
}

func checkTimeExportAnchor(ctx context.Context, tx pgx.Tx, p Principal, subject, job int64, permission string, scope Scope) error {
	if err := AuthorizeTimeExport(p, permission, scope); err != nil {
		return err
	}
	if scope == Own && subject != p.UserID {
		return ErrForbidden
	}
	if scope == Assigned {
		if job == 0 {
			return ErrForbidden
		}
		ok, err := assignedJobAccess(ctx, tx, p.UserID, job, false)
		if err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
	}
	return nil
}

// TimeExportReference binds original scope anchors and visible historical Job
// titles. Moving an entry later never makes an old artifact newly accessible.
type TimeExportReference struct {
	EntryID, SubjectID, JobID int64
	TitleJobs                 []int64
}

func CheckTimeExportReference(ctx context.Context, tx pgx.Tx, p Principal, ref TimeExportReference, permission string, scope Scope) error {
	current, err := readTimeEntryAccess(ctx, tx, p, ref.EntryID, permission, false)
	if err != nil {
		return err
	}
	if current.UserID != ref.SubjectID {
		return ErrForbidden
	}
	if err := checkTimeExportAnchor(ctx, tx, p, current.UserID, current.JobID, permission, scope); err != nil {
		return err
	}
	if err := checkTimeExportAnchor(ctx, tx, p, ref.SubjectID, ref.JobID, permission, scope); err != nil {
		return err
	}
	if permission == "time.gps.export" && !current.CanGPS {
		return ErrForbidden
	}
	if ref.SubjectID != p.UserID {
		for _, job := range ref.TitleJobs {
			if _, err := parentAccess(ctx, tx, p, "job", job, "", false); err != nil {
				return err
			}
		}
	}
	return nil
}
