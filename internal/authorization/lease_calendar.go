package authorization

import "time"

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
