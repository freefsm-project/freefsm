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

func TestLeaseMinimalRegistrationHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("External agreement customer").SaveX(ctx)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET lease_prefix='RENT-',next_lease_number=42 WHERE company_id=$1`, company)
	historyCheck(t, err)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases WHERE company_id=$1`, company).Scan(&id))
	body := requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/%d", id), 200)
	assertContains(t, body, "RENT-00042")
	assertContains(t, body, "Draft")
	// Registration immediately protects the Customer through the existing guard.
	fieldPost(t, web, cookie, fmt.Sprintf("/customers/%d/delete", c.ID), nil, 409)
	var clean bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_master_dependencies WHERE lease_id=$1 AND customer_id=$2 AND lifecycle='draft' AND NOT archived) AND NOT EXISTS(SELECT 1 FROM lease_asset_dependencies WHERE lease_id=$1) AND NOT EXISTS(SELECT 1 FROM invoices WHERE company_id=$3)`, id, c.ID, company).Scan(&clean))
	if !clean {
		t.Fatal("minimal registration did not preserve Draft dependency/no billing contract")
	}
}

func TestLeaseRegistrationCurrentAuthorityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Selected customer").SaveX(ctx)
	svc := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.manage", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Lease registration", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Actual current grants").SetEmail("lease65@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}}
	fieldPost(t, web, cookie, "/leases", values, 303)
	for i, missing := range []string{"lease.manage", "lease.view", "customer.view"} {
		t.Run(missing, func(t *testing.T) {
			var keep []authorization.Grant
			for _, g := range grants {
				if g.Permission != missing && !(missing == "lease.view" && g.Permission == "lease.manage") {
					keep = append(keep, g)
				}
			}
			_, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Lease registration", keep, int64(i+1))
			historyCheck(t, err)
			before := historyDurableRows(t, pool, []string{"leases", "lease_master_dependencies", "issued_document_numbers", "company_settings", "authorization_audit"})
			status := 403
			if missing == "customer.view" {
				status = 404
			}
			fieldPost(t, web, cookie, "/leases", values, status)
			historyEqual(t, "denied registration changes nothing", historyDurableRows(t, pool, []string{"leases", "lease_master_dependencies", "issued_document_numbers", "company_settings", "authorization_audit"}), before)
		})
	}
}

func TestLeaseRegistrationAuditFailureHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Audit rollback").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_registration_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.create' THEN RAISE EXCEPTION 'injected registration audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_registration_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_registration_audit()`)
	historyCheck(t, err)
	snapshot := func() string {
		var result string
		historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_build_object('leases',(SELECT jsonb_agg(l) FROM leases l),'dependencies',(SELECT jsonb_agg(d) FROM lease_master_dependencies d),'coverage',(SELECT jsonb_agg(d) FROM lease_asset_dependencies d),'numbers',(SELECT jsonb_agg(n) FROM issued_document_numbers n),'settings',(SELECT jsonb_agg(s) FROM company_settings s),'audit',(SELECT jsonb_agg(a ORDER BY id) FROM authorization_audit a),'activity',(SELECT jsonb_agg(a ORDER BY id) FROM activity_logs a))::text`).Scan(&result))
		return result
	}
	before := snapshot()
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 409)
	historyEqual(t, "audit failure rolls registration and numbering back atomically", snapshot(), before)
}

func TestLeaseRegistrationConcurrentNumbersHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Concurrent registrations").SaveX(ctx)
	f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: sessionCookie(t, ctx, sessions, admin)}
	barrier, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer barrier.Rollback(context.Background())
	var holder int
	historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			done <- f.call(creditCommand{path: "/leases", values: url.Values{"customer_id": {fmt.Sprint(c.ID)}}})
		}()
	}
	commercialWaiters(t, ctx, pool, holder, 2)
	historyCheck(t, barrier.Commit(ctx))
	for range 2 {
		if response := <-done; response.Code != 303 {
			t.Fatalf("registration: %d %s", response.Code, response.Body.String())
		}
	}
	var labels []string
	historyCheck(t, pool.QueryRow(ctx, `SELECT array_agg(number_label ORDER BY document_number) FROM leases`).Scan(&labels))
	historyEqual(t, "concurrent issued labels", labels, []string{"LSE-00001", "LSE-00002"})
	t.Logf("observed two registration waiters behind advisory holder %d", holder)
}

func TestLeaseRegistrationRechecksWaitingCustomerHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archived while registration waits").SaveX(ctx)
	f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: sessionCookie(t, ctx, sessions, admin)}
	barrier, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer barrier.Rollback(context.Background())
	var holder int
	historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- f.call(creditCommand{path: "/leases", values: url.Values{"customer_id": {fmt.Sprint(c.ID)}}})
	}()
	commercialWaiters(t, ctx, pool, holder, 1)
	_, err = barrier.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, c.ID)
	historyCheck(t, err)
	historyCheck(t, barrier.Commit(ctx))
	if response := <-done; response.Code != 409 {
		t.Fatalf("waiting registration: %d %s", response.Code, response.Body.String())
	}
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM leases`).Scan(&count))
	if count != 0 {
		t.Fatal("archived Customer acquired a new Draft")
	}
	t.Logf("observed registration waiter behind Customer archive holder %d", holder)
}

func TestLeaseRegistrationRechecksWaitingAuthorityHTTP(t *testing.T) {
	for _, change := range []string{"grant", "identity"} {
		t.Run(change, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Authority race").SaveX(ctx)
			svc := authorization.New(pool)
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Race writer", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.manage", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}})
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetName("Waiting writer").SetEmail("lease-race65@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
			historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: sessionCookie(t, ctx, sessions, u.ID)}
			barrier, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var holder int
			historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- f.call(creditCommand{path: "/leases", values: url.Values{"customer_id": {fmt.Sprint(c.ID)}}})
			}()
			commercialWaiters(t, ctx, pool, holder, 1)
			if change == "grant" {
				_, err = barrier.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.manage'`, role)
			} else {
				_, err = barrier.Exec(ctx, `UPDATE users SET is_active=false WHERE id=$1`, u.ID)
			}
			historyCheck(t, err)
			historyCheck(t, barrier.Commit(ctx))
			if response := <-done; response.Code != 403 {
				t.Fatalf("waiting registration: %d %s", response.Code, response.Body.String())
			}
			var untouched bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM leases) AND NOT EXISTS(SELECT 1 FROM issued_document_numbers WHERE document_type='lease') AND NOT EXISTS(SELECT 1 FROM authorization_audit WHERE action='lease.create')`).Scan(&untouched))
			if !untouched {
				t.Fatal("waiting registration committed after authority loss")
			}
			t.Logf("observed %s revocation with registration blocked by holder %d", change, holder)
		})
	}
}

func TestLeaseRegistrationImmutableIdentityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original Customer").SaveX(ctx)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Wrong Customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "number_label": {"FORGED"}, "document_number": {"999"}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	for _, query := range []string{
		fmt.Sprintf(`UPDATE leases SET customer_id=%d WHERE id=$1`, other.ID),
		`UPDATE leases SET number_label='FORGED' WHERE id=$1`,
		`UPDATE leases SET document_number=999 WHERE id=$1`,
		`DELETE FROM leases WHERE id=$1`,
	} {
		if _, err := pool.Exec(ctx, query, id); err == nil {
			t.Fatalf("accepted identity rewrite: %s", query)
		}
	}
	assertContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/%d", id), 200), "LSE-00001")
	// Native dependency projection cannot drift independently from the agreement.
	if _, err := pool.Exec(ctx, `UPDATE lease_master_dependencies SET archived=true WHERE lease_id=$1`, id); err == nil {
		t.Fatal("native Lease and dependency archive states diverged")
	}
	if _, err := authorization.New(pool).RegisterDraftLease(ctx, authorization.Actor{UserID: admin, Channel: "api"}, c.ID); err != authorization.ErrForbidden {
		t.Fatalf("field API channel registered Lease: %v", err)
	}
}
