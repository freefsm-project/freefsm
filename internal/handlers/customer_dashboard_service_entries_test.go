package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent/status"
)

func TestCustomerDashboardServiceEntriesHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Service dashboard Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Bounded service Job").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	today := time.Now().UTC().Format("2006-01-02")
	serviceEntryPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID), url.Values{"service_date": {today}, "summary": {"Dashboard seal replacement"}, "responsible_user_id": {fmt.Sprint(admin)}}, 303)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), 200)
	assertContains(t, body, "Dashboard seal replacement")
	assertContains(t, body, today)
	assertContains(t, body, "Revision accepted")
	assertNotContains(t, body, "responsible_user_id")
}

func TestCustomerDashboardServiceWaitingRefreshHTTP(t *testing.T) {
	for _, change := range []string{"grant", "archive"} {
		t.Run(change, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Waiting service Customer").SaveX(ctx)
			typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
			a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
			completed := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("job:completed")).FirstX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetStatusID(completed.ID).SetJobType("Waiting service Job").SaveX(ctx)
			adminCookie := sessionCookie(t, ctx, sessions, admin)
			serviceEntryPost(t, router, adminCookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID), url.Values{"service_date": {time.Now().UTC().Format("2006-01-02")}, "summary": {"Previously visible service"}, "responsible_user_id": {fmt.Sprint(admin)}}, 303)
			auth := authorization.New(pool)
			actor := authorization.Actor{UserID: admin}
			role, err := auth.SaveRole(ctx, actor, 0, "Waiting service reader", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "asset.view", Scope: authorization.All}, {Permission: "service_entry.view", Scope: authorization.All}})
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetCompanyID(company).SetName("Waiting reader").SetEmail("waiting-service104@test").SetRole("admin").SetPasswordHash("hash").SaveX(ctx)
			if err = auth.AssignUser(ctx, actor, u.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			path := fmt.Sprintf("/customers/%d", c.ID)
			assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Previously visible service")
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			var blocker int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("GET", path, nil).WithContext(ctx)
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				done <- w
			}()
			waitExportBlocker(t, ctx, pool, blocker)
			if change == "grant" {
				_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='service_entry.view'`, role)
			} else {
				_, err = tx.Exec(ctx, `UPDATE assets SET deleted_at=now() WHERE id=$1`, a.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				if w.Code != 200 {
					t.Fatalf("refresh status=%d: %s", w.Code, w.Body.String())
				}
				assertNotContains(t, w.Body.String(), "Previously visible service")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestCustomerDashboardServicePeriodsAndRevisionHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Returning Customer").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE NEXT CUSTOMER").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("First visit Job").SetNotes("PRIVATE JOB FACET").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	// These are actual successive same-day visits. Unknown legacy link dates
	// must not be backdated just to manufacture a historical test timeline.
	today := time.Now().UTC().Format("2006-01-02")
	path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
	form := url.Values{"service_date": {today}, "summary": {"Superseded first service"}, "responsible_user_id": {fmt.Sprint(admin)}}
	serviceEntryPost(t, router, cookie, path, form, 303)
	var entries []authorization.ServiceEntry
	if err := json.Unmarshal([]byte(requestBody(t, router, cookie, "GET", path+"?format=json", 200)), &entries); err != nil || len(entries) != 1 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	form.Set("revision", entries[0].Revision)
	form.Set("summary", "Corrected first service")
	serviceEntryPost(t, router, cookie, fmt.Sprintf("%s/%d", path, entries[0].ID), form, 303)
	transfer := fmt.Sprintf("/assets/%d/associations/transfer", a.ID)
	fieldPost(t, router, cookie, transfer, url.Values{"customer_id": {fmt.Sprint(next.ID)}, "effective_date": {today}, "reason": {"Sale"}, "revision": {"0"}}, 303)
	nextJob := client.Job.Create().SetCompanyID(company).SetCustomerID(next.ID).SetAssetID(a.ID).SetJobType("PRIVATE NEXT JOB").SaveX(ctx)
	serviceEntryPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", nextJob.ID, a.ID), url.Values{"service_date": {today}, "summary": {"PRIVATE NEXT SERVICE"}, "responsible_user_id": {fmt.Sprint(admin)}}, 303)
	dashboard := fmt.Sprintf("/customers/%d", c.ID)
	body := requestBody(t, router, cookie, "GET", dashboard, 200)
	assertContains(t, body, "Corrected first service")
	assertContains(t, body, "Service Entries for past Assets")
	assertNotContains(t, body, "PRIVATE NEXT")
	fieldPost(t, router, cookie, transfer, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "effective_date": {today}, "reason": {"Revisit"}, "revision": {"1"}}, 303)
	revisit := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Returning visit Job").SaveX(ctx)
	serviceEntryPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", revisit.ID, a.ID), url.Values{"service_date": {today}, "summary": {"Returning service"}, "responsible_user_id": {fmt.Sprint(admin)}}, 303)
	body = requestBody(t, router, cookie, "GET", dashboard, 200)
	for _, summary := range []string{"Corrected first service", "Returning service"} {
		if strings.Count(body, summary) != 1 {
			t.Fatalf("expected one %q", summary)
		}
	}
	for _, forbidden := range []string{"PRIVATE NEXT", "PRIVATE JOB FACET", "Superseded first service", "responsible_user_id"} {
		assertNotContains(t, body, forbidden)
	}
	other := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", next.ID), 200)
	assertContains(t, other, "PRIVATE NEXT SERVICE")
	assertNotContains(t, other, "Corrected first service")
	assertNotContains(t, other, "Returning service")
}

func TestCustomerDashboardServiceFailureAuthorityAndArchiveHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Independent service section").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetNumber("SERVICE-104").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	completed := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("job:completed")).FirstX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetStatusID(completed.ID).SetJobType("Unaffected dashboard work").SaveX(ctx)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	serviceEntryPost(t, router, adminCookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID), url.Values{"service_date": {time.Now().UTC().Format("2006-01-02")}, "summary": {"Currently authorized service"}, "responsible_user_id": {fmt.Sprint(admin)}}, 303)
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "asset.view", Scope: authorization.All}, {Permission: "service_entry.view", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, actor, 0, "Independent history reader", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("History reader").SetEmail("history104@test").SetRole("admin").SetPasswordHash("hash").SaveX(ctx)
	if err = auth.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/customers/%d", c.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Currently authorized service")
	assertNotContains(t, body, fmt.Sprintf(`href="/jobs/%d"`, j.ID))
	if _, err = pool.Exec(ctx, `ALTER TABLE asset_service_revisions RENAME TO dashboard_service_unavailable`); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, adminCookie, "GET", path, 200)
	assertContains(t, body, "Service Entries unavailable")
	assertContains(t, body, "Unaffected dashboard work")
	assertContains(t, body, "SERVICE-104")
	assertContains(t, body, "Retry")
	assertNotContains(t, body, "Currently authorized service")
	assertNotContains(t, body, "No Service Entries")
	if _, err = pool.Exec(ctx, `ALTER TABLE dashboard_service_unavailable RENAME TO asset_service_revisions`); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Currently authorized service")
	if _, err = auth.SaveRole(ctx, actor, role, "Independent history reader", grants[:2], 1); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertNotContains(t, body, "Currently authorized service")
	assertNotContains(t, body, "Service Entries for this Customer")
	if _, err = pool.Exec(ctx, `UPDATE assets SET deleted_at=now() WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, adminCookie, "GET", path, 200)
	assertNotContains(t, body, "Currently authorized service")
	assertNotContains(t, body, "SERVICE-104")
	body = requestBody(t, router, adminCookie, "GET", path+"/asset-history?archived=1", 200)
	assertContains(t, body, "SERVICE-104")
	assertContains(t, body, "Archived")
	assertNotContains(t, body, fmt.Sprintf(`href="/customers/%d/assets/%d/service-entries"`, c.ID, a.ID))
}
