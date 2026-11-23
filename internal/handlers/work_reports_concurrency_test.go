package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/jackc/pgx/v5"
)

// Each case uses the production route and its independently registered adapter.
var workReportExportCases = []struct{ name, route, permission string }{
	{"workload", "/reports/workload/export", "job"},
	{"cohort", "/reports/estimate-cohort/export", "estimate"},
	{"jobs", "/jobs/export", "job"},
	{"estimates", "/estimates/export", "estimate"},
	{"invoices", "/invoices/export", "invoice"},
}

func TestWorkloadActualAssignmentAndArchiveBoundaries(t *testing.T) {
	for _, route := range []string{"/reports/workload/export", "/jobs/export"} {
		for _, mutation := range []string{"assignment", "archive"} {
			for _, phase := range []string{"accept", "generate", "download"} {
				t.Run(route+"/"+mutation+"/"+phase, func(t *testing.T) {
					pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					auth := authorization.New(pool)
					actor := authorization.Actor{UserID: admin}
					reader := client.User.Create().SetCompanyID(company).SetEmail("job-report-race@test").SetName("Assigned reporter").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
					role, err := auth.SaveRole(ctx, actor, 0, "Assigned reporter", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.export", Scope: authorization.Assigned}})
					if err != nil {
						t.Fatal(err)
					}
					if err := auth.AssignUser(ctx, actor, reader.ID, role); err != nil {
						t.Fatal(err)
					}
					cookie := sessionCookie(t, ctx, sessions, reader.ID)
					customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Assignment race").SaveX(ctx)
					var completed int64
					if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&completed); err != nil {
						t.Fatal(err)
					}
					job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetStatusID(completed).SetJobType("Lost report membership").SaveX(ctx)
					if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id,role) VALUES($1,$2,'')`, job.ID, reader.ID); err != nil {
						t.Fatal(err)
					}
					review, err := auth.ReviewDispatch(ctx, actor, job.ID, authorization.DispatchPlan{})
					if err != nil {
						t.Fatal(err)
					}
					values := url.Values{"operation_id": {newOperationKey()}, "scope": {"J"}}
					path := ""
					if phase != "accept" {
						path = masterPost(t, router, cookie, route, values, 303)
					}
					if phase == "download" {
						if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
							t.Fatal(err)
						}
						assertContains(t, requestBody(t, router, cookie, "GET", path+"/download", 200), "Lost report membership")
					}
					holder, err := pool.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer holder.Rollback(context.Background())
					var blocker int32
					if err := holder.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(100200)`).Scan(&blocker); err != nil {
						t.Fatal(err)
					}
					event, statement, returned := "DELETE ON job_assignments", "DELETE FROM job_assignments", "OLD"
					if mutation == "archive" {
						event, statement, returned = "UPDATE OF deleted_at ON jobs", "UPDATE jobs SET deleted_at", "NEW"
					}
					if _, err := pool.Exec(ctx, `CREATE FUNCTION hold_report_membership() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(100200);RETURN `+returned+`;END $$; CREATE TRIGGER hold_report_membership BEFORE `+event+` FOR EACH ROW EXECUTE FUNCTION hold_report_membership()`); err != nil {
						t.Fatal(err)
					}
					changed := make(chan error, 1)
					go func() {
						if mutation == "archive" {
							changed <- auth.JobLifecycle(ctx, actor, job.ID, false)
						} else {
							changed <- auth.ConfirmDispatch(ctx, actor, job.ID, review.ID, true)
						}
					}()
					writer := timeExportDMLWaiter(t, ctx, pool, blocker, 100200, statement)
					assertForeignAuthorityExcluded(t, ctx, pool, writer)
					done := make(chan *httptest.ResponseRecorder, 1)
					generated := make(chan error, 1)
					if phase == "generate" {
						go func() { _, err := exports.Product(pool).ProcessOne(ctx); generated <- err }()
						waitAuthorityRequest(t, ctx, pool, writer, authoritySnapshotStatement)
					} else {
						go func() {
							method, target, body := "GET", path+"/download", ""
							if phase == "accept" {
								method, target, body = "POST", route, values.Encode()
							}
							r := httptest.NewRequest(method, target, strings.NewReader(body))
							r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
							r.AddCookie(cookie)
							w := httptest.NewRecorder()
							router.ServeHTTP(w, r)
							done <- w
						}()
						waitAuthorityRequest(t, ctx, pool, writer, authorityWriterStatement)
					}
					if err := holder.Commit(ctx); err != nil {
						t.Fatal(err)
					}
					if err := <-changed; err != nil {
						t.Fatal(err)
					}
					if phase == "generate" {
						if err := <-generated; err != nil {
							t.Fatal(err)
						}
					} else {
						w := <-done
						want := 410
						if phase == "accept" {
							want = 303
							path = w.Header().Get("Location")
						}
						if w.Code != want || strings.Contains(w.Body.String(), "Lost report membership") {
							t.Fatalf("response=%d body=%s", w.Code, w.Body.String())
						}
						if phase == "accept" {
							if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
								t.Fatal(err)
							}
						}
					}
					if phase != "download" {
						rows := timeExportCSV(t, requestBody(t, router, cookie, "GET", path+"/download", 200))
						if len(rows) != 0 {
							t.Fatalf("lost membership exported: %v", rows)
						}
					}
				})
			}
		}
	}
}

func TestWorkloadCoherentSnapshotDuringSourceChange(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original snapshot customer").SaveX(ctx)
	start := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	for range 2 {
		client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Original snapshot job").SetStartTime(start).SetEndTime(start.Add(time.Hour)).SaveX(ctx)
	}
	writeSource := exportSnapshotWriter(t, ctx, pool)
	path := masterPost(t, router, cookie, "/reports/workload/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}, 303)
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	var blocker int32
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(100201)`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	// Pause after the transaction snapshot is established but before the adapter
	// selects any rows. The writer proves runtime guards remain enabled.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION hold_workload_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(100201);RETURN NEW;END $$; CREATE TRIGGER hold_workload_snapshot BEFORE INSERT ON export_records FOR EACH ROW WHEN(NEW.ordinal=0) EXECUTE FUNCTION hold_workload_snapshot()`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := exports.Product(pool).ProcessOne(ctx); done <- err }()
	timeExportDMLWaiter(t, ctx, pool, blocker, 100201, "INSERT INTO export_records")
	writeSource(int(blocker), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE jobs SET job_type='Changed snapshot job',start_time=NULL,end_time=NULL`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE customers SET display_name='Changed snapshot customer'`)
		return err
	})
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rows := timeExportCSV(t, requestBody(t, router, cookie, "GET", path+"/download", 200))
	if len(rows) != 2 {
		t.Fatalf("snapshot population: %v", rows)
	}
	for _, row := range rows {
		if row["title"] != "Original snapshot job" || row["customer"] != "Original snapshot customer" || row["scheduled"] != "true" || row["unscheduled"] != "false" || row["as_of"] != rows[0]["as_of"] {
			t.Fatalf("mixed row/measures snapshot: %v", row)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT row_count FROM export_jobs WHERE id=$1`, strings.TrimPrefix(path, "/exports/")).Scan(&count); err != nil || count != len(rows) {
		t.Fatalf("snapshot total=%d rows=%d err=%v", count, len(rows), err)
	}
	fresh := masterPost(t, router, cookie, "/reports/workload/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	for _, row := range timeExportCSV(t, requestBody(t, router, cookie, "GET", fresh+"/download", 200)) {
		if row["title"] != "Changed snapshot job" || row["customer"] != "Changed snapshot customer" || row["scheduled"] != "false" || row["unscheduled"] != "true" {
			t.Fatalf("fresh snapshot did not observe committed changes: %v", row)
		}
	}
}

func TestEstimateCohortActualConversionReversionBoundaries(t *testing.T) {
	for _, phase := range []string{"accept", "generate", "download"} {
		t.Run(phase, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Cohort reversion").SaveX(ctx)
			estimate := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Reverted cohort source").SaveX(ctx)
			convertPath := fmt.Sprintf("/estimates/%d/convert", estimate.ID)
			fieldPost(t, router, cookie, convertPath, conversionReview(t, router, cookie, convertPath), 303)
			var invoice int64
			if err := pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1 AND reverted_at IS NULL`, estimate.ID).Scan(&invoice); err != nil {
				t.Fatal(err)
			}
			revertPath := fmt.Sprintf("/invoices/%d/revert-to-estimate", invoice)
			revertValues := conversionReview(t, router, cookie, revertPath)
			values := url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}
			path := ""
			if phase != "accept" {
				path = masterPost(t, router, cookie, "/reports/estimate-cohort/export", values, 303)
			}
			if phase == "download" {
				if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
					t.Fatal(err)
				}
				rows := timeExportCSV(t, requestBody(t, router, cookie, "GET", path+"/download", 200))
				if len(rows) != 1 || rows[0]["active_conversion"] != "true" || rows[0]["invoice_id"] != fmt.Sprint(invoice) {
					t.Fatalf("active cohort snapshot: %v", rows)
				}
			}
			holder, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(context.Background())
			var blocker int32
			if err := holder.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(100202)`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `CREATE FUNCTION hold_cohort_revert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(100202);RETURN NEW;END $$; CREATE TRIGGER hold_cohort_revert BEFORE UPDATE OF reverted_at ON estimate_invoice_conversion_cycles FOR EACH ROW EXECUTE FUNCTION hold_cohort_revert()`); err != nil {
				t.Fatal(err)
			}
			reverted := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("POST", revertPath, strings.NewReader(revertValues.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				reverted <- w
			}()
			writer := timeExportDMLWaiter(t, ctx, pool, blocker, 100202, "UPDATE estimate_invoice_conversion_cycles SET reverted_by")
			assertForeignAuthorityExcluded(t, ctx, pool, writer)
			done := make(chan *httptest.ResponseRecorder, 1)
			generated := make(chan error, 1)
			if phase == "generate" {
				go func() { _, err := exports.Product(pool).ProcessOne(ctx); generated <- err }()
				waitAuthorityRequest(t, ctx, pool, writer, authoritySnapshotStatement)
			} else {
				go func() {
					method, target, body := "GET", path+"/download", ""
					if phase == "accept" {
						method, target, body = "POST", "/reports/estimate-cohort/export", values.Encode()
					}
					r := httptest.NewRequest(method, target, strings.NewReader(body))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(cookie)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, r)
					done <- w
				}()
				waitAuthorityRequest(t, ctx, pool, writer, authorityWriterStatement)
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if w := <-reverted; w.Code != 303 {
				t.Fatalf("reversion=%d %s", w.Code, w.Body.String())
			}
			if phase == "generate" {
				if err := <-generated; err != nil {
					t.Fatal(err)
				}
			} else {
				w := <-done
				want := 410
				if phase == "accept" {
					want, path = 303, w.Header().Get("Location")
				}
				if w.Code != want {
					t.Fatalf("export response=%d %s", w.Code, w.Body.String())
				}
				if phase == "accept" {
					if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			if phase != "download" {
				rows := timeExportCSV(t, requestBody(t, router, cookie, "GET", path+"/download", 200))
				if len(rows) != 1 || rows[0]["estimate_id"] != fmt.Sprint(estimate.ID) || rows[0]["active_conversion"] != "false" || rows[0]["conversion_hidden"] != "false" || rows[0]["invoice_id"] != "" {
					t.Fatalf("reverted cohort leaked old conversion: %v", rows)
				}
			}
		})
	}
}

func TestOperationalListExportActualGrantRevocationBoundaries(t *testing.T) {
	for _, output := range workReportExportCases {
		for _, phase := range []string{"accept", "generate", "download"} {
			t.Run(output.name+"/"+phase, func(t *testing.T) {
				pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				auth := authorization.New(pool)
				actor := authorization.Actor{UserID: admin}
				reader := client.User.Create().SetCompanyID(company).SetEmail("report-race@test").SetName("Report exporter").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
				view := []authorization.Grant{{Permission: output.permission + ".view", Scope: authorization.All}}
				if output.name == "cohort" {
					view = append(view, authorization.Grant{Permission: "invoice.view", Scope: authorization.All}, authorization.Grant{Permission: "invoice.export", Scope: authorization.All})
				}
				grants := append(append([]authorization.Grant(nil), view...), authorization.Grant{Permission: output.permission + ".export", Scope: authorization.All})
				role, err := auth.SaveRole(ctx, actor, 0, "Report exporter", grants)
				if err != nil {
					t.Fatal(err)
				}
				if err := auth.AssignUser(ctx, actor, reader.ID, role); err != nil {
					t.Fatal(err)
				}
				cookie := sessionCookie(t, ctx, sessions, reader.ID)
				customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Report race customer").SaveX(ctx)
				client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Must not disclose report row").SaveX(ctx)
				client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Must not disclose report row").SaveX(ctx)
				client.Invoice.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetInvoiceNumber(981).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetTitle("Must not disclose report row").SaveX(ctx)
				values := url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}
				path := ""
				if phase != "accept" {
					path = masterPost(t, router, cookie, output.route, values, 303)
				}
				if phase == "download" {
					if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
						t.Fatal(err)
					}
				}
				var revision int64
				if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
					t.Fatal(err)
				}
				holder, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer holder.Rollback(context.Background())
				if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(100198)`); err != nil {
					t.Fatal(err)
				}
				var blocker int32
				if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `CREATE FUNCTION hold_report_revoke() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(100198);RETURN OLD;END $$; CREATE TRIGGER hold_report_revoke BEFORE DELETE ON authorization_grants FOR EACH ROW WHEN(OLD.permission LIKE '%.export') EXECUTE FUNCTION hold_report_revoke()`); err != nil {
					t.Fatal(err)
				}
				revoked := make(chan error, 1)
				go func() {
					_, err := auth.SaveRole(ctx, actor, role, "Report exporter", view, revision)
					revoked <- err
				}()
				writer := timeExportDMLWaiter(t, ctx, pool, blocker, 100198, "DELETE FROM authorization_grants")
				assertForeignAuthorityExcluded(t, ctx, pool, writer)
				done := make(chan error, 1)
				if phase == "generate" {
					go func() { _, err := exports.Product(pool).ProcessOne(ctx); done <- err }()
					waitAuthorityRequest(t, ctx, pool, writer, authoritySnapshotStatement)
				} else {
					go func() {
						method, target, body, want := "GET", path+"/download", "", 410
						if phase == "accept" {
							method, target, body, want = "POST", output.route, values.Encode(), 403
						}
						r := httptest.NewRequest(method, target, strings.NewReader(body))
						r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
						r.AddCookie(cookie)
						w := httptest.NewRecorder()
						router.ServeHTTP(w, r)
						if w.Code != want || strings.Contains(w.Body.String(), "Must not disclose report row") {
							done <- fmt.Errorf("response %d: %s", w.Code, w.Body.String())
							return
						}
						done <- nil
					}()
					waitAuthorityRequest(t, ctx, pool, writer, authorityWriterStatement)
				}
				if err := holder.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err := <-revoked; err != nil {
					t.Fatal(err)
				}
				result := <-done
				if phase == "generate" {
					if result == nil {
						t.Fatal("revoked generation succeeded")
					}
					assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Failed")
				} else if result != nil {
					t.Fatal(result)
				}
				var accepted, records, generated int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs WHERE operation_id=$1`, values.Get("operation_id")).Scan(&accepted); err != nil {
					t.Fatal(err)
				}
				if phase == "accept" && accepted != 0 {
					t.Fatal("revoked acceptance persisted")
				}
				if phase == "generate" {
					if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM export_records),(SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='generated')`).Scan(&records, &generated); err != nil || records != 0 || generated != 0 {
						t.Fatalf("revoked publication: records=%d audit=%d err=%v", records, generated, err)
					}
				}
			})
		}
	}
}
