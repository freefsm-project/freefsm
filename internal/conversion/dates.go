package conversion

import (
	"fmt"
	"strconv"
	"time"
)

func invoiceDates(now time.Time, timezone string, dueDays *int, dueOverride *string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	date, _ := time.Parse("2006-01-02", now.In(loc).Format("2006-01-02"))
	due := time.Time{}
	if dueDays != nil {
		due = date.AddDate(0, 0, *dueDays)
	}
	if dueOverride != nil {
		due = time.Time{}
		if *dueOverride != "" {
			days, err := strconv.ParseInt(*dueOverride, 10, 32)
			if err != nil {
				return date, due, fmt.Errorf("invalid Customer due offset")
			}
			due = date.AddDate(0, 0, int(days))
		}
	}
	if !due.IsZero() && (due.Year() < 1 || due.Year() > 9999) {
		return date, due, fmt.Errorf("due offset produces an unsupported date")
	}
	return date, due, nil
}
