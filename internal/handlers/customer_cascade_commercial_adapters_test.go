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

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestCustomerCascadeCommercialCopyAndFieldAdapters(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Legacy owner").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Retained Job copy source").SaveX(ctx)
			copy := func() error {
				if kind == "estimate" {
					_, err := services.NewEstimateService(client).CreateFromJob(ctx, j.ID, services.NewStatusService(client), "0")
					return err
				}
				_, err := services.NewInvoiceService(client, pool).CreateFromJob(ctx, j.ID, services.NewStatusService(client), "0")
				return err
			}
			historyCheck(t, copy())
			var id int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM `+kind+`s`).Scan(&id))
			token, err := sessions.CreateMobile(ctx, admin, "Commercial guard field device")
			historyCheck(t, err)
			api := apiv1.NewRouter(pool, client, sessions)
			call := func(method, path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				return w
			}
			path := fmt.Sprintf("/%ss/%d", kind, id)
			var document struct {
				Data struct {
					Revision string `json:"revision"`
				} `json:"data"`
			}
			response := call("GET", path, "")
			if response.Code != 200 {
				t.Fatal(response.Body.String())
			}
			historyCheck(t, json.Unmarshal(response.Body.Bytes(), &document))
			// Explicit legacy setup, not an assertion that today's cascade allows
			// active work. A current writer must reject even this retained state.
			_, err = client.Customer.UpdateOneID(c.ID).SetDeletedAt(time.Now()).Save(ctx)
			historyCheck(t, err)
			before := cascadeCommercialFacts(t, pool)
			if err := copy(); err == nil {
				t.Fatal("copy created operational document under archived owner")
			}
			historyEqual(t, "copy denial retains captured source and number history", cascadeCommercialFacts(t, pool), before)
			createBody := func() string {
				return fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Field creation","line_items":[]}`, newOperationKey(), newOperationKey(), j.ID)
			}
			for _, mutation := range []struct{ method, path, body string }{
				{"POST", "/" + kind + "s", createBody()},
				{"PATCH", path, fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"notes":"Field edit"}`, newOperationKey(), j.ID, document.Data.Revision)},
			} {
				result := call(mutation.method, mutation.path, mutation.body)
				if result.Code != 409 && result.Code != 404 {
					t.Fatalf("archived owner API %s=%d: %s", mutation.method, result.Code, result.Body.String())
				}
				historyEqual(t, "field denial commits no commercial/number/audit effects", cascadeCommercialFacts(t, pool), before)
			}
			masterPost(t, web, cookie, fmt.Sprintf("/customers/%d/restore", c.ID), nil, 303)
			historyCheck(t, copy())
			result := call("POST", "/"+kind+"s", createBody())
			if result.Code != 200 {
				t.Fatalf("restored API create=%d %s", result.Code, result.Body.String())
			}
			response = call("GET", path, "")
			historyCheck(t, json.Unmarshal(response.Body.Bytes(), &document))
			result = call("PATCH", path, fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"notes":"Restored explicit field edit"}`, newOperationKey(), j.ID, document.Data.Revision))
			if result.Code != 200 {
				t.Fatalf("restored API edit=%d %s", result.Code, result.Body.String())
			}
		})
	}
}

func TestCustomerCascadeCommercialAuditAndWaitingAuthorityHTTP(t *testing.T) {
	for _, kind := range []string{"invoice", "estimate"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Content authority").SaveX(ctx)
			adminCookie := sessionCookie(t, ctx, sessions, admin)
			fieldPost(t, web, adminCookie, "/"+kind+"s", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Unchanged content"}, "line_items": {"[]"}}, 303)
			var id int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM `+kind+`s`).Scan(&id))
			path := fmt.Sprintf("/%ss/%d", kind, id)
			role, err := authorization.New(pool).SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Commercial custodian", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: kind + ".view", Scope: authorization.All}, {Permission: kind + ".edit", Scope: authorization.All}})
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetEmail("commercial-custodian@test").SetName("Custodian").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
			historyCheck(t, authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Proposed content"}}
			if kind == "invoice" {
				var n int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_number FROM invoices WHERE id=$1`, id).Scan(&n))
				values.Set("invoice_number", fmt.Sprint(n))
			}
			values = commercialReviewedValues(t, web, cookie, path, values)
			_, err = pool.Exec(ctx, `CREATE FUNCTION reject_commercial_guard_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('invoice.edit','estimate.edit') THEN RAISE EXCEPTION 'injected commercial audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_commercial_guard_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_commercial_guard_audit()`)
			historyCheck(t, err)
			before := cascadeCommercialFacts(t, pool)
			f := &creditMatrix{pool: pool, web: web, cookie: cookie, ctx: ctx}
			result := f.call(creditCommand{path, values})
			if result.Code < 400 {
				t.Fatal("audit failure committed details")
			}
			historyEqual(t, "audit failure rolls back model-store write", cascadeCommercialFacts(t, pool), before)
			_, err = pool.Exec(ctx, `DROP TRIGGER reject_commercial_guard_audit ON authorization_audit`)
			historyCheck(t, err)
			values.Set("save_action_id", newOperationKey())
			tx, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer tx.Rollback(context.Background())
			_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, kind+".edit")
			historyCheck(t, err)
			var holder int
			historyCheck(t, tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder))
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- f.call(creditCommand{path, values}) }()
			commercialWaiters(t, ctx, pool, holder, 1)
			t.Logf("observed %s details waiting on grant revocation holder=%d", kind, holder)
			historyCheck(t, tx.Commit(ctx))
			result = <-done
			if result.Code != 403 {
				t.Fatalf("revoked edit=%d: %s", result.Code, result.Body.String())
			}
			historyEqual(t, "waiting revoked edit has no commercial/audit effects", cascadeCommercialFacts(t, pool), before)
		})
	}
}
