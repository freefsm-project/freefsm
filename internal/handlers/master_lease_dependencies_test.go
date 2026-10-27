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

// The registration/finalization seam is a database function used inside the
// owning Lease transaction; archive is exercised through production HTTP.
func TestMasterLeasePremisesDependencyHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Lease Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Lease premises").SaveX(ctx)
	path := fmt.Sprintf("/locations/%d", l.ID)
	register := func(state string) {
		t.Helper()
		_, err := pool.Exec(ctx, `SELECT set_lease_master_dependencies($1,$2,$3,$4,$5,false,'[]'::jsonb)`, int64(63), company, c.ID, l.ID, state)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []string{"draft", "scheduled", "active", "month_to_month"} {
		register(state)
		masterPost(t, router, cookie, path+"/delete", nil, 409)
	}
	register("terminated")
	masterPost(t, router, cookie, path+"/delete", nil, 303)
	if _, err := pool.Exec(ctx, `SELECT set_lease_master_dependencies($1,$2,$3,$4,'draft',false,'[]'::jsonb)`, int64(64), company, c.ID, l.ID); err == nil {
		t.Fatal("registration selected archived premises")
	}
	masterPost(t, router, cookie, path+"/restore", nil, 303)
}

func TestMasterLeaseCoverageAndPlacementHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Covered Customer").SaveX(ctx)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	status := client.AssetStatus.Create().SetCompanyID(company).SetName("Retired descriptive label").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetName("Covered Asset").SetAssetTypeID(typ.ID).SetAssetStatusID(status.ID).SaveX(ctx)
	path := fmt.Sprintf("/assets/%d", a.ID)
	svc := authorization.New(pool)
	write := func(state, start string, end *string) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		historyCheck(t, err)
		defer tx.Rollback(ctx)
		historyCheck(t, svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: admin}, authorization.LeaseDependencies{LeaseID: 63, CustomerID: c.ID, Lifecycle: state, Coverage: []authorization.LeaseAssetDependency{{AssetID: a.ID, StartsOn: start, EndsOn: end}}}))
		historyCheck(t, tx.Commit(ctx))
	}
	for _, state := range []string{"scheduled", "active", "month_to_month"} {
		write(state, "2099-01-01", nil)
		fieldPost(t, web, cookie, path+"/delete", nil, 409)
	}
	write("active", "2000-01-01", nil)
	fieldPost(t, web, cookie, path+"/delete", nil, 409)
	values := url.Values{"customer_id": {fmt.Sprint(other.ID)}, "effective_date": {time.Now().UTC().Format("2006-01-02")}, "reason": {"Place equipment"}, "revision": {"0"}}
	fieldPost(t, web, cookie, path+"/associations/place", values, 409)
	values.Set("customer_id", fmt.Sprint(c.ID))
	fieldPost(t, web, cookie, path+"/associations/place", values, 303)
	// Termination ends the coverage blocker, never the independent placement.
	write("terminated", "2099-01-01", nil)
	fieldPost(t, web, cookie, path+"/delete", nil, 409)
	fieldPost(t, web, cookie, path+"/associations/return", url.Values{"effective_date": {time.Now().UTC().Format("2006-01-02")}, "reason": {"Return equipment"}, "revision": {"1"}}, 303)
	fieldPost(t, web, cookie, path+"/delete", nil, 303)
	fieldPost(t, web, cookie, path+"/restore", nil, 303)
	end := "2000-12-31"
	write("active", "2000-01-01", &end)
	fieldPost(t, web, cookie, path+"/delete", nil, 303)
	// Finalization cannot turn a stale historical/Draft choice into coverage.
	tx, err := pool.Begin(ctx)
	historyCheck(t, err)
	err = svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: admin}, authorization.LeaseDependencies{LeaseID: 63, CustomerID: c.ID, Lifecycle: "scheduled", Coverage: []authorization.LeaseAssetDependency{{AssetID: a.ID, StartsOn: "2099-01-01"}}})
	if err == nil {
		t.Fatal("finalization accepted archived Asset")
	}
	historyCheck(t, tx.Rollback(ctx))
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Archived on")
}

func TestMasterLeaseDraftFinalizationAndAtomicAudit(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Draft owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Draft equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Draft choice").SaveX(ctx)
	svc := authorization.New(pool)
	in := authorization.LeaseDependencies{LeaseID: 63, CustomerID: c.ID, Lifecycle: "draft", Coverage: []authorization.LeaseAssetDependency{{AssetID: a.ID, StartsOn: "2099-01-01"}}}
	write := func(actor authorization.Actor) error {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err := svc.WriteLeaseDependencies(ctx, tx, actor, in); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	historyCheck(t, write(authorization.Actor{UserID: admin}))
	path := fmt.Sprintf("/assets/%d", a.ID)
	fieldPost(t, web, cookie, path+"/delete", nil, 303)
	// Draft reserves no coverage; retaining a stale existing choice is allowed.
	historyCheck(t, write(authorization.Actor{UserID: admin}))
	before := historyDurableRows(t, pool, []string{"lease_master_dependencies", "lease_asset_dependencies", "authorization_audit", "activity_logs"})
	in.Lifecycle = "scheduled"
	if err := write(authorization.Actor{UserID: admin}); err == nil {
		t.Fatal("stale Draft finalized")
	}
	historyEqual(t, "failed finalization preserves projection and audit", historyDurableRows(t, pool, []string{"lease_master_dependencies", "lease_asset_dependencies", "authorization_audit", "activity_logs"}), before)
	in.Lifecycle, in.LeaseID = "draft", 64
	if err := write(authorization.Actor{UserID: admin}); err == nil {
		t.Fatal("new Draft selected archived Asset")
	}
	in.LeaseID = 63
	fieldPost(t, web, cookie, path+"/restore", nil, 303)
	before = historyDurableRows(t, pool, []string{"lease_master_dependencies", "lease_asset_dependencies", "authorization_audit", "activity_logs"})
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_lease_dependency_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.dependencies.change' THEN RAISE EXCEPTION 'injected dependency audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_lease_dependency_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_lease_dependency_audit()`)
	historyCheck(t, err)
	in.Lifecycle = "scheduled"
	if err := write(authorization.Actor{UserID: admin}); err == nil {
		t.Fatal("dependency audit failure committed")
	}
	historyEqual(t, "dependency effect and audit roll back together", historyDurableRows(t, pool, []string{"lease_master_dependencies", "lease_asset_dependencies", "authorization_audit", "activity_logs"}), before)
	_, err = pool.Exec(ctx, `DROP TRIGGER reject_lease_dependency_audit ON authorization_audit; DROP FUNCTION reject_lease_dependency_audit()`)
	historyCheck(t, err)
	if err := write(authorization.Actor{UserID: admin, Channel: "api"}); err == nil {
		t.Fatal("field API gained Lease maintenance")
	}
	historyCheck(t, write(authorization.Actor{UserID: admin}))
	fieldPost(t, web, cookie, path+"/delete", nil, 409)
}

func TestMasterLeaseArchiveCommitOrdersHTTP(t *testing.T) {
	for _, kind := range []string{"asset", "location"} {
		for _, first := range []string{"dependency", "archive"} {
			t.Run(kind+"/"+first, func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race owner").SaveX(ctx)
				var id int64
				coverage := `[]`
				var premises *int64
				table := "assets"
				if kind == "asset" {
					typ := client.AssetType.Create().SetCompanyID(company).SetName("Race equipment").SaveX(ctx)
					id = client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Race asset").SaveX(ctx).ID
					coverage = fmt.Sprintf(`[{"asset_id":%d,"starts_on":"2099-01-01"}]`, id)
				} else {
					id = client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Race site").SaveX(ctx).ID
					premises = &id
					table = "locations"
				}
				cookie := sessionCookie(t, ctx, sessions, admin)
				barrier, err := pool.Begin(ctx)
				historyCheck(t, err)
				defer barrier.Rollback(context.Background())
				var holder int
				historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
				query := `SELECT set_lease_master_dependencies(63,$1,$2,$3,'scheduled',false,$4::jsonb)`
				if first == "dependency" {
					_, err = barrier.Exec(ctx, query, company, c.ID, premises, coverage)
					historyCheck(t, err)
					f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
					done := make(chan *httptest.ResponseRecorder, 1)
					go func() { done <- f.call(creditCommand{path: fmt.Sprintf("/%ss/%d/delete", kind, id)}) }()
					commercialWaiters(t, ctx, pool, holder, 1)
					historyCheck(t, barrier.Commit(ctx))
					if response := <-done; response.Code != 409 {
						t.Fatalf("archive=%d: %s", response.Code, response.Body.String())
					}
				} else {
					_, err = barrier.Exec(ctx, `UPDATE `+table+` SET deleted_at=clock_timestamp() WHERE id=$1`, id)
					historyCheck(t, err)
					done := make(chan error, 1)
					go func() { _, err := pool.Exec(ctx, query, company, c.ID, premises, coverage); done <- err }()
					commercialWaiters(t, ctx, pool, holder, 1)
					historyCheck(t, barrier.Commit(ctx))
					if err := <-done; err == nil {
						t.Fatal("waiting dependency accepted archived master")
					}
				}
				t.Logf("observed %s/%s advisory holder %d and blocked waiter", kind, first, holder)
			})
		}
	}
}

func TestMasterLeaseHookRechecksWaitingGrant(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Hook owner").SaveX(ctx)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Lease writer", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.manage", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Lease writer").SetEmail("lease-writer63@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	probe, err := pool.Begin(ctx)
	historyCheck(t, err)
	err = svc.WriteLeaseDependencies(ctx, probe, authorization.Actor{UserID: u.ID}, authorization.LeaseDependencies{LeaseID: 63, CustomerID: c.ID, Lifecycle: "draft", Archived: true})
	historyCheck(t, probe.Rollback(ctx))
	if err != authorization.ErrForbidden {
		t.Fatalf("Lease manage implied Archive: %v", err)
	}
	var auditBefore int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit`).Scan(&auditBefore))
	barrier, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer barrier.Rollback(context.Background())
	var holder int
	historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
	done := make(chan error, 1)
	go func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			done <- err
			return
		}
		defer tx.Rollback(context.Background())
		err = svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: u.ID}, authorization.LeaseDependencies{LeaseID: 63, CustomerID: c.ID, Lifecycle: "draft"})
		if err == nil {
			err = tx.Commit(ctx)
		}
		done <- err
	}()
	commercialWaiters(t, ctx, pool, holder, 1)
	_, err = barrier.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.manage'`, role)
	historyCheck(t, err)
	historyCheck(t, barrier.Commit(ctx))
	if err := <-done; err != authorization.ErrForbidden {
		t.Fatalf("waiting hook authorization: %v", err)
	}
	var rows, auditAfter int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_master_dependencies`).Scan(&rows))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit`).Scan(&auditAfter))
	if rows != 0 || auditAfter != auditBefore {
		t.Fatal("denied Lease hook changed dependencies or audit")
	}
}
