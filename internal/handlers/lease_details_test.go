package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestLeaseDraftIncompleteDetailsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Draft details Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Selected premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Intended only").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "title": {"External duplicate title"}, "reference": {"REF"}, "starts_on": {"2030-12-31"}, "ends_on": {"2030-01-01"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	body := requestBody(t, web, cookie, "GET", path, 200)
	assertContains(t, body, "External duplicate title")
	assertContains(t, body, "2030-12-31")
	fieldPost(t, web, cookie, fmt.Sprintf("/locations/%d/delete", l.ID), nil, 409)
	var clean bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM lease_asset_dependencies) AND NOT EXISTS(SELECT 1 FROM invoices) AND EXISTS(SELECT 1 FROM assets WHERE id=$1 AND customer_id IS NULL)`, a.ID).Scan(&clean))
	if !clean {
		t.Fatal("Draft details reserved coverage, moved an Asset, or created billing")
	}
}

func TestLeaseFourCollaborationsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Collaboration Customer").SaveX(ctx)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	grants := []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.manage", Scope: authorization.All}}
	for _, key := range []string{"comment.add", "file.upload", "file.download", "tag.attach", "tag.detach", "lease.billing.view", "lease.billing.manage", "lease.archive", "lease.archived.view", "lease.restore"} {
		grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
	}
	for _, key := range []string{"comment.remove", "file.rename", "file.remove"} {
		grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.Own})
	}
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Lease collaborator", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Ordinary collaborator").SetEmail("lease-collaboration65@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie = sessionCookie(t, ctx, sessions, u.ID)
	fieldPost(t, web, cookie, path+"/comments", url.Values{"content": {"Lease discussion"}}, 200)
	var comment int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM comments WHERE object_type='lease' AND object_id=$1`, id).Scan(&comment))
	fieldPost(t, web, cookie, path+"/comments", url.Values{"content": {"One depth reply"}, "reply_to": {fmt.Sprint(comment)}}, 200)
	var reply int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM comments WHERE reply_to=$1`, comment).Scan(&reply))
	fieldPost(t, web, cookie, path+"/comments", url.Values{"content": {"Nested forbidden"}, "reply_to": {fmt.Sprint(reply)}}, 409)
	fieldPost(t, web, cookie, fmt.Sprintf("%s/comments/%d/delete", path, comment), nil, 200)
	assertContains(t, requestBody(t, web, cookie, "GET", path+"/comments", 200), "One depth reply")
	tag := client.Tag.Create().SetCompanyID(company).SetName("Lease tag").SaveX(ctx)
	fieldPost(t, web, cookie, fmt.Sprintf("%s/tags/%d/attach", path, tag.ID), nil, 200)
	uploadRouter := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		q.Set("object_type", "lease")
		r.URL.RawQuery = q.Encode()
		web.ServeHTTP(w, r)
	})
	response := uploadFileHTTP(t, uploadRouter, cookie, id, "agreement.txt", []byte("Externally agreed terms"))
	if response.Code != 200 {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	var file int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM files WHERE object_type='lease' AND object_id=$1`, id).Scan(&file))
	field := client.CustomFieldDefinition.Create().SetCompanyID(company).SetObjectType("lease").SetName("Equipment notes").SetFieldType("text").SetRequired(true).SaveX(ctx)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, fmt.Sprintf("custom_field_%d", field.ID): {"Recorded field"}, "agreement_file_id": {fmt.Sprint(file)}}, 303)
	body := requestBody(t, web, cookie, "GET", path, 200)
	for _, text := range []string{"Lease tag", "Recorded field", "agreement.txt", "/comments"} {
		assertContains(t, body, text)
	}
	assertContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/files/%d", file), 200), "Externally agreed terms")
	fieldPost(t, web, cookie, fmt.Sprintf("%s/tags/%d/detach", path, tag.ID), nil, 200)
	bad := uploadFileHTTP(t, uploadRouter, cookie, id, "pretend.png", []byte("not a PNG"))
	if bad.Code < 400 {
		t.Fatal("invalid content accepted")
	}
	// The broad mobile actor still has no Lease details or maintenance surface.
	token, err := sessions.CreateMobile(ctx, u.ID, "Lease denial device")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewLeaseDirectory(objectref.NewEntDirectory(client), pool), t.TempDir(), 0))
	for _, route := range []string{"/leases", "/leases/new", path, path + "/billing", path + "/comments", path + "/custom-fields", path + "/history", path + "/billing/history", path + "/delete", path + "/restore", fmt.Sprintf("%s/tags/%d/attach", path, tag.ID), fmt.Sprintf("/files/%d/metadata", file), fmt.Sprintf("/files/%d", file), fmt.Sprintf("/files/%d/rename", file), fmt.Sprintf("/files/%d/remove", file)} {
		for _, method := range []string{"GET", "POST"} {
			body := `{}`
			if method == "POST" && strings.HasPrefix(route, "/files/") && (strings.HasSuffix(route, "/rename") || strings.HasSuffix(route, "/remove")) {
				name := ""
				if strings.HasSuffix(route, "/rename") {
					name = "denied.txt"
				}
				body = fmt.Sprintf(`{"operation_id":%q,"occurred_at":%q,"object_type":"lease","object_id":%d,"revision":%q,"name":%q}`, newOperationKey(), time.Now().UTC().Format(time.RFC3339Nano), id, services.FileRevision(client.File.GetX(ctx, file)), name)
			}
			r := httptest.NewRequest(method, route, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if w.Code != 404 && w.Code != 405 {
				t.Fatalf("API %s %s: %d %s", method, route, w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "Recorded field") || strings.Contains(w.Body.String(), "agreement.txt") {
				t.Fatal("API denied response leaked content")
			}
		}
	}
	fieldPost(t, web, cookie, fmt.Sprintf("/files/%d/rename", file), url.Values{"original_name": {"renamed-agreement.txt"}, "revision": {"1"}, "redirect": {path}}, 200)
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "renamed-agreement.txt")
	fieldPost(t, web, cookie, fmt.Sprintf("/files/%d/delete", file), url.Values{"revision": {"2"}, "redirect": {path}}, 200)
	requestBody(t, web, cookie, "GET", fmt.Sprintf("/files/%d", file), 404)
	base, err := svc.DraftLeaseBase(ctx, authorization.Actor{UserID: u.ID}, id)
	historyCheck(t, err)
	if base.AgreementFileID != nil {
		t.Fatal("removed agreement remained an accessible base reference")
	}
}

func TestLeaseRequiredFieldsDefaultsAndRetirementHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Fields Customer").SaveX(ctx)
	f := client.CustomFieldDefinition.Create().SetCompanyID(company).SetObjectType("lease").SetName("Required answer").SetFieldType("checkbox").SetRequired(true).SaveX(ctx)
	d := client.CustomFieldDefinition.Create().SetCompanyID(company).SetObjectType("lease").SetName("Creation default").SetFieldType("text").SetDefaultValue("at creation").SaveX(ctx)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "at creation")
	rejected := fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "title": {"Rejected title"}}, 422)
	assertContains(t, rejected.Body.String(), "Required answer")
	assertContains(t, rejected.Body.String(), `value="Rejected title"`)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, fmt.Sprintf("custom_field_%d", f.ID): {"false"}}, 303)
	_, err := pool.Exec(ctx, `UPDATE custom_field_definitions SET default_value='must not backfill' WHERE id=$1`, d.ID)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `UPDATE custom_field_definitions SET retired=true WHERE id=$1`, f.ID)
	historyCheck(t, err)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"1"}, "title": {"Retired field history"}}, 303)
	body := requestBody(t, web, cookie, "GET", path, 200)
	assertContains(t, body, "at creation")
	assertContains(t, body, "Retired")
	assertContains(t, body, "No")
	if strings.Contains(body, "must not backfill") || strings.Contains(body, fmt.Sprintf(`name="custom_field_%d"`, f.ID)) {
		t.Fatal("default backfilled or retired field remained editable")
	}
	fieldPost(t, web, cookie, path, url.Values{"revision": {"2"}, fmt.Sprintf("custom_field_%d", f.ID): {"true"}}, 422)
}

func TestLeaseDetailsAuditFailureAndPremisesRaceHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic details").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Racing premises").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	values := url.Values{"revision": {"0"}, "title": {"must roll back"}, "premises_id": {fmt.Sprint(l.ID)}}
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_lease_details_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.details.change' THEN RAISE EXCEPTION 'injected details audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_lease_details_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_lease_details_audit()`)
	historyCheck(t, err)
	tables := []string{"leases", "lease_master_dependencies", "authorization_audit"}
	before := historyDurableRows(t, pool, tables)
	fieldPost(t, web, cookie, path, values, 409)
	historyEqual(t, "base and projection audit rollback", historyDurableRows(t, pool, tables), before)
	_, err = pool.Exec(ctx, `DROP TRIGGER reject_lease_details_audit ON authorization_audit`)
	historyCheck(t, err)
	barrier, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer barrier.Rollback(context.Background())
	var holder int
	historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
	f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.call(creditCommand{path: path, values: values}) }()
	commercialWaiters(t, ctx, pool, holder, 1)
	_, err = barrier.Exec(ctx, `UPDATE locations SET deleted_at=clock_timestamp() WHERE id=$1`, l.ID)
	historyCheck(t, err)
	historyCheck(t, barrier.Commit(ctx))
	if response := <-done; response.Code != 409 {
		t.Fatalf("racing stale selection: %d", response.Code)
	}
	historyEqual(t, "waiting stale premises has no details effect", historyDurableRows(t, pool, tables), before)
	t.Logf("observed details waiter blocked by Location archive holder %d", holder)
}

func TestLeaseBillingIndependentFacetsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Facets Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Selected").SaveX(ctx)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, adminCookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, adminCookie, path, url.Values{"revision": {"0"}, "title": {"Base private title"}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	svc := authorization.New(pool)
	for _, facet := range []string{"base", "billing"} {
		keys := []string{"lease.view", "lease.manage"}
		if facet == "billing" {
			keys = []string{"lease.billing.view", "lease.billing.manage", "customer.view", "comment.add", "file.upload", "file.download", "tag.attach"}
		}
		grants := []authorization.Grant{}
		for _, key := range keys {
			grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
		}
		role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Lease "+facet, grants)
		historyCheck(t, err)
		u := client.User.Create().SetCompanyID(company).SetName(facet).SetEmail(facet + "65@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
		historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
		cookie := sessionCookie(t, ctx, sessions, u.ID)
		if facet == "billing" {
			fieldPost(t, web, cookie, path+"/billing", url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(a.ID)}, "initial_cents": {"1234567"}, "rent_cents": {"7654321"}, "tax_rate": {"12.3456"}}, 303)
			body := requestBody(t, web, cookie, "GET", path+"/billing", 200)
			assertContains(t, body, "7654321")
			if strings.Contains(body, "Base private title") || strings.Contains(body, "comments") {
				t.Fatal("financial-only view exposed base content")
			}
			requestBody(t, web, cookie, "GET", path, 404)
			requestBody(t, web, cookie, "GET", path+"/comments", 404)
			fieldPost(t, web, cookie, path+"/comments", url.Values{"content": {"Financial-only cannot collaborate"}}, 404)
		} else {
			requestBody(t, web, cookie, "GET", path+"/billing", 404)
			fieldPost(t, web, cookie, path+"/billing", url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(a.ID)}, "rent_cents": {"1"}}, 403)
		}
	}
	body := requestBody(t, web, adminCookie, "GET", path, 200)
	for _, secret := range []string{"1234567", "7654321", "12.3456", "rent_cents", "tax_rate"} {
		if strings.Contains(body, secret) {
			t.Fatalf("base response leaked %s", secret)
		}
	}
}
