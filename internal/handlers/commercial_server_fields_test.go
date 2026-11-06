package handlers

import (
	"context"
	"fmt"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestCommercialServerChangedFieldsAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Field history").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Field history").SaveX(ctx)
			assignFieldDocumentCreator(t, pool, j.ID, admin)
			token, err := sessions.CreateMobile(ctx, admin, "Changed fields")
			historyCheck(t, err)
			api := apiv1.NewRouter(pool, client, sessions)
			created := matrixState(t, lifecycleCall(t, api, token, "POST", "/"+kind+"s", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": j.ID, "customer_id": c.ID, "title": "Retained fields"}), 200))
			path := fmt.Sprintf("/%ss/%d", kind, created.ID)
			var required, number, option int64
			historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,required) VALUES($1,$2,'New required','text',true) RETURNING id`, company, kind).Scan(&required))
			historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,precision) VALUES($1,$2,'Precision','number',2) RETURNING id`, company, kind).Scan(&number))
			historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,options) VALUES($1,$2,'Selection','select','["Old","Current"]') RETURNING id`, company, kind).Scan(&option))
			read := func() string { return lifecycleCall(t, api, token, "GET", path, "", 200) }
			edit := func(fields map[string]any, want int) string {
				t.Helper()
				before := read()
				fields["operation_id"], fields["job_id"], fields["revision"] = newOperationKey(), j.ID, matrixState(t, before).Revision
				response := lifecycleCall(t, api, token, "PATCH", path, matrixBody(t, fields), want)
				if want != 200 && read() != before {
					t.Fatal("invalid field edit partially persisted")
				}
				return response
			}
			value := func(id int64, v string) map[string]any {
				return map[string]any{"custom_fields": []map[string]any{{"definition_id": id, "value": v}}}
			}
			// Newly required unrelated values never block a pending offline patch.
			edit(map[string]any{"notes": "Unrelated requiredness deferred"}, 200)
			edit(value(number, "1.23"), 200)
			edit(value(required, ""), 422)
			edit(value(number, "1.234"), 422)
			edit(value(option, "Old"), 200)
			_, err = pool.Exec(ctx, `UPDATE custom_field_definitions SET retired_options='["Old"]' WHERE id=$1`, option)
			historyCheck(t, err)
			assertContains(t, read(), `"value":"Old"`)
			edit(value(option, "Old"), 200) // retained selection is not newly selecting it
			edit(value(option, "Current"), 200)
			edit(value(option, "Old"), 422)
			_, err = pool.Exec(ctx, `UPDATE custom_field_definitions SET retired=true WHERE id=$1`, number)
			historyCheck(t, err)
			assertContains(t, read(), `"value":"1.23"`)
			edit(value(number, "2.00"), 422)
			edit(map[string]any{"terms": "Retired value retained"}, 200)
			_, err = pool.Exec(ctx, `UPDATE custom_field_definitions SET retired=false,precision=1 WHERE id=$1`, number)
			historyCheck(t, err)
			edit(value(number, "2.34"), 422)
			edit(value(number, "2.3"), 200)
			// Field activity records the changed values, not a restorable full version.
			var before, after string
			historyCheck(t, pool.QueryRow(ctx, `SELECT detail->'before'->>'custom_fields',detail->'after'->>'custom_fields' FROM authorization_audit WHERE target_type=$1 AND target_id=$2 AND action=$1||'.edit' AND detail->'after' ? 'custom_fields' ORDER BY id DESC LIMIT 1`, kind, created.ID).Scan(&before, &after))
			assertContains(t, before, "1.23")
			assertContains(t, after, "2.3")
		})
	}
}

func TestCommercialServerEditAtomicFailuresAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		for _, boundary := range []string{"effect", "audit", "receipt"} {
			t.Run(kind+"/"+boundary, func(t *testing.T) {
				pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic edit").SaveX(ctx)
				j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Atomic edit").SaveX(ctx)
				assignFieldDocumentCreator(t, pool, j.ID, admin)
				token, err := sessions.CreateMobile(ctx, admin, "Atomic edit")
				historyCheck(t, err)
				api := apiv1.NewRouter(pool, client, sessions)
				created := matrixState(t, lifecycleCall(t, api, token, "POST", "/"+kind+"s", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": j.ID, "customer_id": c.ID, "title": "Before failure"}), 200))
				path := fmt.Sprintf("/%ss/%d", kind, created.ID)
				before := lifecycleCall(t, api, token, "GET", path, "", 200)
				op := newOperationKey()
				body := matrixBody(t, map[string]any{"operation_id": op, "job_id": j.ID, "revision": created.Revision, "title": "After retry", "notes": "Atomic notes"})
				table, event := kind+"s", "UPDATE"
				if boundary == "audit" {
					table, event = "authorization_audit", "INSERT"
				}
				if boundary == "receipt" {
					table, event = "commercial_operations", "INSERT"
				}
				_, err = pool.Exec(ctx, `CREATE FUNCTION matrix_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected storage failure' USING ERRCODE='23514'; END $$; CREATE TRIGGER matrix_failure BEFORE `+event+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION matrix_failure()`)
				historyCheck(t, err)
				lifecycleCall(t, api, token, "PATCH", path, body, 503)
				lifecycleCall(t, api, token, "GET", "/commercial-operations/"+op, "", 404)
				if lifecycleCall(t, api, token, "GET", path, "", 200) != before {
					t.Fatal("failed write changed document/revision")
				}
				var count int
				historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM authorization_audit WHERE action=$1 AND target_id=$2)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$3)`, kind+".edit", created.ID, op).Scan(&count))
				if count != 0 {
					t.Fatal("failed write left audit or admission")
				}
				_, err = pool.Exec(ctx, `DROP TRIGGER matrix_failure ON `+table)
				historyCheck(t, err)
				accepted := lifecycleCall(t, api, token, "PATCH", path, body, 200)
				if replay := lifecycleCall(t, api, token, "PATCH", path, body, 200); replay != accepted {
					t.Fatal("retry receipt changed")
				}
				historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action=$1 AND target_id=$2`, kind+".edit", created.ID).Scan(&count))
				if count != 1 {
					t.Fatalf("retry has %d effects", count)
				}
			})
		}
	}
}
