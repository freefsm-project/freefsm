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

func TestMasterArchivedCoordinatesRequireViewAndRemainReadOnlyHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Coordinate owner").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Archived coordinate target").SetAddress1("PRIVATE_COORDINATE_ADDRESS").SetLatitude(51.5).SetLongitude(-0.12).SaveX(ctx)
	path := fmt.Sprintf("/locations/%d", l.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, adminCookie, path+"/delete", nil, 303)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Ordinary Location editor", []authorization.Grant{{Permission: "location.view", Scope: authorization.All}, {Permission: "location.edit", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Editor").SetEmail("coordinates63@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	expectStatus(t, web, cookie, "GET", path+"/coordinates", 404)
	body := requestBody(t, web, adminCookie, "GET", path+"/coordinates", 200)
	assertContains(t, body, "PRIVATE_COORDINATE_ADDRESS")
	assertNotContains(t, body, ">Accept coordinates</button>")
	assertNotContains(t, body, ">Look up current address</button>")
	var revision string
	historyCheck(t, pool.QueryRow(ctx, `SELECT xmin::text FROM locations WHERE id=$1`, l.ID).Scan(&revision))
	before := historyDurableRows(t, pool, []string{"locations", "authorization_audit", "activity_logs"})
	fieldPost(t, web, adminCookie, path+"/coordinates", url.Values{"revision": {revision}, "latitude": {"42"}, "longitude": {"10"}}, 404)
	fieldPost(t, web, adminCookie, path+"/coordinates/lookup", url.Values{"revision": {revision}}, 409)
	historyEqual(t, "archived coordinate writes and lookup leave history intact", historyDurableRows(t, pool, []string{"locations", "authorization_audit", "activity_logs"}), before)
}

func TestMasterAssetLocationChoicesExcludeArchiveButRetainCurrentHTTP(t *testing.T) {
	_, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Picker owner").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("ARCHIVED_PICKER_LOCATION").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(l.ID).SetOwnershipClass("customer_owned").SetAssetTypeID(typ.ID).SetName("Retained location Asset").SaveX(ctx)
	fieldPost(t, web, cookie, fmt.Sprintf("/locations/%d/delete", l.ID), nil, 303)
	body := requestBody(t, web, cookie, "GET", fmt.Sprintf("/assets/locations?customer_id=%d&selected=%d", c.ID, l.ID), 200)
	assertNotContains(t, body, "ARCHIVED_PICKER_LOCATION")
	path := fmt.Sprintf("/assets/%d", a.ID)
	body = requestBody(t, web, cookie, "GET", path+"/edit", 200)
	assertContains(t, body, "Current Location (unavailable for new selection)")
	fieldPost(t, web, cookie, path, url.Values{"name": {"Retained after unrelated edit"}, "location_id": {fmt.Sprint(l.ID)}}, 303)
	got := client.Asset.GetX(ctx, a.ID)
	if got.LocationID == nil || *got.LocationID != l.ID {
		t.Fatal("unrelated edit forced Location clearing or restoration")
	}
}

func TestMasterCoordinatesArchiveCommitOrdersHTTP(t *testing.T) {
	for _, first := range []string{"archive", "coordinates"} {
		t.Run(first, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Coordinate race owner").SaveX(ctx)
			l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Coordinate race place").SetLatitude(1).SetLongitude(2).SaveX(ctx)
			var revision string
			historyCheck(t, pool.QueryRow(ctx, `SELECT xmin::text FROM locations WHERE id=$1`, l.ID).Scan(&revision))
			barrier, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var holder int
			f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
			path := fmt.Sprintf("/locations/%d", l.ID)
			edited := make(chan *httptest.ResponseRecorder, 1)
			command := creditCommand{path: path + "/coordinates", values: url.Values{"revision": {revision}, "latitude": {"42"}, "longitude": {"10"}}}
			if first == "archive" {
				historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
				_, err = barrier.Exec(ctx, `UPDATE locations SET deleted_at=clock_timestamp() WHERE id=$1`, l.ID)
				historyCheck(t, err)
				go func() { edited <- f.call(command) }()
				commercialWaiters(t, ctx, pool, holder, 1)
				historyCheck(t, barrier.Commit(ctx))
				if response := <-edited; response.Code != 404 {
					t.Fatalf("late coordinates=%d: %s", response.Code, response.Body.String())
				}
			} else {
				historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM locations WHERE id=$1 FOR UPDATE`, l.ID).Scan(&holder))
				go func() { edited <- f.call(command) }()
				var editor int
				for editor == 0 {
					historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(min(pid),0) FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid))`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&editor))
					if editor == 0 {
						select {
						case <-ctx.Done():
							t.Fatal("coordinate writer did not reach row barrier")
						case <-time.After(10 * time.Millisecond):
						}
					}
				}
				archived := make(chan *httptest.ResponseRecorder, 1)
				go func() { archived <- f.call(creditCommand{path: path + "/delete"}) }()
				commercialWaiters(t, ctx, pool, editor, 1)
				historyCheck(t, barrier.Commit(ctx))
				if response := <-edited; response.Code != 303 {
					t.Fatalf("coordinates=%d: %s", response.Code, response.Body.String())
				}
				if response := <-archived; response.Code != 303 {
					t.Fatalf("archive=%d: %s", response.Code, response.Body.String())
				}
			}
			var lat float64
			var archived bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT latitude,deleted_at IS NOT NULL FROM locations WHERE id=$1`, l.ID).Scan(&lat, &archived))
			want := 1.0
			if first == "coordinates" {
				want = 42
			}
			if lat != want || !archived {
				t.Fatalf("invalid ordered result: latitude=%g archived=%v", lat, archived)
			}
			t.Logf("observed %s-first coordinate/archive holder=%d", first, holder)
		})
	}
}
