package authorization

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// The dependency projection retains termination facts, including imported
// agreements. Scheduled/Active/Month-to-Month are never trusted as a clock.
const leaseCalendarColumns = `CASE WHEN EXISTS(SELECT 1 FROM lease_master_dependencies d WHERE d.lease_id=l.id AND d.lifecycle='terminated') THEN 'terminated' ELSE l.lifecycle END,coalesce(l.starts_on::text,''),coalesce(l.ends_on::text,''),lease_dependency_today(l.company_id)`

func readLeaseCalendar(ctx context.Context, tx pgx.Tx, id int64) (LeaseCalendar, error) {
	var state, start, end string
	var today time.Time
	if err := tx.QueryRow(ctx, `SELECT `+leaseCalendarColumns+` FROM leases l WHERE l.id=$1`, id).Scan(&state, &start, &end, &today); err != nil {
		return LeaseCalendar{}, err
	}
	return leaseCalendar(state, start, end, today)
}

// LeaseCalendar is a read-derived agreement calendar, not the rent due schedule.
// Renewal ends are inclusive. Empty renewal dates mean no current monthly term.
type LeaseCalendar struct {
	Lifecycle                      string
	RenewalStartsOn, RenewalEndsOn string
}

func leaseCalendar(state, startsOn, endsOn string, companyToday time.Time) (LeaseCalendar, error) {
	out := LeaseCalendar{Lifecycle: state}
	if state == "draft" || state == "terminated" {
		return out, nil
	}
	start, err := time.Parse(time.DateOnly, startsOn)
	if err != nil {
		return out, err
	}
	end, err := time.Parse(time.DateOnly, endsOn)
	if err != nil {
		return out, err
	}
	// Compare civil dates, never elapsed 24-hour durations across DST changes.
	y, m, d := companyToday.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	switch {
	case today.Before(start):
		out.Lifecycle = "scheduled"
	case !today.After(end):
		out.Lifecycle = "active"
	default:
		out.Lifecycle = "month_to_month"
		anchor := end.AddDate(0, 0, 1)
		months := (today.Year()-anchor.Year())*12 + int(today.Month()-anchor.Month())
		if leaseRenewalBoundary(anchor, months).After(today) {
			months--
		}
		out.RenewalStartsOn = leaseRenewalBoundary(anchor, months).Format(time.DateOnly)
		out.RenewalEndsOn = leaseRenewalBoundary(anchor, months+1).AddDate(0, 0, -1).Format(time.DateOnly)
	}
	return out, nil
}

// Always clamp from the original anchor so February never shifts March's day.
func leaseRenewalBoundary(anchor time.Time, months int) time.Time {
	first := time.Date(anchor.Year(), anchor.Month()+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	day := min(anchor.Day(), first.AddDate(0, 1, -1).Day())
	return first.AddDate(0, 0, day-1)
}
