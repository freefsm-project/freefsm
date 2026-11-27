package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/jackc/pgx/v5"
)

func TestLeaseExportActualMutationSnapshotOrders(t *testing.T) {
	for _, first := range []string{"export", "revision"} {
		t.Run(first, func(t *testing.T) {
			pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			leaseExportClock(t, pool, "2024-06-01")
			leaseExportInstallRecordingClock(t, pool, "2024-06-01T12:00:00Z")
			lease, assets := leaseExportAgreement(t, pool, client, company, admin, "Snapshot coherent rent", "2024-01-01", "2024-12-31", 5000, 2500)
			var customer int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT customer_id FROM leases WHERE id=$1`, lease).Scan(&customer))
			for range 102 {
				_, err := authorization.New(pool).RegisterDraftLease(ctx, authorization.Actor{UserID: admin}, customer)
				historyCheck(t, err)
			}
			path := masterPost(t, web, cookie, leaseExportReviewPath, url.Values{"mode": {"billing"}, "operation_id": {newOperationKey()}}, 303)
			holder, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer holder.Rollback(context.Background())
			_, err = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(101101)`)
			historyCheck(t, err)
			var blocker int32
			historyCheck(t, holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker))
			sql := `CREATE FUNCTION lease_export_hold_dml() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(101101); RETURN NEW; END $$; `
			statement := "INSERT INTO export_records"
			if first == "export" {
				sql += `CREATE TRIGGER lease_export_hold_dml BEFORE INSERT ON export_records FOR EACH ROW WHEN(NEW.ordinal=2) EXECUTE FUNCTION lease_export_hold_dml()`
			} else {
				sql += `CREATE TRIGGER lease_export_hold_dml BEFORE INSERT ON lease_billing_revisions FOR EACH ROW EXECUTE FUNCTION lease_export_hold_dml()`
				statement = "INSERT INTO lease_billing_revisions"
			}
			_, err = pool.Exec(ctx, sql)
			historyCheck(t, err)
			exported, revised := make(chan error, 1), make(chan error, 1)
			export := func() { _, err := exports.Product(pool).ProcessOne(ctx); exported <- err }
			revise := func() {
				revised <- authorization.New(pool).ReviseLeaseBilling(ctx, authorization.Actor{UserID: admin}, lease, authorization.LeaseBillingRevisionInput{EffectiveOn: "2024-06-01", Reason: "Actual fenced rent change", OperationKey: newOperationKey(), Changes: []authorization.LeaseBillingChange{{AssetID: assets[0], RentCents: 7500, TaxRate: "5"}}})
			}
			if first == "export" {
				go export()
			} else {
				go revise()
			}
			writer := timeExportDMLWaiter(t, ctx, pool, blocker, 101101, statement)
			assertForeignAuthorityExcluded(t, ctx, pool, writer)
			if first == "export" {
				go revise()
				waitAuthorityRequest(t, ctx, pool, writer, authorityWriterStatement)
			} else {
				go export()
				waitAuthorityRequest(t, ctx, pool, writer, authoritySnapshotStatement)
			}
			historyCheck(t, holder.Commit(ctx))
			historyCheck(t, <-exported)
			historyCheck(t, <-revised)
			rows := leaseExportRows(t, requestBody(t, web, cookie, "GET", path+"/download", 200))
			historyEqual(t, "snapshot includes more than one browser page", len(leaseExportKind(rows, "lease")), 103)
			historyEqual(t, "snapshot summary includes every page", leaseExportKind(rows, "summary")[0]["matching_lease_count"], "103")
			want := "75.00"
			if first == "revision" {
				want = "100.00"
			}
			historyEqual(t, "coherent Lease rent", leaseExportKind(rows, "lease")[0]["monthly_rent"], want)
			historyEqual(t, "coherent matching total", leaseExportKind(rows, "summary")[0]["matching_monthly_rent"], want)
			fresh, _ := leaseExportGet(t, pool, web, cookie, url.Values{"mode": {"billing"}})
			historyEqual(t, "fresh export observes committed revision", leaseExportKind(fresh, "summary")[0]["matching_monthly_rent"], "100.00")
		})
	}
}

func TestLeaseExportActualGrantRevocationBoundaries(t *testing.T) {
	for _, phase := range []string{"accept", "generate", "download"} {
		t.Run(phase, func(t *testing.T) {
			pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			user, role := leaseExportReader(t, pool, client, company, admin, "Fenced Lease exporter", leaseExportGrants("lease.view", "lease.export"))
			cookie := sessionCookie(t, ctx, sessions, user)
			values := url.Values{"mode": {"operational"}, "operation_id": {newOperationKey()}}
			path := ""
			if phase != "accept" {
				path = masterPost(t, web, cookie, leaseExportReviewPath, values, 303)
			}
			if phase == "download" {
				_, err := exports.Product(pool).ProcessOne(ctx)
				historyCheck(t, err)
			}
			var revision int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
			holder, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer holder.Rollback(context.Background())
			_, err = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(101102)`)
			historyCheck(t, err)
			var blocker int32
			historyCheck(t, holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker))
			_, err = pool.Exec(ctx, `CREATE FUNCTION lease_export_hold_revoke() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(101102); RETURN OLD; END $$; CREATE TRIGGER lease_export_hold_revoke BEFORE DELETE ON authorization_grants FOR EACH ROW WHEN(OLD.permission='lease.export') EXECUTE FUNCTION lease_export_hold_revoke()`)
			historyCheck(t, err)
			revoked := make(chan error, 1)
			go func() {
				_, err := authorization.New(pool).SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Fenced Lease exporter", leaseExportGrants("lease.view"), revision)
				revoked <- err
			}()
			writer := timeExportDMLWaiter(t, ctx, pool, blocker, 101102, "DELETE FROM authorization_grants")
			assertForeignAuthorityExcluded(t, ctx, pool, writer)
			done := make(chan error, 1)
			if phase == "generate" {
				go func() { _, err := exports.Product(pool).ProcessOne(ctx); done <- err }()
				waitAuthorityRequest(t, ctx, pool, writer, authoritySnapshotStatement)
			} else {
				go func() {
					method, target, body, want := "GET", path+"/download", "", 410
					if phase == "accept" {
						method, target, body, want = "POST", leaseExportReviewPath, values.Encode(), 403
					}
					r := httptest.NewRequest(method, target, strings.NewReader(body))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(cookie)
					w := httptest.NewRecorder()
					web.ServeHTTP(w, r)
					if w.Code != want {
						done <- fmt.Errorf("revoked %s: %d %s", phase, w.Code, w.Body.String())
						return
					}
					done <- nil
				}()
				statement := authorityWriterStatement
				if phase == "accept" {
					statement = authoritySnapshotStatement
				}
				waitAuthorityRequest(t, ctx, pool, writer, statement)
			}
			historyCheck(t, holder.Commit(ctx))
			historyCheck(t, <-revoked)
			result := <-done
			if phase == "generate" {
				if result == nil {
					t.Fatal("revoked generation succeeded")
				}
			} else {
				historyCheck(t, result)
			}
			var count int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM export_records`).Scan(&count))
			if phase != "download" {
				historyEqual(t, "no denied publication", count, 0)
			}
		})
	}
}

func TestLeaseExportAtomicFailuresAndIntegrity(t *testing.T) {
	for _, fault := range []string{"accepted", "generated", "downloaded", "row", "digest", "missing_ordinal"} {
		t.Run(fault, func(t *testing.T) {
			pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			populateFinalizedLease(t, pool, client, web, cookie, company)
			values := url.Values{"mode": {"billing"}, "operation_id": {newOperationKey()}}
			installAudit := func(event string) {
				_, err := pool.Exec(ctx, `CREATE FUNCTION lease_export_fail_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='export' AND NEW.detail->>'event'='`+event+`' THEN RAISE EXCEPTION 'lease export audit fixture failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER lease_export_fail_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION lease_export_fail_audit()`)
				historyCheck(t, err)
			}
			if fault == "accepted" {
				installAudit(fault)
				masterPost(t, web, cookie, leaseExportReviewPath, values, 503)
				var count int
				historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs`).Scan(&count))
				historyEqual(t, "acceptance audit failure rolls back job", count, 0)
				return
			}
			path := masterPost(t, web, cookie, leaseExportReviewPath, values, 303)
			id := strings.TrimPrefix(path, "/exports/")
			if fault == "generated" {
				installAudit(fault)
			}
			if fault == "row" {
				_, err := pool.Exec(ctx, `CREATE FUNCTION lease_export_fail_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ordinal=3 THEN RAISE EXCEPTION 'lease export third row failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER lease_export_fail_record BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION lease_export_fail_record()`)
				historyCheck(t, err)
			}
			_, err := exports.Product(pool).ProcessOne(ctx)
			if fault == "generated" || fault == "row" {
				if err == nil {
					t.Fatal("fault did not fail generation")
				}
				var count, generated int
				var state string
				historyCheck(t, pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM export_records WHERE export_id=$1),(SELECT count(*) FROM authorization_audit WHERE target_type='export' AND target_id=$1 AND detail->>'event'='generated') FROM export_jobs WHERE id=$1`, id).Scan(&state, &count, &generated))
				historyEqual(t, "failed publication", state, "Failed")
				historyEqual(t, "no partial records", count, 0)
				historyEqual(t, "no false successful generation audit", generated, 0)
				return
			}
			historyCheck(t, err)
			switch fault {
			case "downloaded":
				installAudit(fault)
			case "digest":
				_, err = pool.Exec(ctx, `UPDATE export_records SET csv='corrupt'::bytea WHERE export_id=$1 AND ordinal=1`, id)
			case "missing_ordinal":
				_, err = pool.Exec(ctx, `DELETE FROM export_records WHERE export_id=$1 AND ordinal=1`, id)
			}
			historyCheck(t, err)
			body := requestBody(t, web, cookie, "GET", path+"/download", 503)
			assertNotContains(t, body, "generated_total")
			assertNotContains(t, body, "corrupt")
			var downloaded int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND target_id=$1 AND detail->>'event'='downloaded'`, id).Scan(&downloaded))
			historyEqual(t, "no false successful download audit", downloaded, 0)
		})
	}
}

func TestLeaseExportMalformedRequiredReferences(t *testing.T) {
	for _, mutation := range []string{"reference-'complete_generation'", "reference-'summary'", "reference-'leases'", "jsonb_set(reference,'{version}','2')", "reference || '{\"unknown\":true}'::jsonb", "jsonb_set(reference,'{leases}', 'null')"} {
		t.Run(mutation, func(t *testing.T) {
			pool, _, web, sessions, _, admin := leaseExportHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			path := leaseExportPrepare(t, pool, web, cookie, url.Values{"mode": {"operational"}})
			_, err := pool.Exec(ctx, `UPDATE export_records SET reference=`+mutation+` WHERE export_id=$1 AND ordinal>0`, strings.TrimPrefix(path, "/exports/"))
			historyCheck(t, err)
			body := requestBody(t, web, cookie, "GET", path+"/download", 410)
			assertNotContains(t, body, "matching_lease_count")
		})
	}
}

func TestLeaseExportInterruptedWorkerAndCompetingRecovery(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	populateFinalizedLease(t, pool, client, web, cookie, company)
	path := masterPost(t, web, cookie, leaseExportReviewPath, url.Values{"mode": {"billing"}, "operation_id": {newOperationKey()}}, 303)
	id := strings.TrimPrefix(path, "/exports/")
	holder, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer holder.Rollback(context.Background())
	_, err = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(101103)`)
	historyCheck(t, err)
	var blocker int32
	historyCheck(t, holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker))
	_, err = pool.Exec(ctx, `CREATE FUNCTION lease_export_pause_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ordinal=2 THEN PERFORM pg_advisory_xact_lock(101103); END IF; RETURN NEW; END $$; CREATE TRIGGER lease_export_pause_record BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION lease_export_pause_record()`)
	historyCheck(t, err)
	workerCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { _, err := exports.Product(pool).ProcessOne(workerCtx); done <- err }()
	timeExportDMLWaiter(t, ctx, pool, blocker, 101103, "INSERT INTO export_records")
	processed, err := exports.Product(pool).ProcessOne(ctx)
	historyCheck(t, err)
	if processed {
		t.Fatal("competing worker acquired active publication")
	}
	stop()
	if err := <-done; err == nil {
		t.Fatal("interruption not observed")
	}
	var state string
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM export_records WHERE export_id=$1) FROM export_jobs WHERE id=$1`, id).Scan(&state, &count))
	historyEqual(t, "recoverable interrupted state", state, "Running")
	historyEqual(t, "interrupted rows rollback", count, 0)
	historyCheck(t, holder.Commit(ctx))
	_, err = exports.Product(pool).ProcessOne(ctx)
	historyCheck(t, err)
	leaseExportRows(t, requestBody(t, web, cookie, "GET", path+"/download", 200))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND target_id=$1 AND detail->>'event'='generated'`, id).Scan(&count))
	historyEqual(t, "one recovered publication audit", count, 1)
}

func TestLeaseExportExpiryDuringTransferPreparation(t *testing.T) {
	pool, _, web, sessions, _, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := leaseExportPrepare(t, pool, web, cookie, url.Values{"mode": {"operational"}})
	id := strings.TrimPrefix(path, "/exports/")
	// Elapsed time is the subject here, not a race-order oracle: force transfer
	// preparation itself across Ready+24h, as the existing engine fixture does.
	_, err := pool.Exec(ctx, `CREATE FUNCTION lease_export_slow_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='export' AND NEW.detail->>'event'='downloaded' THEN PERFORM pg_sleep(1.2); END IF; RETURN NEW; END $$; CREATE TRIGGER lease_export_slow_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION lease_export_slow_audit()`)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours'+interval '1 second' WHERE id=$1`, id)
	historyCheck(t, err)
	body := requestBody(t, web, cookie, "GET", path+"/download", 410)
	assertNotContains(t, body, "matching_lease_count")
	var downloaded int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND target_id=$1 AND detail->>'event'='downloaded'`, id).Scan(&downloaded))
	historyEqual(t, "expiry during preparation rolls back audit", downloaded, 0)
}

func TestLeaseExportIdentityAssignmentArchiveSerialization(t *testing.T) {
	for _, change := range []string{"role_replacement", "user_disable", "assignment", "archive"} {
		t.Run(change, func(t *testing.T) {
			pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			svc, actor := authorization.New(pool), authorization.Actor{UserID: admin}
			lease, _ := populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
			var customer, invoice int64
			var today string
			historyCheck(t, pool.QueryRow(ctx, `SELECT customer_id,lease_dependency_today(company_id)::text FROM leases WHERE id=$1`, lease).Scan(&customer, &today))
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE lease_id=$1`, lease).Scan(&invoice))
			historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease, authorization.LeaseNoticeInput{Action: "create", Category: "cancel_before_start", Initiator: "company", NoticeOn: today, EffectiveOn: today, Reason: "Permit archive race", OperationKey: newOperationKey()}))
			grants := leaseExportGrants("lease.view", "lease.export", "lease.billing.view")
			for _, permission := range []string{"job.view", "invoice.view", "pricing.view"} {
				grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.Assigned})
			}
			user, _ := leaseExportReader(t, pool, client, company, admin, "Serialization exporter", grants)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer).SetJobType("Captured Assigned anchor").SaveX(ctx)
			_, err := pool.Exec(ctx, `UPDATE invoices SET job_id=$1 WHERE id=$2`, job.ID, invoice)
			historyCheck(t, err)
			_, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, job.ID, user)
			historyCheck(t, err)
			rows, path := leaseExportGet(t, pool, web, sessionCookie(t, ctx, sessions, user), url.Values{"mode": {"billing"}})
			historyEqual(t, "captured sensitive Invoice", leaseExportKind(rows, "invoice")[0]["invoice_id"], fmt.Sprint(invoice))
			replacementRole, err := svc.SaveRole(ctx, actor, 0, "Replacement no export", leaseExportGrants("lease.view"))
			historyCheck(t, err)
			holder, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer holder.Rollback(context.Background())
			_, err = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(101104)`)
			historyCheck(t, err)
			var blocker int32
			historyCheck(t, holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker))
			trigger, statement := `BEFORE UPDATE ON users FOR EACH ROW WHEN(NEW.authorization_role_id IS DISTINCT FROM OLD.authorization_role_id)`, "UPDATE users"
			mutate := func() error { return svc.AssignUser(ctx, actor, user, replacementRole) }
			switch change {
			case "user_disable":
				trigger = `BEFORE UPDATE ON users FOR EACH ROW WHEN(NEW.is_active IS DISTINCT FROM OLD.is_active)`
				mutate = func() error { return svc.SetEnabled(ctx, actor, user, false) }
			case "assignment":
				trigger, statement = `BEFORE DELETE ON job_assignments FOR EACH ROW`, "DELETE FROM job_assignments"
				mutate = func() error {
					return svc.WithinActor(ctx, actor, func(tx pgx.Tx, _ authorization.Principal) error {
						_, err := tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, user)
						return err
					})
				}
			case "archive":
				trigger, statement = `BEFORE UPDATE ON leases FOR EACH ROW WHEN(NEW.deleted_at IS DISTINCT FROM OLD.deleted_at)`, "UPDATE leases"
				mutate = func() error { return svc.LeaseLifecycle(ctx, actor, lease, false) }
			}
			_, err = pool.Exec(ctx, `CREATE FUNCTION lease_export_hold_identity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(101104); IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW; END $$; CREATE TRIGGER lease_export_hold_identity `+trigger+` EXECUTE FUNCTION lease_export_hold_identity()`)
			historyCheck(t, err)
			changed := make(chan error, 1)
			go func() { changed <- mutate() }()
			writer := timeExportDMLWaiter(t, ctx, pool, blocker, 101104, statement)
			assertForeignAuthorityExcluded(t, ctx, pool, writer)
			id, err := strconv.ParseInt(strings.TrimPrefix(path, "/exports/"), 10, 64)
			historyCheck(t, err)
			downloaded := make(chan error, 1)
			go func() {
				download, err := exports.Product(pool).PrepareDownload(ctx, authorization.Actor{UserID: user, Channel: "web"}, id)
				if download != nil {
					_ = download.Close()
				}
				downloaded <- err
			}()
			waitAuthorityRequest(t, ctx, pool, writer, authorityWriterStatement)
			historyCheck(t, holder.Commit(ctx))
			historyCheck(t, <-changed)
			if err := <-downloaded; err == nil {
				t.Fatal("authority-changing commit lost serialization against prepared download")
			}
			var downloads int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND target_id=$1 AND detail->>'event'='downloaded'`, id).Scan(&downloads))
			historyEqual(t, "only the authorized fixture download audited", downloads, 1)
		})
	}
}
