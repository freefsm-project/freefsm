package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestCommercialConstraintDependencyRecoveryAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		for _, state := range []string{"23514", "23503"} {
			for _, boundary := range []string{"authorization_audit", kind + "s"} {
				t.Run(kind+"/"+state+"/"+boundary, func(t *testing.T) {
					pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
					ctx := context.Background()
					c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recovery Customer").SaveX(ctx)
					j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Recovery Job").SaveX(ctx)
					token, err := sessions.CreateMobile(ctx, admin, "Recovery device")
					if err != nil {
						t.Fatal(err)
					}
					api := apiv1.NewRouter(pool, client, sessions)
					call := func(method, path, body string, want int) string {
						t.Helper()
						r := httptest.NewRequest(method, path, strings.NewReader(body))
						r.Header.Set("Authorization", "Bearer "+token)
						r.Header.Set("Content-Type", "application/json")
						w := httptest.NewRecorder()
						api.ServeHTTP(w, r)
						if w.Code != want {
							t.Errorf("%s %s = %d want %d: %s", method, path, w.Code, want, w.Body.String())
						}
						return w.Body.String()
					}
					op, identity := newOperationKey(), newOperationKey()
					body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Recovery","line_items":[]}`, op, identity, j.ID)
					sql := fmt.Sprintf(`CREATE FUNCTION repair_dependency_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'dependency unavailable' USING ERRCODE='%s'; END $$; CREATE TRIGGER repair_dependency_failure BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION repair_dependency_failure()`, state, boundary)
					if _, err := pool.Exec(ctx, sql); err != nil {
						t.Fatal(err)
					}
					call("POST", "/"+kind+"s", body, 503)
					var count int
					if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM `+kind+`s)+(SELECT count(*) FROM commercial_operations)+(SELECT count(*) FROM commercial_client_identities)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)+(SELECT count(*) FROM issued_document_numbers WHERE document_type=$2)`, op, kind).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count != 0 {
						t.Errorf("dependency failure persisted %d effects/admissions/receipts", count)
					}
					call("GET", "/commercial-operations/"+op, "", 404)
					if _, err := pool.Exec(ctx, `DROP TRIGGER repair_dependency_failure ON `+boundary); err != nil {
						t.Fatal(err)
					}
					accepted := call("POST", "/"+kind+"s", body, 200)
					if replay := call("POST", "/"+kind+"s", body, 200); replay != accepted {
						t.Error("retry receipt changed")
					}
					if got := call("GET", "/commercial-operations/"+op, "", 200); got != accepted {
						t.Error("lookup receipt changed")
					}
					if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+kind+`s`).Scan(&count); err != nil || count != 1 {
						t.Errorf("recovery effects=%d err=%v", count, err)
					}
					// A reused client document identity is a genuine domain conflict, not a dependency error.
					conflict := strings.Replace(body, op, newOperationKey(), 1)
					call("POST", "/"+kind+"s", conflict, 409)
					call("POST", "/"+kind+"s", conflict, 409)
					if err := pool.QueryRow(ctx, `SELECT count(*) FROM commercial_operations WHERE receipt->>'outcome'='commercial_conflict'`).Scan(&count); err != nil || count != 1 {
						t.Errorf("domain rejection receipts=%d err=%v", count, err)
					}
					invalid := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":" ","line_items":[]}`, newOperationKey(), newOperationKey(), j.ID)
					call("POST", "/"+kind+"s", invalid, 422)
					call("POST", "/"+kind+"s", invalid, 422)
					if err := pool.QueryRow(ctx, `SELECT count(*) FROM commercial_operations WHERE receipt->>'outcome'='invalid_document'`).Scan(&count); err != nil || count != 1 {
						t.Errorf("input rejection receipts=%d err=%v", count, err)
					}
				})
			}
		}
	}
}
