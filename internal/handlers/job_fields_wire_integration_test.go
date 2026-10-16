package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestJobFieldsExactNestedWireBeforeAdmissionHTTP(t *testing.T) {
	zero := `"00000000-0000-0000-0000-000000000000"`
	for _, batch := range []bool{false, true} {
		for _, tc := range []struct{ name, values, revisions string }{
			{"case", `[{"definition_id":2,"VALUE":"2.00"}]`, `{"2":` + zero + `}`},
			{"unknown", `[{"definition_id":2,"value":"2.00","extra":true}]`, `{"2":` + zero + `}`},
			{"duplicate-value", `[{"definition_id":2,"value":"2.00","value":"3.00"}]`, `{"2":` + zero + `}`},
			{"duplicate-id", `[{"definition_id":1,"definition_id":2,"value":"2.00"}]`, `{"2":` + zero + `}`},
			{"duplicate-revision", `[{"definition_id":2,"value":"2.00"}]`, `{"2":` + zero + `,"2":` + zero + `}`},
			{"null-revision", `[{"definition_id":2,"value":"2.00"}]`, `{"2":` + zero + `,"9":null}`},
			{"null-entry", `[null]`, `{"2":` + zero + `}`},
			{"boolean-value", `[{"definition_id":2,"value":false}]`, `{"2":` + zero + `}`},
			{"number-value", `[{"definition_id":2,"value":2}]`, `{"2":` + zero + `}`},
			{"null-selection", `[{"definition_id":2,"value":[null]}]`, `{"2":` + zero + `}`},
			{"duplicate-selection", `[{"definition_id":2,"value":["A","A"]}]`, `{"2":` + zero + `}`},
			{"missing-value", `[{"definition_id":2}]`, `{"2":` + zero + `}`},
			{"domain-invalid-continuation", `[{"definition_id":2,"value":"not-number"}]`, `{"2":` + zero + `}`},
		} {
			t.Run(fmt.Sprintf("batch=%t/%s", batch, tc.name), func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				cookie := sessionCookie(t, ctx, sessions, admin)
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Wire customer").SaveX(ctx)
				j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Wire job").SaveX(ctx)
				fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"First"}, "object_type": {"job"}, "field_type": {"text"}}, 303)
				fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Second"}, "object_type": {"job"}, "field_type": {"number"}, "precision": {"2"}}, 303)
				token, err := sessions.CreateMobile(ctx, admin, "Wire client")
				if err != nil {
					t.Fatal(err)
				}
				api := apiv1.NewRouter(pool, client, sessions)
				body := fmt.Sprintf(`{"operation_id":"82000000-0000-4000-8000-000000000021","revisions":%s,"values":%s}`, tc.revisions, tc.values)
				method, path := "PATCH", fmt.Sprintf("/jobs/%d/custom-fields", j.ID)
				if batch {
					method, path = "POST", "/sync/operations"
					body = fmt.Sprintf(`{"operations":[{"operation_id":"82000000-0000-4000-8000-000000000020","type":"job.fields","target":"%d","depends_on":[],"revisions":{"1":%s},"values":[{"definition_id":1,"value":"must not persist"}]},{"operation_id":"82000000-0000-4000-8000-000000000021","type":"job.fields","target":"%d","depends_on":[],"revisions":%s,"values":%s}]}`, j.ID, zero, j.ID, tc.revisions, tc.values)
				}
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				want := 400
				if tc.name == "domain-invalid-continuation" {
					want = 409
					if batch {
						want = 200
					}
					assertContains(t, w.Body.String(), `"reason":"invalid_number"`)
				}
				if w.Code != want {
					t.Errorf("malformed wire: %d %s", w.Code, w.Body.String())
				}
				var values string
				var admissions, receipts, audits, revisions int
				if err := pool.QueryRow(ctx, `SELECT custom_fields::text,(SELECT count(*) FROM operation_admissions WHERE operation_type='job.fields'),(SELECT count(*) FROM job_field_operations),(SELECT count(*) FROM authorization_audit WHERE action='job.fields.edit'),(SELECT count(*) FROM job_field_revisions WHERE job_id=$1) FROM jobs WHERE id=$1`, j.ID).Scan(&values, &admissions, &receipts, &audits, &revisions); err != nil {
					t.Fatal(err)
				}
				if tc.name == "domain-invalid-continuation" {
					wantReceipts, wantEffects := 1, 0
					if batch {
						wantReceipts, wantEffects = 2, 1
					}
					if admissions != wantReceipts || receipts != wantReceipts || audits != wantEffects || revisions != wantEffects {
						t.Errorf("domain outcome lost atomic continuation: %s admissions=%d receipts=%d audits=%d revisions=%d", values, admissions, receipts, audits, revisions)
					}
					if batch {
						assertContains(t, values, "must not persist")
						assertNotContains(t, values, "not-number")
					}
				} else if values != "[]" || admissions != 0 || receipts != 0 || audits != 0 || revisions != 0 {
					t.Errorf("malformed envelope changed durable state: values=%s admissions=%d receipts=%d audits=%d revisions=%d", values, admissions, receipts, audits, revisions)
				}
			})
		}
	}
}
