package handlers

import (
	"context"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAssetAssociationTransferHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	first := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original owner").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Next owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(first.ID).SetOwnershipClass("customer_owned").SetName("Transfer equipment").SetAssetTypeID(typ.ID).SaveX(ctx)
	path := fmt.Sprintf("/assets/%d/associations/transfer", a.ID)
	values := url.Values{"customer_id": {fmt.Sprint(next.ID)}, "location_id": {"0"}, "effective_date": {time.Now().UTC().Format("2006-01-02")}, "reason": {"Sold to next owner"}, "revision": {"0"}}
	fieldPost(t, router, cookie, path, values, 303)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d/associations", a.ID), 200)
	assertContains(t, body, "Sold to next owner")
	assertContains(t, body, "Original owner")
	assertContains(t, body, "Next owner")
	fieldPost(t, router, cookie, path, values, 409)
}

func TestAssetAssociationPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser placement Customer").SaveX(ctx)
	n := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser destination Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser historical service").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetNumber("BROWSER-035").SetName("Browser association equipment").SetAssetTypeID(typ.ID).SaveX(ctx)
	origin := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetNumber("BROWSER-ORIGIN-035").SetName("Mistaken initial observation").SetAssetTypeID(typ.ID).SaveX(ctx)
	client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Browser placement site").SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/asset_associations.cjs", server.URL, cookie.Value, fmt.Sprint(a.ID), fmt.Sprint(c.ID), fmt.Sprint(n.ID), fmt.Sprint(j.ID), fmt.Sprint(origin.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	if evidence := os.Getenv("ASSET_ASSOCIATION_EVIDENCE"); evidence != "" {
		var schema string
		if err = pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
			t.Fatal(err)
		}
		output, err = exec.CommandContext(ctx, "pg_dump", "--dbname", os.Getenv("FREEFSM_TEST_DATABASE_URL"), "--schema", schema, "--no-owner", "--no-acl", "--file", filepath.Join(evidence, "browser-fixture.sql")).CombinedOutput()
		if err != nil {
			t.Fatalf("retain browser fixture: %v %s", err, output)
		}
	}
}

func TestAssetAssociationHistoricalContextDoesNotReviveAPI(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Former owner").SaveX(ctx)
	n := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE_NEW_CUSTOMER").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("FIELD_EQUIPMENT").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Past service").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Assigned equipment context", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("context35@test").SetName("Field viewer").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, u.ID, "Association device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	get := func(method, path string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	contextPath := fmt.Sprintf("/jobs/%d/master-context", j.ID)
	assertContains(t, get("GET", contextPath, 200), "FIELD_EQUIPMENT")
	second := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("SECOND_CONTEXT").SetAssetTypeID(typ.ID).SaveX(ctx)
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"link"}, "asset_id": {fmt.Sprint(second.ID)}}, 303)
	assertContains(t, get("GET", contextPath, 200), "SECOND_CONTEXT")
	input := url.Values{"customer_id": {fmt.Sprint(n.ID)}, "effective_date": {time.Now().UTC().Format("2006-01-02")}, "reason": {"Ownership departure"}, "revision": {"0"}}
	path := fmt.Sprintf("/assets/%d/associations/transfer", a.ID)
	fieldPost(t, router, cookie, path, input, 303)
	client.Asset.UpdateOneID(a.ID).SetNotes("PRIVATE_NEW_SERVICE_NOTES").SaveX(ctx)
	for _, response := range []string{get("GET", contextPath, 200), requestBody(t, router, sessionCookie(t, ctx, sessions, u.ID), "GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), 200)} {
		assertNotContains(t, response, "FIELD_EQUIPMENT")
		assertNotContains(t, response, "PRIVATE_")
	}
	input.Set("customer_id", fmt.Sprint(c.ID))
	input.Set("revision", "1")
	input.Set("reason", "Real revisit")
	fieldPost(t, router, cookie, path, input, 303)
	assertNotContains(t, get("GET", contextPath, 200), "FIELD_EQUIPMENT")
	// Relinking is explicit, and repairing an older historical link must not
	// silently unlink the distinct current visit to the same Customer.
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"link"}, "asset_id": {fmt.Sprint(a.ID)}}, 303)
	assertContains(t, get("GET", contextPath, 200), "FIELD_EQUIPMENT")
	var originalLink int64
	if err = pool.QueryRow(ctx, `SELECT min(id) FROM job_asset_links WHERE job_id=$1`, j.ID).Scan(&originalLink); err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"unlink"}, "link_id": {fmt.Sprint(originalLink)}}, 303)
	assertContains(t, get("GET", contextPath, 200), "FIELD_EQUIPMENT")
	get("POST", path, 404)
	get("GET", fmt.Sprintf("/assets/%d/associations", a.ID), 404)
	expectStatus(t, router, sessionCookie(t, ctx, sessions, u.ID), "GET", fmt.Sprintf("/assets/%d/associations", a.ID), 404)
}

func TestAssetAssociationIndependentGrantsAtomicAuditAndConcurrencyHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("First").SaveX(ctx)
	n := client.Customer.Create().SetCompanyID(company).SetDisplayName("Second").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Atomic equipment").SetAssetTypeID(typ.ID).SaveX(ctx)
	root := fmt.Sprintf("/assets/%d/associations", a.ID)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Descriptive editor", []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}, {Permission: "asset.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("asset35@test").SetName("Equipment editor").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	limited := sessionCookie(t, ctx, sessions, u.ID)
	input := url.Values{"customer_id": {fmt.Sprint(n.ID)}, "effective_date": {time.Now().UTC().Format("2006-01-02")}, "reason": {"Recorded sale"}, "revision": {"0"}}
	for _, action := range []string{"transfer", "place", "return", "correct"} {
		fieldPost(t, router, limited, root+"/"+action, input, 403)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_association_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='asset.transfer' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_association_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_association_audit()`); err != nil {
		t.Fatal(err)
	}
	fieldPost(t, router, cookie, root+"/transfer", input, 500)
	body := requestBody(t, router, cookie, "GET", root, 200)
	assertNotContains(t, body, "Recorded sale")
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_association_audit ON authorization_audit; DROP FUNCTION reject_association_audit()`); err != nil {
		t.Fatal(err)
	}
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", root+"/transfer", strings.NewReader(input.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	wg.Wait()
	first, second := <-codes, <-codes
	if !(first == 303 && second == 409 || first == 409 && second == 303) {
		t.Fatalf("concurrent transfers: %d %d", first, second)
	}
	body = requestBody(t, router, cookie, "GET", root, 200)
	if strings.Count(body, "Recorded sale") != 2 {
		t.Fatalf("expected original event evidence and correction selector exactly once each")
	}
	var events, audits int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM asset_association_events WHERE asset_id=$1),(SELECT count(*) FROM authorization_audit WHERE target_type='asset' AND target_id=$1 AND action='asset.transfer')`, a.ID).Scan(&events, &audits); err != nil || events != 1 || audits != 1 {
		t.Fatalf("atomic evidence events=%d audits=%d err=%v", events, audits, err)
	}
}

func TestAssetAssociationCommitTimeSessionLossHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetName("No partial placement").SetAssetTypeID(typ.ID).SaveX(ctx)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		input := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "effective_date": {time.Now().UTC().Format("2006-01-02")}, "reason": {"Revoked intent"}, "revision": {"0"}}
		r := httptest.NewRequest("POST", fmt.Sprintf("/assets/%d/associations/place", a.ID), strings.NewReader(input.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w.Code
	}()
	// Observe a real transaction waiting behind the authority fence.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND objid=190019)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request never waited on authority fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != http.StatusForbidden && code != http.StatusUnauthorized {
		t.Fatalf("revoked waiter got %d", code)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM asset_association_events WHERE asset_id=$1`, a.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked action committed evidence: %d %v", count, err)
	}
}

func TestAssetAssociationTransferLinkAndCorrectionRacesHTTP(t *testing.T) {
	for _, race := range []string{"link", "correct"} {
		t.Run(race, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original owner").SaveX(ctx)
			n := client.Customer.Create().SetCompanyID(company).SetDisplayName("New owner").SaveX(ctx)
			typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
			a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Racing equipment").SetAssetTypeID(typ.ID).SaveX(ctx)
			root := fmt.Sprintf("/assets/%d/associations", a.ID)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Racing link").SaveX(ctx)
			paths := []string{root + "/transfer", fmt.Sprintf("/jobs/%d/assets", j.ID)}
			values := []url.Values{{"customer_id": {fmt.Sprint(n.ID)}, "effective_date": {time.Now().UTC().Format("2006-01-02")}, "reason": {"Race transfer"}, "revision": {"0"}}, {"asset_id": {fmt.Sprint(a.ID)}, "action": {"link"}, "save_action_id": {newOperationKey()}, "save_revision": {jobRepairRevision(t, router, cookie, fmt.Sprintf("/jobs/%d/assets", j.ID))}}}
			if race == "correct" {
				values[0].Set("effective_date", "2020-01-01")
				fieldPost(t, router, cookie, paths[0], values[0], 303)
				var event int64
				if err := pool.QueryRow(ctx, `SELECT min(id) FROM asset_association_events WHERE asset_id=$1`, a.ID).Scan(&event); err != nil {
					t.Fatal(err)
				}
				values[0].Set("customer_id", fmt.Sprint(c.ID))
				values[0].Set("effective_date", "2020-02-01")
				values[0].Set("revision", "1")
				paths[1] = root + "/correct"
				values[1] = url.Values{"customer_id": {fmt.Sprint(n.ID)}, "effective_date": {"2020-01-02"}, "reason": {"Race correction"}, "revision": {"1"}, "event_id": {fmt.Sprint(event)}}
			}
			start := make(chan struct{})
			codes := make(chan int, 2)
			var wg sync.WaitGroup
			for i := range paths {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					r := httptest.NewRequest("POST", paths[i], strings.NewReader(values[i].Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(cookie)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, r)
					codes <- w.Code
				}(i)
			}
			close(start)
			wg.Wait()
			first, second := <-codes, <-codes
			if race == "correct" {
				if !(first == 303 && second == 409 || first == 409 && second == 303) {
					t.Fatalf("transfer/correction: %d %d", first, second)
				}
			} else {
				if first != 303 && second != 303 {
					t.Fatalf("transfer/link neither accepted: %d %d", first, second)
				}
				for _, code := range []int{first, second} {
					if code != 303 && code != 404 && code != 409 {
						t.Fatalf("unexpected race outcome %d", code)
					}
				}
				var invalid int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM job_asset_links l JOIN assets a ON a.id=l.asset_id WHERE l.job_id=$1 AND l.operational AND l.customer_id IS DISTINCT FROM a.customer_id`, j.ID).Scan(&invalid); err != nil || invalid != 0 {
					t.Fatalf("race left invalid operational link %d %v", invalid, err)
				}
			}
		})
	}
}

func TestAssetAssociationBackdateRequiresExplicitRelationshipRepairHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original").SaveX(ctx)
	n := client.Customer.Create().SetCompanyID(company).SetDisplayName("Destination").SaveX(ctx)
	loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Original location").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(loc.ID).SetOwnershipClass("customer_owned").SetNumber("BACKDATE-035").SetName("Backdate equipment").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Affected service").SaveX(ctx)
	path := fmt.Sprintf("/assets/%d/associations/transfer", a.ID)
	values := url.Values{"customer_id": {fmt.Sprint(n.ID)}, "location_id": {fmt.Sprint(loc.ID)}, "effective_date": {"2020-01-01"}, "reason": {"Backdated sale"}, "revision": {"0"}}
	fieldPost(t, router, cookie, path, values, 409)
	values.Set("location_id", "0")
	fieldPost(t, router, cookie, path, values, 409)
	assertNotContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d/associations", a.ID), 200), "Backdated sale")
	var link int64
	if err := pool.QueryRow(ctx, `SELECT id FROM job_asset_links WHERE job_id=$1`, j.ID).Scan(&link); err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"unlink"}, "link_id": {fmt.Sprint(link)}}, 303)
	fieldPost(t, router, cookie, path, values, 303)
	got := client.Asset.GetX(ctx, a.ID)
	if got.CustomerID != n.ID || got.LocationID != nil || got.Number != a.Number || got.OwnershipClass != a.OwnershipClass {
		t.Fatal("association action changed identity or carried an incompatible Location")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d", j.ID), 200), "Backdate equipment")
}

func TestAssetAssociationEachGrantWorksIndependentlyHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("First").SaveX(ctx)
	n := client.Customer.Create().SetCompanyID(company).SetDisplayName("Next").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetName("Independent equipment").SetAssetTypeID(typ.ID).SaveX(ctx)
	auth := authorization.New(pool)
	u := client.User.Create().SetCompanyID(company).SetEmail("independent35@test").SetName("Independent actor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	var role, event int64
	for i, step := range []struct {
		action, date string
		customer     int64
	}{{"place", "2020-01-01", c.ID}, {"transfer", "2020-02-01", n.ID}, {"return", "2020-03-01", 0}, {"correct", "2020-01-02", c.ID}} {
		permission := "asset." + step.action
		if step.action == "correct" {
			permission = "asset.history.correct"
		}
		grants := []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}, {Permission: permission, Scope: authorization.All}}
		var err error
		if role == 0 {
			role, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Equipment operator", grants)
		} else {
			role, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Renamed independent operator", grants, int64(i))
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
			t.Fatal(err)
		}
		cookie := sessionCookie(t, ctx, sessions, u.ID)
		values := url.Values{"customer_id": {fmt.Sprint(step.customer)}, "effective_date": {step.date}, "reason": {"Independent " + step.action}, "revision": {fmt.Sprint(i)}}
		if step.action == "correct" {
			values.Set("event_id", fmt.Sprint(event))
		}
		fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/associations/%s", a.ID, step.action), values, 303)
		if i == 0 {
			if err = pool.QueryRow(ctx, `SELECT min(id) FROM asset_association_events WHERE asset_id=$1`, a.ID).Scan(&event); err != nil {
				t.Fatal(err)
			}
		}
		denied := "place"
		if step.action == denied {
			denied = "transfer"
		}
		fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/associations/%s", a.ID, denied), values, 403)
	}
}

func TestAssetAssociationRevisitsAndCorrectionHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Repeat placement").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetName("Returning equipment").SetAssetTypeID(typ.ID).SaveX(ctx)
	root := fmt.Sprintf("/assets/%d/associations", a.ID)
	post := func(action, date, reason string, revision int, customer int64, status int) {
		t.Helper()
		fieldPost(t, router, cookie, root+"/"+action, url.Values{"customer_id": {fmt.Sprint(customer)}, "effective_date": {date}, "reason": {reason}, "revision": {fmt.Sprint(revision)}}, status)
	}
	post("place", "2099-01-01", "Future invalid", 0, c.ID, 409)
	post("place", "2020-01-01", "First placement", 0, c.ID, 303)
	post("return", "2020-02-01", "Warehouse return", 1, 0, 303)
	post("place", "2020-03-01", "Second placement", 2, c.ID, 303)
	currentLocation := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Current same-Customer site").SaveX(ctx)
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d", a.ID), url.Values{"location_id": {fmt.Sprint(currentLocation.ID)}}, 303)
	body := requestBody(t, router, cookie, "GET", root, 200)
	if strings.Count(body, "Repeat placement") < 2 {
		t.Fatal("revisit periods collapsed")
	}
	var event int64
	if err := pool.QueryRow(ctx, `SELECT min(id) FROM asset_association_events WHERE asset_id=$1`, a.ID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	correct := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "effective_date": {"2020-01-02"}, "reason": {"Corrected first effective date"}, "revision": {"3"}, "event_id": {fmt.Sprint(event)}}
	fieldPost(t, router, cookie, root+"/correct", correct, 303)
	currentAsset := client.Asset.GetX(ctx, a.ID)
	if currentAsset.LocationID == nil || *currentAsset.LocationID != currentLocation.ID {
		t.Fatal("correcting a past period reset the unrelated current Location")
	}
	body = requestBody(t, router, cookie, "GET", root, 200)
	for _, text := range []string{"2020-01-01", "2020-01-02", "First placement", "Corrected first effective date", "Warehouse return", "Second placement"} {
		assertContains(t, body, text)
	}
	correct.Set("revision", "4")
	correct.Set("effective_date", "2020-02-02")
	fieldPost(t, router, cookie, root+"/correct", correct, 409)
	correct.Set("effective_date", "2019-12-31")
	correct.Set("customer_id", "0")
	fieldPost(t, router, cookie, root+"/correct", correct, 409)
	periods := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/asset-history", c.ID), 200)
	assertContains(t, periods, "2020-01-02")
	assertContains(t, periods, "2020-03-01")
}

func TestAssetAssociationHistoricalJobLinksHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Former Customer").SaveX(ctx)
	n := client.Customer.Create().SetCompanyID(company).SetDisplayName("New private Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Recorded equipment").SetAssetTypeID(typ.ID).SaveX(ctx)
	b := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Second equipment").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Retained work").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d/assets", j.ID)
	for _, id := range []int64{a.ID, b.ID} {
		masterPost(t, router, cookie, path, url.Values{"asset_id": {fmt.Sprint(id)}, "action": {"link"}}, 303)
	}
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d", j.ID), 200)
	assertContains(t, body, "Recorded equipment")
	assertContains(t, body, "Second equipment")
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/associations/transfer", a.ID), url.Values{"customer_id": {fmt.Sprint(n.ID)}, "effective_date": {time.Now().UTC().Format("2006-01-02")}, "reason": {"Ownership changed"}, "revision": {"0"}}, 303)
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d", j.ID), 200)
	assertContains(t, body, "Historical")
	assertContains(t, body, "Recorded equipment")
	assertNotContains(t, body, "New private Customer")
	masterPost(t, router, cookie, path, url.Values{"asset_id": {fmt.Sprint(a.ID)}, "action": {"link"}}, 404)
}
