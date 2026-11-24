package services_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDashboardRecordedNetPaymentsBackdatingAndLaterReversals(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL dashboard tests")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := "dashboard_cash_" + uuid.NewString()[:8]
	if _, err := root.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var company, customer, actor int64
	if err := pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`).Scan(&company); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Dashboard cash') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'dashboard-cash@test','Dashboard cash','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin') RETURNING id`, company).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	ctx = authorization.WithActor(ctx, auth, authorization.Actor{UserID: actor})
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	instant := func(date string) time.Time {
		t.Helper()
		v, err := time.ParseInLocation("2006-01-02 15:04", date, loc)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	payment, credit, refund := uuid.NewString(), uuid.NewString(), uuid.NewString()
	// Immutable fixture facts retain deliberately different effective/entry dates.
	// The receipt is entirely unapplied; Credit creation is not another cash event.
	exec(`INSERT INTO invoice_payments(id,company_id,customer_id,amount_cents,method,received_date,actor_id,created_at) VALUES($1,$2,$3,10001,'cash','2026-02-01',$4,$5)`, payment, company, customer, actor, instant("2026-03-08 00:00"))
	exec(`INSERT INTO customer_credits(id,company_id,customer_id,source_payment_id,original_amount_cents,source_date) VALUES($1,$2,$3,$4,10001,'2026-02-01')`, credit, company, customer, payment)
	exec(`INSERT INTO credit_refunds(id,company_id,customer_id,amount_cents,method,effective_date,reason,actor_id,created_at) VALUES($1,$2,$3,2000,'cash','2026-02-02','fixture',$4,$5)`, refund, company, customer, actor, instant("2026-03-09 00:00"))
	exec(`INSERT INTO credit_refund_allocations(refund_id,credit_id,amount_cents) VALUES($1,$2,2000)`, refund, credit)
	exec(`INSERT INTO settlement_reversals(id,company_id,operation_type,operation_id,reason,actor_id,effective_date,created_at) VALUES($1,$2,'credit_refund',$3,'fixture',$4,'2026-02-03',$5)`, uuid.NewString(), company, refund, actor, instant("2026-03-10 00:00"))
	exec(`INSERT INTO settlement_reversals(id,company_id,operation_type,operation_id,reason,actor_id,effective_date,created_at) VALUES($1,$2,'payment',$3,'fixture',$4,'2026-02-04',$5)`, uuid.NewString(), company, payment, actor, instant("2026-04-01 00:00"))
	for _, tc := range []struct {
		date         string
		month, today int64
	}{
		{"2026-02-01 12:00", 0, 0},
		{"2026-03-08 12:00", 10001, 10001},
		{"2026-03-09 12:00", 10001, -2000},
		{"2026-03-10 12:00", 10001, 2000},
		{"2026-03-31 12:00", 10001, 0},
		{"2026-04-01 12:00", -10001, -10001},
	} {
		t.Run(tc.date, func(t *testing.T) {
			month, today, unknown, err := services.DashboardPaymentTotalsForTest(ctx, pool, company, instant(tc.date))
			if err != nil {
				t.Fatal(err)
			}
			if month != tc.month || today != tc.today || unknown {
				t.Fatalf("got month=%d today=%d unknown=%t; want %d/%d/false", month, today, unknown, tc.month, tc.today)
			}
		})
	}
	grants := []authorization.Grant{}
	for _, permission := range []string{"customer.view", "customer.financial.view", "job.view", "invoice.view", "pricing.view", "settlement.view"} {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.All})
	}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: actor}, 0, "Dashboard finance", grants)
	if err != nil {
		t.Fatal(err)
	}
	var reader, archived int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,'dashboard-reader@test','Reader','unused','tech',$2) RETURNING id`, company, role).Scan(&reader); err != nil {
		t.Fatal(err)
	}
	readerCtx := authorization.WithActor(ctx, auth, authorization.Actor{UserID: reader})
	cached, err := auth.Principal(readerCtx, authorization.Actor{UserID: reader})
	if err != nil {
		t.Fatal(err)
	}
	readerCtx = authorization.WithNavigationReads(readerCtx, cached)
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name,deleted_at) VALUES($1,'Archived cash',now()) RETURNING id`, company).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO invoice_payments(id,company_id,customer_id,amount_cents,method,received_date,created_at) VALUES($1,$2,$3,900,'cash','2026-03-08',$4),($5,$2,$3,800,'cash','2026-03-08',NULL)`, uuid.NewString(), company, archived, instant("2026-03-08 00:00"), uuid.NewString())
	t.Run("archived population and unknown disclosure", func(t *testing.T) {
		month, today, unknown, err := services.DashboardPaymentTotalsForTest(readerCtx, pool, company, instant("2026-03-08 12:00"))
		if err != nil || month != 10001 || today != 10001 || unknown {
			t.Fatalf("archived population leaked: %d/%d/%t %v", month, today, unknown, err)
		}
		month, today, unknown, err = services.DashboardPaymentTotalsForTest(ctx, pool, company, instant("2026-03-08 12:00"))
		if err != nil || month != 10901 || today != 10901 || !unknown {
			t.Fatalf("authorized archived history absent: %d/%d/%t %v", month, today, unknown, err)
		}
	})
	// Wait for a real PostgreSQL lock dependency, not an assumed goroutine order.
	waitBlocked := func(waitCtx context.Context, pid int) {
		t.Helper()
		for {
			var waiting bool
			if err := pool.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				return
			}
		}
	}
	t.Run("concurrent cash uses one snapshot", func(t *testing.T) {
		requestCtx, cancel := context.WithTimeout(readerCtx, 10*time.Second)
		defer cancel()
		tx, err := pool.Begin(requestCtx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(requestCtx, `LOCK TABLE invoice_payments IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		var pid int
		if err := tx.QueryRow(requestCtx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		type result struct {
			month, today int64
			unknown      bool
			err          error
		}
		done := make(chan result, 1)
		go func() {
			m, d, u, e := services.DashboardPaymentTotalsForTest(requestCtx, pool, company, instant("2026-05-01 12:00"))
			done <- result{m, d, u, e}
		}()
		waitBlocked(requestCtx, pid)
		if _, err := tx.Exec(requestCtx, `INSERT INTO invoice_payments(id,company_id,customer_id,amount_cents,method,received_date,created_at) VALUES($1,$2,$3,123,'cash','2026-05-01',$4),($5,$2,$3,456,'cash','2026-05-01',NULL)`, uuid.NewString(), company, customer, instant("2026-05-01 00:00"), uuid.NewString()); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(requestCtx); err != nil {
			t.Fatal(err)
		}
		got := <-done
		if got.err != nil || got.month != 0 || got.today != 0 || got.unknown {
			t.Fatalf("mixed snapshot: %+v", got)
		}
		m, d, u, e := services.DashboardPaymentTotalsForTest(readerCtx, pool, company, instant("2026-05-01 12:00"))
		if e != nil || m != 123 || d != 123 || !u {
			t.Fatalf("fresh snapshot: %d/%d/%t %v", m, d, u, e)
		}
	})
	t.Run("revocation while waiting on authority fence", func(t *testing.T) {
		requestCtx, cancel := context.WithTimeout(readerCtx, 10*time.Second)
		defer cancel()
		tx, err := pool.Begin(requestCtx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(requestCtx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='settlement.view'`, role); err != nil {
			t.Fatal(err)
		}
		var pid int
		if err := tx.QueryRow(requestCtx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			m, d, u, e := services.DashboardPaymentTotalsForTest(requestCtx, pool, company, instant("2026-03-08 12:00"))
			if m != 0 || d != 0 || u {
				done <- errors.New("revoked request returned cash data")
				return
			}
			done <- e
		}()
		waitBlocked(requestCtx, pid)
		if err := tx.Commit(requestCtx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("revoked request: %v", err)
		}
	})
	// Legacy unknown entry evidence cannot be attributed using received_date.
	exec(`INSERT INTO invoice_payments(id,company_id,customer_id,amount_cents,method,received_date,created_at) VALUES($1,$2,$3,777,'cash','2026-03-08',NULL)`, uuid.NewString(), company, customer)
	month, today, unknown, err := services.DashboardPaymentTotalsForTest(ctx, pool, company, instant("2026-03-08 12:00"))
	if err != nil {
		t.Fatal(err)
	}
	if month != 10901 || today != 10901 || !unknown {
		t.Fatalf("unknown entry incorrectly attributed: month=%d today=%d unknown=%t", month, today, unknown)
	}
}
