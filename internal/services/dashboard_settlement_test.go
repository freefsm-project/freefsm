package services

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDashboardFinancialQueriesPropagateDatabaseErrors(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres:///freefsm_dashboard_closed")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	svc := &DashboardService{db: pool}

	if _, _, err = svc.paymentTotals(context.Background(), 1, dashboardPeriod{}); err == nil {
		t.Fatal("payment totals unexpectedly ignored closed pool")
	}
	if _, err = svc.invoiceSettledTotals(context.Background(), 1); err == nil {
		t.Fatal("invoice totals unexpectedly ignored closed pool")
	}
}
