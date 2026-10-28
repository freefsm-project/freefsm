package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseRestoreMasterArchiveCommitOrdersHTTP(t *testing.T) {
	for _, kind := range []string{"customer", "location"} {
		for _, first := range []string{"archive", "restore"} {
			t.Run(kind+"/"+first, func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				cookie := sessionCookie(t, ctx, sessions, admin)
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race Customer").SaveX(ctx)
				l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Race premises").SaveX(ctx)
				fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
				var id int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
				path := fmt.Sprintf("/leases/%d", id)
				fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "premises_id": {fmt.Sprint(l.ID)}}, 303)
				fieldPost(t, web, cookie, path+"/delete", nil, 303)
				f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
				done := make(chan *httptest.ResponseRecorder, 1)
				barrier, err := pool.Begin(ctx)
				historyCheck(t, err)
				defer barrier.Rollback(context.Background())
				var holder int
				if first == "archive" {
					historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
					go func() { done <- f.call(creditCommand{path: path + "/restore"}) }()
					commercialWaiters(t, ctx, pool, holder, 1)
					query, target := `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, c.ID
					if kind == "location" {
						query, target = `UPDATE locations SET deleted_at=clock_timestamp() WHERE id=$1`, l.ID
					}
					_, err = barrier.Exec(ctx, query, target)
					historyCheck(t, err)
					historyCheck(t, barrier.Commit(ctx))
					want := 409
					if kind == "location" {
						want = 303
					}
					if response := <-done; response.Code != want {
						t.Fatalf("waiting restore=%d: %s", response.Code, response.Body.String())
					}
				} else {
					var review authorization.CustomerArchivePreview
					if kind == "customer" {
						masterJSON(t, web, cookie, fmt.Sprintf("/customers/%d/archive", c.ID), &review)
					}
					_, err = pool.Exec(ctx, `CREATE FUNCTION pause_lease_restore_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.restore' THEN PERFORM pg_advisory_xact_lock(650109); END IF; RETURN NEW; END $$; CREATE TRIGGER pause_lease_restore_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION pause_lease_restore_audit()`)
					historyCheck(t, err)
					historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(650109)`).Scan(&holder))
					go func() { done <- f.call(creditCommand{path: path + "/restore"}) }()
					commercialWaiters(t, ctx, pool, holder, 1)
					var restorePID int
					historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid))`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&restorePID))
					archivePath := fmt.Sprintf("/locations/%d/delete", l.ID)
					values := url.Values{}
					if kind == "customer" {
						archivePath = fmt.Sprintf("/customers/%d/delete", c.ID)
						values.Set("archive_snapshot", review.Snapshot)
					}
					archived := make(chan *httptest.ResponseRecorder, 1)
					go func() { archived <- f.call(creditCommand{path: archivePath, values: values}) }()
					commercialWaiters(t, ctx, pool, restorePID, 1)
					historyCheck(t, barrier.Commit(ctx))
					if response := <-done; response.Code != 303 {
						t.Fatalf("restore=%d %s", response.Code, response.Body.String())
					}
					if response := <-archived; response.Code != 409 {
						t.Fatalf("waiting master archive=%d %s", response.Code, response.Body.String())
					}
				}
				var valid bool
				historyCheck(t, pool.QueryRow(ctx, `SELECT (l.deleted_at IS NOT NULL OR c.deleted_at IS NULL) AND l.premises_id=$2 AND d.premises_id=l.premises_id AND d.archived=(l.deleted_at IS NOT NULL) AND NOT EXISTS(SELECT 1 FROM lease_asset_dependencies WHERE lease_id=l.id) FROM leases l JOIN customers c ON c.id=l.customer_id JOIN lease_master_dependencies d ON d.lease_id=l.id WHERE l.id=$1`, id, l.ID).Scan(&valid))
				if !valid {
					t.Fatal("invalid post-race agreement/dependency state")
				}
				t.Logf("observed %s/%s database lock holder %d and competing waiter", kind, first, holder)
			})
		}
	}
}

func TestLeaseRestoreGrantRaceAndAuditRollbackHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Restore audit Customer").SaveX(ctx)
	fieldPost(t, web, adminCookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, adminCookie, path+"/delete", nil, 303)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Restore race", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.archived.view", Scope: authorization.All}, {Permission: "lease.restore", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Independent restorer").SetEmail("restore-race65@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	before := leaseDurableState(t, pool)
	barrier, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer barrier.Rollback(context.Background())
	var holder int
	historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
	f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.call(creditCommand{path: path + "/restore"}) }()
	commercialWaiters(t, ctx, pool, holder, 1)
	_, err = barrier.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.restore'`, role)
	historyCheck(t, err)
	historyCheck(t, barrier.Commit(ctx))
	if response := <-done; response.Code != 403 {
		t.Fatalf("waiting revoked Restore=%d", response.Code)
	}
	historyEqual(t, "revoked restore has no partial effect/audit", leaseDurableState(t, pool), before)
	_, err = pool.Exec(ctx, `CREATE FUNCTION reject_lease_restore_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.restore' THEN RAISE EXCEPTION 'injected Restore audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_lease_restore_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_lease_restore_audit()`)
	historyCheck(t, err)
	fieldPost(t, web, adminCookie, path+"/restore", nil, 409)
	historyEqual(t, "Restore and projection roll back with audit failure", leaseDurableState(t, pool), before)
	t.Logf("observed grant revocation while Restore waited behind %d", holder)
}

func TestLeaseRegistrationAndPremisesMoveWinArchiveRaceHTTP(t *testing.T) {
	for _, kind := range []string{"registration", "premises-move"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Native writer race").SaveX(ctx)
			command := creditCommand{path: "/leases", values: url.Values{"customer_id": {fmt.Sprint(c.ID)}}}
			action := "lease.create"
			archive := creditCommand{path: fmt.Sprintf("/customers/%d/delete", c.ID)}
			var oldLocation int64
			if kind == "premises-move" {
				old := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Original premises").SaveX(ctx)
				next := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("New premises").SaveX(ctx)
				oldLocation = old.ID
				fieldPost(t, web, cookie, command.path, command.values, 303)
				var id int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
				path := fmt.Sprintf("/leases/%d", id)
				fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "premises_id": {fmt.Sprint(old.ID)}}, 303)
				command = creditCommand{path: path, values: url.Values{"revision": {"1"}, "premises_id": {fmt.Sprint(next.ID)}}}
				action = "lease.details.change"
				archive = creditCommand{path: fmt.Sprintf("/locations/%d/delete", next.ID)}
			} else {
				var preview authorization.CustomerArchivePreview
				masterJSON(t, web, cookie, fmt.Sprintf("/customers/%d/archive", c.ID), &preview)
				archive.values = url.Values{"archive_snapshot": {preview.Snapshot}}
			}
			_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION pause_native_lease_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s' THEN PERFORM pg_advisory_xact_lock(650110); END IF; RETURN NEW; END $$; CREATE TRIGGER pause_native_lease_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION pause_native_lease_audit()`, action))
			historyCheck(t, err)
			barrier, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var holder int
			historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(650110)`).Scan(&holder))
			f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
			first := make(chan *httptest.ResponseRecorder, 1)
			go func() { first <- f.call(command) }()
			commercialWaiters(t, ctx, pool, holder, 1)
			var writer int
			historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid))`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&writer))
			second := make(chan *httptest.ResponseRecorder, 1)
			go func() { second <- f.call(archive) }()
			commercialWaiters(t, ctx, pool, writer, 1)
			historyCheck(t, barrier.Commit(ctx))
			if response := <-first; response.Code != 303 {
				t.Fatalf("native first writer=%d %s", response.Code, response.Body.String())
			}
			if response := <-second; response.Code != 409 {
				t.Fatalf("waiting master archive=%d %s", response.Code, response.Body.String())
			}
			if oldLocation != 0 {
				fieldPost(t, web, cookie, fmt.Sprintf("/locations/%d/delete", oldLocation), nil, 303)
			}
			t.Logf("observed native %s writer %d ahead of master archive waiter", kind, writer)
		})
	}
}
