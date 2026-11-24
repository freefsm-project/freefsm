package services

import (
	"context"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Test-only bridge lets the integration fixture use the production migrations
// without introducing a services -> database -> services import cycle.
func DashboardPaymentTotalsForTest(ctx context.Context, pool *pgxpool.Pool, companyID int64, now time.Time) (int64, int64, bool, error) {
	return (&DashboardService{db: pool}).paymentTotals(ctx, companyID, dashboardPaymentPeriod(now))
}

func TestDashboardFinancialQueriesPropagateDatabaseErrors(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres:///freefsm_dashboard_closed")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	svc := &DashboardService{db: pool}
	ctx := authorization.WithActor(context.Background(), authorization.New(pool), authorization.Actor{UserID: 1})

	if _, _, _, err = svc.paymentTotals(ctx, 1, dashboardPeriod{}); err == nil {
		t.Fatal("payment totals unexpectedly ignored closed pool")
	}
	if _, err = svc.invoiceSettledTotals(context.Background(), 1); err == nil {
		t.Fatal("invoice totals unexpectedly ignored closed pool")
	}
}

func TestDashboardPaymentPeriodCompanyCalendar(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		date  string
		hours int
	}{{"2026-03-08", 23}, {"2026-11-01", 25}, {"2026-12-31", 24}} {
		now, err := time.ParseInLocation("2006-01-02", tc.date, loc)
		if err != nil {
			t.Fatal(err)
		}
		period := dashboardPaymentPeriod(now.Add(12 * time.Hour))
		if got := period.tomorrowStart.Sub(period.todayStart); got != time.Duration(tc.hours)*time.Hour {
			t.Errorf("%s day length = %s", tc.date, got)
		}
		wantNext := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, loc)
		if period.monthStart.Day() != 1 || !period.nextMonthStart.Equal(wantNext) || period.todayStart.Hour() != 0 || period.tomorrowStart.Hour() != 0 {
			t.Errorf("invalid local calendar bounds: %+v", period)
		}
		if tc.date == "2026-12-31" && period.tomorrowStart.Format("2006-01-02") != "2027-01-01" {
			t.Errorf("year rollover: %s", period.tomorrowStart)
		}
	}
}
