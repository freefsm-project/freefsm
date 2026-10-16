package handlers

import (
	"context"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"testing"
	"time"
)

func TestTimesheetsExactTotalsDoNotOverflowNanoseconds(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='UTC'`); err != nil {
		t.Fatal(err)
	}
	other := client.User.Create().SetCompanyID(company).SetEmail("elapsed-total@test").SetName("Other retained subject").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	// Each retained interval is 73,049 Gregorian days. Their combined 400
	// subject-years exceed a nanosecond int64 even though each row fits it.
	for _, id := range []int64{admin, other.ID} {
		client.TimeEntry.Create().SetCompanyID(company).SetUserID(id).SetClockIn(time.Date(1826, 1, 1, 0, 0, 0, 0, time.UTC)).SetClockOut(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)).SaveX(ctx)
	}
	report, err := authorization.New(pool).Timesheet(ctx, authorization.Actor{UserID: admin}, authorization.TimesheetFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Total.String(); got != "3506352h0m0s" {
		t.Fatalf("exact multi-subject elapsed total=%s", got)
	}
	for _, group := range report.Groups {
		if got := group.Total.String(); got != "1753176h0m0s" {
			t.Fatalf("exact subject total=%s", got)
		}
	}
}
