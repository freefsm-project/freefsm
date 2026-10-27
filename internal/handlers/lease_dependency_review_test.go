package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type leaseReviewFixture struct {
	ctx                                context.Context
	pool                               *pgxpool.Pool
	client                             *ent.Client
	web                                http.Handler
	cookie                             *http.Cookie
	company, admin, a, b, asset, event int64
	start, returned, end               string
}

func newLeaseReviewFixture(t *testing.T) *leaseReviewFixture {
	t.Helper()
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	f := &leaseReviewFixture{ctx: ctx, pool: pool, client: client, web: web, cookie: sessionCookie(t, ctx, sessions, admin), company: company, admin: admin}
	f.a = client.Customer.Create().SetCompanyID(company).SetDisplayName("Coverage Customer A").SaveX(ctx).ID
	f.b = client.Customer.Create().SetCompanyID(company).SetDisplayName("Corrected Customer B").SaveX(ctx).ID
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Timeline equipment").SaveX(ctx)
	f.asset = client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Unplaced today, explicit prior placement").SaveX(ctx).ID
	_, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='Pacific/Kiritimati' WHERE company_id=$1`, company)
	historyCheck(t, err)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	f.start, f.returned, f.end = today.AddDate(0, 0, -20).Format(time.DateOnly), today.AddDate(0, 0, -10).Format(time.DateOnly), today.AddDate(0, 0, 10).Format(time.DateOnly)
	path := fmt.Sprintf("/assets/%d/associations/", f.asset)
	fieldPost(t, web, f.cookie, path+"place", url.Values{"customer_id": {fmt.Sprint(f.a)}, "effective_date": {f.start}, "reason": {"Recorded original placement"}, "revision": {"0"}}, 303)
	fieldPost(t, web, f.cookie, path+"return", url.Values{"effective_date": {f.returned}, "reason": {"Recorded actual return"}, "revision": {"1"}}, 303)
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM asset_association_events WHERE asset_id=$1 AND action='place'`, f.asset).Scan(&f.event))
	return f
}

func (f *leaseReviewFixture) dependency() authorization.LeaseDependencies {
	end := f.end
	return authorization.LeaseDependencies{LeaseID: 63, CustomerID: f.a, Lifecycle: "active", Coverage: []authorization.LeaseAssetDependency{{AssetID: f.asset, StartsOn: f.start, EndsOn: &end}}}
}

func (f *leaseReviewFixture) register(actor authorization.Actor, in authorization.LeaseDependencies) error {
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := authorization.New(f.pool).WriteLeaseDependencies(f.ctx, tx, actor, in); err != nil {
		return err
	}
	return tx.Commit(f.ctx)
}

func (f *leaseReviewFixture) correction() creditCommand {
	return creditCommand{path: fmt.Sprintf("/assets/%d/associations/correct", f.asset), values: url.Values{"customer_id": {fmt.Sprint(f.b)}, "effective_date": {f.start}, "event_id": {fmt.Sprint(f.event)}, "reason": {"Explicit factual correction of prior placement"}, "revision": {"2"}}}
}

func (f *leaseReviewFixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"assets", "asset_association_origins", "asset_association_events", "asset_association_origin_revisions", "lease_master_dependencies", "lease_asset_dependencies", "authorization_audit", "activity_logs"} {
		var rows string
		historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]')::text FROM `+pgx.Identifier{table}.Sanitize()+` r`).Scan(&rows))
		out[table] = rows
	}
	return out
}

func TestLeaseReviewHistoricalCorrectionCannotContradictCoverageHTTP(t *testing.T) {
	f := newLeaseReviewFixture(t)
	historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, f.dependency()))
	before := f.snapshot(t)
	command := f.correction()
	fieldPost(t, f.web, f.cookie, command.path, command.values, 409)
	historyEqual(t, "rejected correction preserves original facts, projection and audit", f.snapshot(t), before)
}

func TestLeaseReviewRegistrationChecksRecordedPlacementTimeline(t *testing.T) {
	f := newLeaseReviewFixture(t)
	command := f.correction()
	fieldPost(t, f.web, f.cookie, command.path, command.values, 303)
	before := f.snapshot(t)
	if err := f.register(authorization.Actor{UserID: f.admin}, f.dependency()); err == nil {
		t.Fatal("registration accepted A coverage overlapping B's prior placement while currently unplaced")
	}
	historyEqual(t, "rejected registration is atomic", f.snapshot(t), before)
}

func TestLeaseReviewReturnDateCorrectionCannotExtendIntoCoverageHTTP(t *testing.T) {
	f := newLeaseReviewFixture(t)
	returned, err := time.Parse(time.DateOnly, f.returned)
	historyCheck(t, err)
	in := f.dependency()
	in.CustomerID = f.b
	in.Coverage[0].StartsOn = returned.AddDate(0, 0, 1).Format(time.DateOnly)
	historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
	var event int64
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM asset_association_events WHERE asset_id=$1 AND action='return'`, f.asset).Scan(&event))
	before := f.snapshot(t)
	fieldPost(t, f.web, f.cookie, fmt.Sprintf("/assets/%d/associations/correct", f.asset), url.Values{"event_id": {fmt.Sprint(event)}, "effective_date": {returned.AddDate(0, 0, 2).Format(time.DateOnly)}, "reason": {"Correct recorded return date"}, "revision": {"2"}}, 409)
	historyEqual(t, "date-only correction cannot extend A placement into B coverage", f.snapshot(t), before)
}

func TestLeaseReviewNewSelectionsRequireIndependentView(t *testing.T) {
	for _, missing := range []string{"customer", "location", "asset"} {
		t.Run(missing, func(t *testing.T) {
			f := newLeaseReviewFixture(t)
			loc := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(f.a).SetTitle("Selected premises").SaveX(f.ctx)
			grants := []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.manage", Scope: authorization.All}}
			for _, kind := range []string{"customer", "location", "asset"} {
				if kind != missing {
					grants = append(grants, authorization.Grant{Permission: kind + ".view", Scope: authorization.All})
				}
			}
			svc := authorization.New(f.pool)
			role, err := svc.SaveRole(f.ctx, authorization.Actor{UserID: f.admin}, 0, "Independent selection test", grants)
			historyCheck(t, err)
			u := f.client.User.Create().SetCompanyID(f.company).SetName("Lease manager without selected master").SetEmail("lease-selection-review@test").SetPasswordHash("x").SetRole("admin").SaveX(f.ctx)
			historyCheck(t, svc.AssignUser(f.ctx, authorization.Actor{UserID: f.admin}, u.ID, role))
			in := f.dependency()
			in.PremisesID = &loc.ID
			before := f.snapshot(t)
			if err := f.register(authorization.Actor{UserID: u.ID}, in); err != authorization.ErrNotFound {
				t.Fatalf("missing independent %s View accepted/revealed selection: %v", missing, err)
			}
			historyEqual(t, "denied selection retains facts and audit", f.snapshot(t), before)
			grants = append(grants, authorization.Grant{Permission: missing + ".view", Scope: authorization.All})
			_, err = svc.SaveRole(f.ctx, authorization.Actor{UserID: f.admin}, role, "Independent selection test", grants, 1)
			historyCheck(t, err)
			historyCheck(t, f.register(authorization.Actor{UserID: u.ID}, in))
			var recorded int
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM authorization_audit WHERE action='lease.dependencies.change' AND actor_id=$1 AND role_id=$2 AND target_id=63`, u.ID, role).Scan(&recorded))
			if recorded != 1 {
				t.Fatalf("authorized selected input audit count=%d", recorded)
			}
		})
	}
}

func TestLeaseReviewDateBoundariesAndHistoricalControls(t *testing.T) {
	for _, scenario := range []string{"return-day-start", "return-eve-start", "placement-eve-end", "placement-day-end", "same-day-placement-return", "terminated", "archived-draft", "draft", "past-matching-coverage"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLeaseReviewFixture(t)
			in := f.dependency()
			wantConflict := false
			start, err := time.Parse(time.DateOnly, f.start)
			historyCheck(t, err)
			returned, err := time.Parse(time.DateOnly, f.returned)
			historyCheck(t, err)
			date := func(value time.Time) *string { s := value.Format(time.DateOnly); return &s }
			switch scenario {
			case "return-day-start":
				in.Coverage[0].StartsOn = f.returned
			case "return-eve-start":
				in.Coverage[0].StartsOn = returned.AddDate(0, 0, -1).Format(time.DateOnly)
				wantConflict = true
			case "placement-eve-end":
				in.Coverage[0].StartsOn = start.AddDate(0, 0, -2).Format(time.DateOnly)
				in.Coverage[0].EndsOn = date(start.AddDate(0, 0, -1))
			case "placement-day-end":
				in.Coverage[0].StartsOn = start.AddDate(0, 0, -2).Format(time.DateOnly)
				in.Coverage[0].EndsOn = &f.start
				wantConflict = true
			case "terminated":
				in.Lifecycle = "terminated"
			case "archived-draft":
				in.Lifecycle, in.Archived = "draft", true
			case "draft":
				in.Lifecycle = "draft"
			case "past-matching-coverage":
				in.Coverage[0].EndsOn = date(returned.AddDate(0, 0, -1))
			}
			if scenario != "past-matching-coverage" {
				command := f.correction()
				if scenario == "same-day-placement-return" {
					var returnedEvent int64
					historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM asset_association_events WHERE asset_id=$1 AND action='return'`, f.asset).Scan(&returnedEvent))
					fieldPost(t, f.web, f.cookie, command.path, url.Values{"event_id": {fmt.Sprint(returnedEvent)}, "effective_date": {f.start}, "reason": {"Explicit correction to same-day return"}, "revision": {"2"}}, 303)
					command.values.Set("revision", "3")
				}
				fieldPost(t, f.web, f.cookie, command.path, command.values, 303)
			}
			before := f.snapshot(t)
			err = f.register(authorization.Actor{UserID: f.admin}, in)
			if wantConflict {
				if err == nil {
					t.Fatal("inclusive covered day did not intersect recorded placement")
				}
				historyEqual(t, "boundary denial rolls back", f.snapshot(t), before)
			} else {
				historyCheck(t, err)
				if scenario == "terminated" || scenario == "archived-draft" || scenario == "draft" || scenario == "past-matching-coverage" {
					fieldPost(t, f.web, f.cookie, fmt.Sprintf("/assets/%d/delete", f.asset), nil, 303)
				}
			}
		})
	}
}

func TestLeaseReviewDirectSQLTimelineGuards(t *testing.T) {
	for _, first := range []string{"coverage", "correction"} {
		t.Run(first, func(t *testing.T) {
			f := newLeaseReviewFixture(t)
			if first == "coverage" {
				historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, f.dependency()))
			} else {
				command := f.correction()
				fieldPost(t, f.web, f.cookie, command.path, command.values, 303)
			}
			before := f.snapshot(t)
			tx, err := f.pool.Begin(f.ctx)
			historyCheck(t, err)
			defer tx.Rollback(context.Background())
			if first == "coverage" {
				_, err = tx.Exec(f.ctx, `INSERT INTO asset_association_events(asset_id,event_id,action,effective_date,customer_id,reason,actor_id,before_fact) SELECT $1,$2,'correct',$3,$4,'Explicit SQL correction',$5,to_jsonb(f) FROM asset_association_facts f WHERE asset_id=$1 AND event_id=$2`, f.asset, f.event, f.start, f.b, f.admin)
			} else {
				_, err = tx.Exec(f.ctx, `INSERT INTO lease_master_dependencies(lease_id,company_id,customer_id,lifecycle) VALUES(63,$1,$2,'active')`, f.company, f.a)
				historyCheck(t, err)
				_, err = tx.Exec(f.ctx, `INSERT INTO lease_asset_dependencies(lease_id,asset_id,starts_on,ends_on) VALUES(63,$1,$2,$3)`, f.asset, f.start, f.end)
			}
			historyCheck(t, err) // Constraint validates final transaction state.
			if err := tx.Commit(f.ctx); err == nil {
				t.Fatal("direct SQL committed contradictory coverage/placement history")
			}
			historyEqual(t, "deferred SQL guard rolls back whole transaction", f.snapshot(t), before)
		})
	}
}

func TestLeaseReviewOriginCorrectionRetainsUnknownStartAndOriginalFact(t *testing.T) {
	f := newLeaseReviewFixture(t)
	typ := f.client.Asset.GetX(f.ctx, f.asset).AssetTypeID
	asset := f.client.Asset.Create().SetCompanyID(f.company).SetCustomerID(f.a).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Initially observed placement, start unknown").SaveX(f.ctx)
	f.asset, f.event = asset.ID, 0
	fieldPost(t, f.web, f.cookie, fmt.Sprintf("/assets/%d/associations/return", f.asset), url.Values{"effective_date": {f.returned}, "reason": {"Explicit return after initial observation"}, "revision": {"0"}}, 303)
	in := f.dependency()
	historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
	before := f.snapshot(t)
	command := f.correction()
	command.values.Set("revision", "1")
	fieldPost(t, f.web, f.cookie, command.path, command.values, 409)
	historyEqual(t, "failed origin correction retains original observation", f.snapshot(t), before)
	tx, err := f.pool.Begin(f.ctx)
	historyCheck(t, err)
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(f.ctx, `INSERT INTO asset_association_origin_revisions(asset_id,effective_date,customer_id,reason,actor_id,before_fact) SELECT $1,$2,$3,'Explicit SQL origin correction',$4,to_jsonb(o) FROM asset_association_origin_facts o WHERE asset_id=$1`, f.asset, f.start, f.b, f.admin)
	historyCheck(t, err)
	if err := tx.Commit(f.ctx); err == nil {
		t.Fatal("direct SQL origin correction contradicted live coverage")
	}
	historyEqual(t, "deferred origin guard retains original observation", f.snapshot(t), before)
	// The original row is immutable even when a Terminated reference no longer
	// blocks a legitimate factual correction. No acquisition date is fabricated.
	in.Lifecycle = "terminated"
	historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
	fieldPost(t, f.web, f.cookie, command.path, command.values, 303)
	var preserved bool
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT o.customer_id=$2 AND p.customer_id=$3 AND p.starts_on IS NULL AND a.customer_id IS NULL AND (SELECT count(*) FROM asset_association_origin_revisions WHERE asset_id=$1)=1 FROM asset_association_origins o JOIN asset_association_periods p ON p.asset_id=o.asset_id AND p.event_id=0 JOIN assets a ON a.id=o.asset_id WHERE o.asset_id=$1`, f.asset, f.a, f.b).Scan(&preserved))
	if !preserved {
		t.Fatal("correction rewrote original fact, invented start, or changed current placement")
	}
}

func TestLeaseReviewHistoricalCorrectionRegistrationCommitOrdersHTTP(t *testing.T) {
	for _, first := range []string{"coverage", "correction"} {
		t.Run(first, func(t *testing.T) {
			f := newLeaseReviewFixture(t)
			barrier, err := f.pool.Begin(f.ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var holder int
			command := f.correction()
			httpFixture := &creditMatrix{ctx: f.ctx, pool: f.pool, web: f.web, cookie: f.cookie}
			corrected := make(chan *httptest.ResponseRecorder, 1)
			if first == "coverage" {
				historyCheck(t, barrier.QueryRow(f.ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
				historyCheck(t, authorization.New(f.pool).WriteLeaseDependencies(f.ctx, barrier, authorization.Actor{UserID: f.admin}, f.dependency()))
				go func() { corrected <- httpFixture.call(command) }()
				commercialWaiters(t, f.ctx, f.pool, holder, 1)
				historyCheck(t, barrier.Commit(f.ctx))
				if response := <-corrected; response.Code != 409 {
					t.Fatalf("late historical correction=%d: %s", response.Code, response.Body.String())
				}
			} else {
				historyCheck(t, barrier.QueryRow(f.ctx, `SELECT pg_backend_pid() FROM assets WHERE id=$1 FOR UPDATE`, f.asset).Scan(&holder))
				go func() { corrected <- httpFixture.call(command) }()
				var corrector int
				for corrector == 0 {
					historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT coalesce(min(pid),0) FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid))`, holder, f.pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&corrector))
					if corrector == 0 {
						select {
						case <-f.ctx.Done():
							t.Fatal("correction did not reach row barrier")
						case <-time.After(10 * time.Millisecond):
						}
					}
				}
				registered := make(chan error, 1)
				go func() { registered <- f.register(authorization.Actor{UserID: f.admin}, f.dependency()) }()
				commercialWaiters(t, f.ctx, f.pool, corrector, 1)
				historyCheck(t, barrier.Commit(f.ctx))
				if response := <-corrected; response.Code != 303 {
					t.Fatalf("historical correction=%d: %s", response.Code, response.Body.String())
				}
				if err := <-registered; err == nil {
					t.Fatal("late registration contradicted corrected prior placement")
				}
			}
			var corrections, registrations int
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM authorization_audit WHERE action='asset.history.correct'`).Scan(&corrections))
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM authorization_audit WHERE action='lease.dependencies.change'`).Scan(&registrations))
			if first == "coverage" && (corrections != 0 || registrations != 1) || first == "correction" && (corrections != 1 || registrations != 0) {
				t.Fatalf("wrong atomic outcome correction=%d registration=%d", corrections, registrations)
			}
			t.Logf("observed %s-first historical correction/registration holder=%d", first, holder)
		})
	}
}

func leaseReviewManager(t *testing.T, f *leaseReviewFixture, grants []authorization.Grant) (int64, int64) {
	t.Helper()
	svc := authorization.New(f.pool)
	grants = append([]authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.manage", Scope: authorization.All}}, grants...)
	role, err := svc.SaveRole(f.ctx, authorization.Actor{UserID: f.admin}, 0, "Reviewed Lease manager", grants)
	historyCheck(t, err)
	u := f.client.User.Create().SetCompanyID(f.company).SetName("Lease manager").SetEmail("review-manager@test").SetPasswordHash("x").SetRole("admin").SaveX(f.ctx)
	historyCheck(t, svc.AssignUser(f.ctx, authorization.Actor{UserID: f.admin}, u.ID, role))
	return u.ID, role
}

func TestLeaseReviewRetainedSelectionsDoNotExpandAuthority(t *testing.T) {
	f := newLeaseReviewFixture(t)
	loc := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(f.a).SetTitle("Originally selected premises").SaveX(f.ctx)
	in := f.dependency()
	in.PremisesID = &loc.ID
	historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
	user, _ := leaseReviewManager(t, f, nil)
	actor := authorization.Actor{UserID: user}
	// Keeping a recorded key or revising its end is not a fresh master selection.
	historyCheck(t, f.register(actor, in))
	end, err := time.Parse(time.DateOnly, f.end)
	historyCheck(t, err)
	later := end.AddDate(0, 0, 1).Format(time.DateOnly)
	in.Coverage[0].EndsOn = &later
	historyCheck(t, f.register(actor, in))
	before := f.snapshot(t)
	newInterval := in
	newInterval.Coverage = []authorization.LeaseAssetDependency{{AssetID: f.asset, StartsOn: f.returned, EndsOn: &later}}
	if err := f.register(actor, newInterval); err != authorization.ErrNotFound {
		t.Fatalf("new interval reused old Asset selection authority: %v", err)
	}
	newOwner := in
	newOwner.CustomerID = f.b
	if err := f.register(actor, newOwner); err != authorization.ErrNotFound {
		t.Fatalf("Lease grant selected another Customer: %v", err)
	}
	historyEqual(t, "new selections leave retained facts/audit untouched", f.snapshot(t), before)
	// Removal is authorized by Lease Manage; re-adding a former ID is new input.
	removed := in
	removed.PremisesID, removed.Coverage = nil, nil
	historyCheck(t, f.register(actor, removed))
	for _, target := range []string{"location", "asset"} {
		readd := removed
		if target == "location" {
			readd.PremisesID = in.PremisesID
		} else {
			readd.Coverage = in.Coverage
		}
		if err := f.register(actor, readd); err != authorization.ErrNotFound {
			t.Fatalf("re-added %s bypassed current View: %v", target, err)
		}
	}
}

func TestLeaseReviewRetainedTerminatedHistoryDoesNotRequireRevival(t *testing.T) {
	f := newLeaseReviewFixture(t)
	loc := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(f.a).SetTitle("Historical premises").SaveX(f.ctx)
	in := f.dependency()
	in.Lifecycle, in.PremisesID = "terminated", &loc.ID
	historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
	fieldPost(t, f.web, f.cookie, fmt.Sprintf("/assets/%d/delete", f.asset), nil, 303)
	fieldPost(t, f.web, f.cookie, fmt.Sprintf("/locations/%d/delete", loc.ID), nil, 303)
	_, err := f.pool.Exec(f.ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, f.a)
	historyCheck(t, err)
	user, _ := leaseReviewManager(t, f, nil)
	before := f.snapshot(t)
	historyCheck(t, f.register(authorization.Actor{UserID: user}, in))
	after := f.snapshot(t)
	for _, table := range []string{"assets", "asset_association_origins", "asset_association_events", "asset_association_origin_revisions", "lease_master_dependencies", "lease_asset_dependencies"} {
		historyEqual(t, "retaining Terminated history does not revive/rewrite "+table, after[table], before[table])
	}
	var archived bool
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT c.deleted_at IS NOT NULL AND l.deleted_at IS NOT NULL FROM customers c JOIN locations l ON l.id=$2 WHERE c.id=$1`, f.a, loc.ID).Scan(&archived))
	if !archived {
		t.Fatal("retaining history revived Customer/premises")
	}
}

func TestLeaseReviewWaitingSelectedViewRevocationRollsBack(t *testing.T) {
	for _, kind := range []string{"customer", "location", "asset"} {
		t.Run(kind, func(t *testing.T) {
			f := newLeaseReviewFixture(t)
			loc := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(f.a).SetTitle("Waiting premises").SaveX(f.ctx)
			grants := []authorization.Grant{}
			for _, resource := range []string{"customer", "location", "asset"} {
				grants = append(grants, authorization.Grant{Permission: resource + ".view", Scope: authorization.All})
			}
			user, role := leaseReviewManager(t, f, grants)
			in := f.dependency()
			in.PremisesID = &loc.ID
			before := f.snapshot(t)
			barrier, err := f.pool.Begin(f.ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var holder int
			historyCheck(t, barrier.QueryRow(f.ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
			done := make(chan error, 1)
			go func() { done <- f.register(authorization.Actor{UserID: user}, in) }()
			commercialWaiters(t, f.ctx, f.pool, holder, 1)
			_, err = barrier.Exec(f.ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, kind+".view")
			historyCheck(t, err)
			historyCheck(t, barrier.Commit(f.ctx))
			if err := <-done; err != authorization.ErrNotFound {
				t.Fatalf("waiting %s View revocation=%v", kind, err)
			}
			historyEqual(t, "waiting selection denial is atomic", f.snapshot(t), before)
			t.Logf("observed %s View revocation holder=%d and registration waiter", kind, holder)
		})
	}
}

func TestLeaseReviewForeignAndContextOnlySelectionsDenied(t *testing.T) {
	for _, scenario := range []string{"foreign-customer", "foreign-location", "foreign-asset", "foreign-lease", "context-only"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLeaseReviewFixture(t)
			var foreignCompany int64
			historyCheck(t, f.pool.QueryRow(f.ctx, `INSERT INTO companies(name,slug) VALUES('Foreign Company','lease-review-foreign') RETURNING id`).Scan(&foreignCompany))
			foreignCustomer := f.client.Customer.Create().SetCompanyID(foreignCompany).SetDisplayName("FOREIGN PRIVATE CUSTOMER").SaveX(f.ctx)
			foreignLocation := f.client.Location.Create().SetCompanyID(foreignCompany).SetObjectType("customer").SetObjectID(foreignCustomer.ID).SetTitle("FOREIGN PRIVATE PREMISES").SaveX(f.ctx)
			typ := f.client.AssetType.Create().SetCompanyID(foreignCompany).SetName("Foreign equipment").SaveX(f.ctx)
			foreignAsset := f.client.Asset.Create().SetCompanyID(foreignCompany).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("FOREIGN PRIVATE ASSET").SaveX(f.ctx)
			grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "location.view", Scope: authorization.All}, {Permission: "asset.view", Scope: authorization.All}}
			if scenario == "context-only" {
				grants[2].Scope = authorization.Context
				grants = append(grants, authorization.Grant{Permission: "job.view", Scope: authorization.Assigned})
			}
			user, _ := leaseReviewManager(t, f, grants)
			in := f.dependency()
			switch scenario {
			case "foreign-customer":
				in.CustomerID = foreignCustomer.ID
			case "foreign-location":
				in.PremisesID = &foreignLocation.ID
			case "foreign-asset":
				in.Coverage[0].AssetID = foreignAsset.ID
			case "foreign-lease":
				_, err := f.pool.Exec(f.ctx, `SELECT set_lease_master_dependencies(63,$1,$2,NULL,'terminated',false,'[]'::jsonb)`, foreignCompany, foreignCustomer.ID)
				historyCheck(t, err)
			}
			before := f.snapshot(t)
			if err := f.register(authorization.Actor{UserID: user}, in); err != authorization.ErrNotFound {
				t.Fatalf("%s expanded authority: %v", scenario, err)
			}
			historyEqual(t, "foreign/hidden input denial is atomic", f.snapshot(t), before)
		})
	}
}

func TestLeaseReviewValidTimelineWritesRollbackOnAuditFailure(t *testing.T) {
	for _, action := range []string{"lease.dependencies.change", "asset.history.correct"} {
		t.Run(action, func(t *testing.T) {
			f := newLeaseReviewFixture(t)
			in := f.dependency()
			in.Coverage[0].StartsOn = f.returned // The past correction is valid outside this coverage.
			if action == "asset.history.correct" {
				historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
			}
			before := f.snapshot(t)
			_, err := f.pool.Exec(f.ctx, fmt.Sprintf(`CREATE FUNCTION fail_lease_review_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s' THEN RAISE EXCEPTION 'injected review audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_lease_review_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_lease_review_audit()`, action))
			historyCheck(t, err)
			command := f.correction()
			if action == "asset.history.correct" {
				request := &creditMatrix{ctx: f.ctx, pool: f.pool, web: f.web, cookie: f.cookie}
				if response := request.call(command); response.Code < 400 {
					t.Fatal("correction survived failed immutable audit")
				}
			} else if err := f.register(authorization.Actor{UserID: f.admin}, in); err == nil {
				t.Fatal("registration survived failed immutable audit")
			}
			historyEqual(t, "audit failure rolls back projection and appended facts", f.snapshot(t), before)
			_, err = f.pool.Exec(f.ctx, `DROP TRIGGER fail_lease_review_audit ON authorization_audit; DROP FUNCTION fail_lease_review_audit()`)
			historyCheck(t, err)
			if action == "asset.history.correct" {
				fieldPost(t, f.web, f.cookie, command.path, command.values, 303)
			} else {
				historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
			}
		})
	}
}

func TestLeaseReviewDeferredSQLChecksFinalTerminatedRepair(t *testing.T) {
	f := newLeaseReviewFixture(t)
	historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, f.dependency()))
	tx, err := f.pool.Begin(f.ctx)
	historyCheck(t, err)
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(f.ctx, `INSERT INTO asset_association_events(asset_id,event_id,action,effective_date,customer_id,reason,actor_id,before_fact) SELECT $1,$2,'correct',$3,$4,'Explicit final-state SQL repair',$5,to_jsonb(f) FROM asset_association_facts f WHERE asset_id=$1 AND event_id=$2`, f.asset, f.event, f.start, f.b, f.admin)
	historyCheck(t, err)
	_, err = tx.Exec(f.ctx, `UPDATE lease_master_dependencies SET lifecycle='terminated' WHERE lease_id=63`)
	historyCheck(t, err)
	historyCheck(t, tx.Commit(f.ctx))
	var valid bool
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT e.customer_id=$2 AND f.customer_id=$3 AND a.customer_id IS NULL FROM asset_association_events e JOIN asset_association_facts f ON f.event_id=e.id JOIN assets a ON a.id=e.asset_id WHERE e.id=$1`, f.event, f.a, f.b).Scan(&valid))
	if !valid {
		t.Fatal("final-state repair overwrote original evidence or current placement")
	}
}

func TestLeaseReviewTerminatedReplacementUsesFinalSnapshot(t *testing.T) {
	f := newLeaseReviewFixture(t)
	returned, err := time.Parse(time.DateOnly, f.returned)
	historyCheck(t, err)
	oldEnd := returned.AddDate(0, 0, -1).Format(time.DateOnly)
	in := f.dependency()
	in.Coverage[0].EndsOn = &oldEnd
	historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
	var today time.Time
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT lease_dependency_today($1)`, f.company).Scan(&today))
	fieldPost(t, f.web, f.cookie, fmt.Sprintf("/assets/%d/associations/place", f.asset), url.Values{"customer_id": {fmt.Sprint(f.b)}, "effective_date": {today.Format(time.DateOnly)}, "reason": {"New placement after past coverage ended"}, "revision": {"2"}}, 303)
	// Terminated references have no placement effect. Replacing their projection
	// must validate the final lifecycle, not transient old Active metadata.
	in.Lifecycle = "terminated"
	in.Coverage[0].StartsOn, in.Coverage[0].EndsOn = today.Format(time.DateOnly), &f.end
	historyCheck(t, f.register(authorization.Actor{UserID: f.admin}, in))
	var owner int64
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT customer_id FROM assets WHERE id=$1`, f.asset).Scan(&owner))
	if owner != f.b {
		t.Fatal("Terminated replacement rewrote current placement")
	}
}
