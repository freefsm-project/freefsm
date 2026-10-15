package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestServiceEntryFormerResponsibleBrowserRoundTrip(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Service editor without directory", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}, {Permission: "service_entry.view", Scope: authorization.Assigned}, {Permission: "service_entry.create", Scope: authorization.Assigned}, {Permission: "service_entry.revise", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	editor := client.User.Create().SetCompanyID(company).SetEmail("responsible-editor@test").SetName("Editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	departed := client.User.Create().SetCompanyID(company).SetEmail("departed-responsible@test").SetName("Original colleague").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	for _, id := range []int64{editor.ID, departed.ID} {
		if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, id, role); err != nil {
			t.Fatal(err)
		}
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Responsible Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Responsible service").SaveX(ctx)
	for _, id := range []int64{editor.ID, departed.ID} {
		client.JobAssignment.Create().SetJobID(j.ID).SetUserID(id).SaveX(ctx)
	}
	path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	for _, entry := range []struct {
		summary string
		users   []string
	}{{"Both responsible", []string{fmt.Sprint(editor.ID), fmt.Sprint(departed.ID)}}, {"Departed solely responsible", []string{fmt.Sprint(departed.ID)}}, {"Editor solely responsible", []string{fmt.Sprint(editor.ID)}}} {
		serviceEntryPost(t, router, adminCookie, path, url.Values{"service_date": {time.Now().UTC().Format("2006-01-02")}, "summary": {entry.summary}, "responsible_user_id": entry.users}, 303)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2;`, j.ID, departed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE users SET name='Renamed former colleague',is_active=false WHERE id=$1`, departed.ID); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, editor.ID)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(runCtx, "node", "testdata/browser/service-entry-responsibles.cjs", server.URL, cookie.Value, path, fmt.Sprint(editor.ID), fmt.Sprint(departed.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}

func TestServiceEntryFreshAPIResponsibleIdentityRoundTrip(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Fresh service editor", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}, {Permission: "service_entry.view", Scope: authorization.Assigned}, {Permission: "service_entry.revise", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	editor := client.User.Create().SetCompanyID(company).SetEmail("fresh-editor@test").SetName("Editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, editor.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Fresh read Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Editable Job").SaveX(ctx)
	other := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Other contributing Job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(editor.ID).SaveX(ctx)
	var expected []int64
	var createIDs []string
	for i := 0; i < 3; i++ {
		u := client.User.Create().SetCompanyID(company).SetEmail(fmt.Sprintf("prior-%d@test", i)).SetName("Duplicate colleague").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
		if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
			t.Fatal(err)
		}
		client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
		expected = append(expected, u.ID)
		createIDs = append(createIDs, fmt.Sprint(u.ID))
	}
	path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
	cookie := sessionCookie(t, ctx, sessions, admin)
	serviceEntryPost(t, router, cookie, path, url.Values{"service_date": {time.Now().UTC().Format("2006-01-02")}, "summary": {"Created by another user"}, "responsible_user_id": createIDs}, 303)
	serviceEntryPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", other.ID, a.ID), url.Values{"service_date": {time.Now().UTC().Format("2006-01-02")}, "summary": {"Cross-Job bounded history"}, "responsible_user_id": createIDs}, 303)
	if _, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id<>$2`, j.ID, editor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE users SET name='Renamed former colleague',is_active=false WHERE id=$1`, expected[2]); err != nil {
		t.Fatal(err)
	}
	// This new authenticated client never saw either creation command or receipt.
	token, err := sessions.CreateMobile(ctx, editor.ID, "Fresh responsible reader")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	type record struct {
		ID               int64    `json:"id"`
		Revision         string   `json:"revision"`
		ServiceDate      string   `json:"service_date"`
		Summary          string   `json:"summary"`
		ResponsibleIDs   []int64  `json:"responsible_user_ids"`
		ResponsibleNames []string `json:"responsible_names"`
	}
	read := func() (int64, []record) {
		t.Helper()
		response := serviceAPIRequest(api, token, "GET", path, nil)
		if response.Code != 200 {
			t.Fatalf("read=%d %s", response.Code, response.Body.String())
		}
		assertNotContains(t, response.Body.String(), `"job_id"`)
		var envelope struct {
			Data struct {
				LinkID  int64    `json:"link_id"`
				Entries []record `json:"entries"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope.Data.Entries) != 2 {
			t.Fatalf("entries=%+v", envelope.Data.Entries)
		}
		return envelope.Data.LinkID, envelope.Data.Entries
	}
	link, entries := read()
	fresh := entries[0]
	if !reflect.DeepEqual(fresh.ResponsibleIDs, expected) {
		t.Fatalf("fresh editable read omitted stable responsible identities: got%v want%v", fresh.ResponsibleIDs, expected)
	}
	if !reflect.DeepEqual(fresh.ResponsibleNames, []string{"Duplicate colleague", "Duplicate colleague", "Renamed former colleague"}) {
		t.Fatalf("names=%v", fresh.ResponsibleNames)
	}
	if entries[1].ResponsibleIDs != nil {
		t.Fatal("cross-Job history exposed editing identities")
	}
	contextPage := serviceAPIRequest(api, token, "GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), nil)
	if contextPage.Code != 200 {
		t.Fatalf("context=%d", contextPage.Code)
	}
	assertNotContains(t, contextPage.Body.String(), `"responsible_user_ids"`)
	// Construct every retained service field from this read, never createIDs.
	command := map[string]any{"operation_id": newOperationKey(), "link_id": link, "revision": fresh.Revision, "service_date": fresh.ServiceDate, "summary": "Fresh client summary correction", "responsible_user_ids": fresh.ResponsibleIDs}
	response := serviceAPIRequest(api, token, "POST", fmt.Sprintf("%s/%d", path, fresh.ID), command)
	if response.Code != 200 {
		t.Fatalf("fresh revision=%d %s", response.Code, response.Body.String())
	}
	_, saved := read()
	if saved[0].Summary != "Fresh client summary correction" || !reflect.DeepEqual(saved[0].ResponsibleIDs, expected) {
		t.Fatalf("saved responsibility changed: %+v", saved[0])
	}
	if _, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='service_entry.revise'`, role); err != nil {
		t.Fatal(err)
	}
	_, readonly := read()
	for _, entry := range readonly {
		if entry.ResponsibleIDs != nil {
			t.Fatal("read-only projection exposed editing identities")
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'service_entry.revise','J')`, role); err != nil {
		t.Fatal(err)
	}
	_, editable := read()
	if !reflect.DeepEqual(editable[0].ResponsibleIDs, expected) {
		t.Fatal("restored revision authority did not restore edit projection")
	}
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"unlink"}, "link_id": {fmt.Sprint(link)}}, 303)
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"link"}, "asset_id": {fmt.Sprint(a.ID)}}, 303)
	_, relinked := read()
	for _, entry := range relinked {
		if entry.ResponsibleIDs != nil {
			t.Fatal("lost original relationship exposed editable identities")
		}
	}
}
