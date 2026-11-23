package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOperationalListExportAtomicFailuresHTTP(t *testing.T) {
	for _, output := range workReportExportCases {
		for _, fault := range []string{"accept-audit", "generate-audit", "second-row-storage", "download-audit", "transfer-storage"} {
			t.Run(output.name+"/"+fault, func(t *testing.T) {
				pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				cookie := sessionCookie(t, ctx, sessions, admin)
				customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Failure customer").SaveX(ctx)
				for i := range 2 {
					client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Never partial report").SaveX(ctx)
					client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Never partial report").SaveX(ctx)
					client.Invoice.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetInvoiceNumber(int64(980 + i)).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetTitle("Never partial report").SaveX(ctx)
				}
				values := url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}
				path := ""
				if fault != "accept-audit" {
					path = masterPost(t, router, cookie, output.route, values, 303)
				}
				if fault == "download-audit" || fault == "transfer-storage" {
					if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
						t.Fatal(err)
					}
				}
				table, condition := "authorization_audit", "NEW.target_type='export' AND NEW.detail->>'event'='accepted'"
				switch fault {
				case "generate-audit":
					condition = "NEW.target_type='export' AND NEW.detail->>'event'='generated'"
				case "download-audit":
					condition = "NEW.target_type='export' AND NEW.detail->>'event'='downloaded'"
				case "second-row-storage":
					table, condition = "export_records", "NEW.ordinal=2"
				case "transfer-storage":
					condition = "false"
				}
				if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_report_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RAISE EXCEPTION 'private injected report failure' USING ERRCODE='58030'; END IF; RETURN NEW;END $$; CREATE TRIGGER fail_report_write BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION fail_report_write()`); err != nil {
					t.Fatal(err)
				}
				switch fault {
				case "accept-audit":
					r := httptest.NewRequest("POST", output.route, strings.NewReader(values.Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(cookie)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, r)
					if w.Code != 503 {
						t.Fatalf("acceptance storage error=%d body=%s", w.Code, w.Body.String())
					}
					assertContains(t, w.Body.String(), values.Get("operation_id"))
					assertNotContains(t, w.Body.String(), "private injected report failure")
					var n int
					if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs WHERE operation_id=$1`, values.Get("operation_id")).Scan(&n); err != nil || n != 0 {
						t.Fatalf("failed acceptance persisted %d jobs: %v", n, err)
					}
				case "download-audit", "transfer-storage":
					tmp := os.Getenv("TMPDIR")
					if fault == "transfer-storage" {
						t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing-transfer-directory"))
					}
					body := requestBody(t, router, cookie, "GET", path+"/download", 503)
					if fault == "transfer-storage" {
						t.Setenv("TMPDIR", tmp)
					}
					assertNotContains(t, body, "Never partial report")
					assertNotContains(t, body, "private injected report failure")
					var n int
					if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='downloaded'`).Scan(&n); err != nil || n != 0 {
						t.Fatalf("failed download audited %d: %v", n, err)
					}
				default:
					if _, err := exports.Product(pool).ProcessOne(ctx); err == nil {
						t.Fatal("injected publication failure succeeded")
					}
					status := requestBody(t, router, cookie, "GET", path, 200)
					assertContains(t, status, "Failed")
					assertNotContains(t, status, "private injected report failure")
					assertNotContains(t, requestBody(t, router, cookie, "GET", path+"/download", 409), "Never partial report")
					var records, generated int
					if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM export_records),(SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='generated')`).Scan(&records, &generated); err != nil || records != 0 || generated != 0 {
						t.Fatalf("partial publication: records=%d audit=%d err=%v", records, generated, err)
					}
				}
				if _, err := pool.Exec(ctx, `DROP TRIGGER fail_report_write ON `+table); err != nil {
					t.Fatal(err)
				}
				// Only unaccepted intent is retryable with the same operation ID.
				if fault != "download-audit" && fault != "transfer-storage" {
					if fault != "accept-audit" {
						values.Set("operation_id", newOperationKey())
					}
					path = masterPost(t, router, cookie, output.route, values, 303)
					if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
						t.Fatal(err)
					}
				}
				body := requestBody(t, router, cookie, "GET", path+"/download", 200)
				if rows := timeExportCSV(t, body); len(rows) != 2 {
					t.Fatalf("recovered artifact rows=%v", rows)
				}
				if output.name != "cohort" {
					assertContains(t, body, "Never partial report")
				}
			})
		}
	}
}

func TestOperationalListExportInterruptedWorkerRestartReplayHTTP(t *testing.T) {
	for _, output := range workReportExportCases {
		t.Run(output.name, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Restart customer").SaveX(ctx)
			client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Durable report intent").SaveX(ctx)
			client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Durable report intent").SaveX(ctx)
			client.Invoice.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetInvoiceNumber(980).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetTitle("Durable report intent").SaveX(ctx)
			values := url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}
			path := masterPost(t, router, cookie, output.route, values, 303)
			if replay := masterPost(t, router, cookie, output.route, values, 303); replay != path {
				t.Fatal("same intent created a new job")
			}
			values.Set("customer_id", fmt.Sprint(customer.ID))
			masterPost(t, router, cookie, output.route, values, 409)
			values.Del("customer_id")
			holder, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(context.Background())
			if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(100199)`); err != nil {
				t.Fatal(err)
			}
			var blocker int32
			if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `CREATE FUNCTION hold_report_restart() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(100199);RETURN NEW;END $$; CREATE TRIGGER hold_report_restart BEFORE INSERT ON export_records FOR EACH ROW WHEN(NEW.ordinal=1) EXECUTE FUNCTION hold_report_restart()`); err != nil {
				t.Fatal(err)
			}
			workerCtx, stopWorker := context.WithCancel(ctx)
			defer stopWorker()
			done := make(chan error, 1)
			go func() { _, err := exports.Product(pool).ProcessOne(workerCtx); done <- err }()
			timeExportDMLWaiter(t, ctx, pool, blocker, 100199, "INSERT INTO export_records")
			stopWorker()
			if err := <-done; err == nil {
				t.Fatal("interrupted worker succeeded")
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var state string
			var n int
			if err := pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM export_records WHERE export_id=$1) FROM export_jobs WHERE id=$1`, strings.TrimPrefix(path, "/exports/")).Scan(&state, &n); err != nil || state != "Running" || n != 0 {
				t.Fatalf("interrupted durable state=%s records=%d: %v", state, n, err)
			}
			// A fresh service instance recovers durable Running work; no fixture
			// state edit or in-memory retry queue supplies the interrupted intent.
			restarted, err := pgxpool.NewWithConfig(ctx, pool.Config().Copy())
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if work, err := exports.Product(restarted).ProcessOne(ctx); err != nil || !work {
				t.Fatalf("restart failed: work=%v err=%v", work, err)
			}
			if replay := masterPost(t, router, cookie, output.route, values, 303); replay != path {
				t.Fatal("ready replay duplicated intent")
			}
			body := requestBody(t, router, cookie, "GET", path+"/download", 200)
			if rows := timeExportCSV(t, body); len(rows) != 1 {
				t.Fatalf("restarted artifact rows=%v", rows)
			}
			if output.name != "cohort" {
				assertContains(t, body, "Durable report intent")
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='generated'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("restart generation audits=%d err=%v", n, err)
			}
			if work, err := exports.Product(pool).ProcessOne(ctx); err != nil || work {
				t.Fatalf("replay queued additional work=%v err=%v", work, err)
			}
		})
	}
}

func TestWorkloadAndEstimateCohortQueryFailureIsNotZeroHTTP(t *testing.T) {
	for _, report := range []struct{ route, table, filter string }{
		{"/reports/workload", "jobs", "schedule_from"},
		{"/reports/estimate-cohort", "estimates", "created_from"},
	} {
		t.Run(report.table, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Query failure source").SaveX(ctx)
			client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Available query row").SaveX(ctx)
			client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Available query row").SaveX(ctx)
			values := url.Values{"scope": {"A"}, report.filter: {"2020-01-01"}}
			assertContains(t, requestBody(t, router, cookie, "GET", report.route+"?"+values.Encode(), 200), "Available query row")
			values.Set("operation_id", newOperationKey())
			path := masterPost(t, router, cookie, report.route+"/export", values, 303)
			// This schema belongs exclusively to permissionHTTPFixture. Renaming
			// the source forces the real selector to fail, not return empty rows.
			if _, err := pool.Exec(ctx, `ALTER TABLE `+report.table+` RENAME TO unavailable_report_source`); err != nil {
				t.Fatal(err)
			}
			restored := false
			defer func() {
				if !restored {
					_, _ = pool.Exec(context.Background(), `ALTER TABLE unavailable_report_source RENAME TO `+report.table)
				}
			}()
			body := requestBody(t, router, cookie, "GET", report.route+"?"+values.Encode(), 503)
			assertNotContains(t, body, "Available query row")
			assertNotContains(t, body, "No matching")
			assertNotContains(t, body, "does not exist")
			assertContains(t, body, "2020-01-01")
			if _, err := exports.Product(pool).ProcessOne(ctx); err == nil {
				t.Fatal("failed selector published a successful zero")
			}
			var state string
			var records int
			if err := pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM export_records WHERE export_id=$1) FROM export_jobs WHERE id=$1`, strings.TrimPrefix(path, "/exports/")).Scan(&state, &records); err != nil || state != "Failed" || records != 0 {
				t.Fatalf("query failure state=%s records=%d err=%v", state, records, err)
			}
			if _, err := pool.Exec(ctx, `ALTER TABLE unavailable_report_source RENAME TO `+report.table); err != nil {
				t.Fatal(err)
			}
			restored = true
			assertContains(t, requestBody(t, router, cookie, "GET", report.route+"?"+values.Encode(), 200), "Available query row")
		})
	}
}

func TestOperationalListExportRowIterationFailureHTTP(t *testing.T) {
	for _, output := range workReportExportCases {
		t.Run(output.name, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Iteration source").SaveX(ctx)
			for i := range 2 {
				client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Iteration must not publish").SaveX(ctx)
				client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Iteration must not publish").SaveX(ctx)
				client.Invoice.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetInvoiceNumber(int64(990 + i)).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetTitle("Iteration must not publish").SaveX(ctx)
			}
			path := masterPost(t, router, cookie, output.route, url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}, 303)
			table, column := output.permission+"s", "title"
			if output.permission == "job" {
				column = "job_type"
			}
			// A view of the same real migrated source fails while evaluating its
			// second row. The nontransactional sequence witnesses that the real
			// selector consumed a successful row before the server-side failure.
			var projection string
			if err := pool.QueryRow(ctx, `SELECT string_agg(CASE WHEN attname=$2 THEN format('fail_report_row(%I) AS %I',attname,attname) ELSE quote_ident(attname) END,',' ORDER BY attnum) FROM pg_attribute WHERE attrelid=$1::regclass AND attnum>0 AND NOT attisdropped`, table, column).Scan(&projection); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `CREATE SEQUENCE report_rows_seen; CREATE FUNCTION fail_report_row(value text) RETURNS text LANGUAGE plpgsql VOLATILE AS $$ BEGIN IF nextval('report_rows_seen')=2 THEN RAISE EXCEPTION 'private iteration failure' USING ERRCODE='58030'; END IF;RETURN value;END $$; ALTER TABLE `+table+` RENAME TO report_iteration_source; CREATE VIEW `+table+` AS SELECT `+projection+` FROM report_iteration_source`); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = pool.Exec(context.Background(), `DROP VIEW IF EXISTS `+table+`; ALTER TABLE report_iteration_source RENAME TO `+table)
			}()
			if _, err := exports.Product(pool).ProcessOne(ctx); err == nil {
				t.Fatal("row iteration failure published an artifact")
			}
			var seen, records, generated int
			var state string
			if err := pool.QueryRow(ctx, `SELECT (SELECT last_value FROM report_rows_seen),(SELECT count(*) FROM export_records),(SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='generated'),state FROM export_jobs WHERE id=$1`, strings.TrimPrefix(path, "/exports/")).Scan(&seen, &records, &generated, &state); err != nil || seen != 2 || records != 0 || generated != 0 || state != "Failed" {
				t.Fatalf("iteration failure seen=%d records=%d generated=%d state=%s err=%v", seen, records, generated, state, err)
			}
			t.Logf("real selector evaluated %d source rows before failure; no artifact or generated audit persisted", seen)
			body := requestBody(t, router, cookie, "GET", path, 200)
			assertContains(t, body, "Failed")
			assertNotContains(t, body, "private iteration failure")
			assertNotContains(t, requestBody(t, router, cookie, "GET", path+"/download", 409), "Iteration must not publish")
		})
	}
}
