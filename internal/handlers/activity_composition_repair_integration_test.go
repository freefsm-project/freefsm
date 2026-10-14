package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/jackc/pgx/v5/pgxpool"
)

func activityCompositionSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var value string
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
	'customers',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM customers r),
	'jobs',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM jobs r),
	'estimates',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM estimates r),
	'invoices',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM invoices r),
	'audit',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM authorization_audit r),
	'activity',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM activity_logs r))::text`).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestActivityCompositionSeparateFieldsHTTP(t *testing.T) {
	for _, kind := range []string{"customer", "estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) { activityCompositionFieldsHTTP(t, kind) })
	}
}

func activityCompositionFieldsHTTP(t *testing.T, kind string) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Field history customer").SaveX(ctx)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Observation"}, "object_type": {kind}, "field_type": {"text"}}, 303)
	var definition int64
	if err := pool.QueryRow(ctx, `SELECT id FROM custom_field_definitions WHERE object_type=$1 AND name='Observation'`, kind).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("custom_field_%d", definition)
	id := c.ID
	if kind != "customer" {
		fieldPost(t, web, cookie, "/"+kind+"s", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Field history document"}, "invoice_date": {"2026-10-13"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
		if err := pool.QueryRow(ctx, `SELECT id FROM `+kind+`s`).Scan(&id); err != nil {
			t.Fatal(err)
		}
	}
	base := fmt.Sprintf("/%ss/%d", kind, id)
	fieldPost(t, web, cookie, base+"/custom-fields", url.Values{key: {"Before observation"}}, 303)
	fieldPost(t, web, cookie, base+"/custom-fields", url.Values{key: {"After observation"}}, 303)
	filter := url.Values{"object_type": {kind}, "object_id": {fmt.Sprint(id)}}
	for _, path := range []string{base + "/activity", "/activity?" + filter.Encode()} {
		body := requestBody(t, web, cookie, "GET", path, 200)
		assertContains(t, body, "Before observation")
		assertContains(t, body, "After observation")
	}
	filter.Set("scope", "A")
	filter.Set("operation_id", newOperationKey())
	exportPath := masterPost(t, web, cookie, "/activity/export", filter, 303)
	if worked, err := exports.Product(pool).ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("export worker: %v %v", worked, err)
	}
	rows, err := csv.NewReader(strings.NewReader(requestBody(t, web, cookie, "GET", exportPath+"/download", 200))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	matches := 0
	for _, row := range rows[1:] {
		for _, column := range row {
			if strings.Contains(column, "Before observation") && strings.Contains(column, "After observation") {
				matches++
			}
		}
	}
	if matches != 1 {
		t.Fatalf("expected one exported changed-field effect, got %d", matches)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION activity_composition_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected activity failure'; END $$; CREATE TRIGGER activity_composition_fail BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION activity_composition_fail()`); err != nil {
		t.Fatal(err)
	}
	values := url.Values{key: {"Must roll back"}}
	if kind != "customer" {
		values = commercialReviewedValues(t, web, cookie, base, values)
	}
	before := activityCompositionSnapshot(t, pool)
	w := jobRepairRawPost(web, cookie, base+"/custom-fields", values)
	if w.Code < 400 {
		t.Fatalf("history failure returned success: %d", w.Code)
	}
	if after := activityCompositionSnapshot(t, pool); before != after {
		t.Fatal("field/history failure partially persisted")
	}
}

func TestActivityCompositionCommercialHTTP(t *testing.T) {
	for _, reassign := range []bool{false, true} {
		for _, fault := range []bool{false, true} {
			t.Run(fmt.Sprintf("reassign=%t/fault=%t", reassign, fault), func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				cookie := sessionCookie(t, ctx, sessions, admin)
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original customer").SaveX(ctx)
				c2 := client.Customer.Create().SetCompanyID(company).SetDisplayName("Replacement customer").SaveX(ctx)
				j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reassignment work").SaveX(ctx)
				ids := map[string]int64{}
				for _, kind := range []string{"estimate", "invoice"} {
					values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Original title"}, "invoice_date": {"2026-10-13"}, "line_items": {"[]"}, "tax_rate": {"0"}}
					documentTransactionRequest(t, web, cookie, "POST", "/"+kind+"s", values, 303)
					var id int64
					if err := pool.QueryRow(ctx, `SELECT id FROM `+kind+`s`).Scan(&id); err != nil {
						t.Fatal(err)
					}
					ids[kind] = id
				}
				if fault {
					// Fail the second document's history during reassignment: the
					// first document's event and all three records must roll back.
					if _, err := pool.Exec(ctx, `CREATE FUNCTION activity_composition_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.object_type='invoice' THEN RAISE EXCEPTION 'injected activity failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER activity_composition_fail BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION activity_composition_fail()`); err != nil {
						t.Fatal(err)
					}
				}
				paths := []string{"estimate", "invoice"}
				if reassign {
					paths = []string{"job"}
				}
				for _, kind := range paths {
					id := ids[kind]
					values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Changed title"}, "invoice_date": {"2026-10-13"}, "line_items": {"[]"}, "tax_rate": {"0"}}
					if kind == "invoice" {
						var number int64
						if err := pool.QueryRow(ctx, `SELECT invoice_number FROM invoices WHERE id=$1`, id).Scan(&number); err != nil {
							t.Fatal(err)
						}
						values.Set("invoice_number", fmt.Sprint(number))
					}
					if reassign {
						id = j.ID
						values = url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c2.ID)}, "job_type": {"Reassignment work"}, "replace_customer": {"true"}}
					}
					path := fmt.Sprintf("/%ss/%d", kind, id)
					values = commercialReviewedValues(t, web, cookie, path, values)
					before := activityCompositionSnapshot(t, pool)
					w := jobRepairRawPost(web, cookie, path, values)
					if fault && (reassign || kind == "invoice") {
						if w.Code < 400 {
							t.Fatalf("history failure returned success: %d", w.Code)
						}
						if activityCompositionSnapshot(t, pool) != before {
							t.Fatal("commercial/history failure partially persisted")
						}
						continue
					}
					if w.Code != 303 {
						t.Fatalf("save: %d %s", w.Code, w.Body.String())
					}
				}
				if fault {
					return
				}
				for kind, id := range ids {
					var count int
					key, old, next := "title", "Original title", "Changed title"
					if reassign {
						key, old, next = "customer_id", fmt.Sprint(c.ID), fmt.Sprint(c2.ID)
					}
					if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE company_id=$1 AND object_type=$2 AND object_id=$3 AND metadata->'before'->>$4=$5 AND metadata->'after'->>$4=$6`, company, kind, id, key, old, next).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count != 1 {
						t.Fatalf("%s: want exactly one tenant-bound effect, got %d", kind, count)
					}
					var total, reduced int
					if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE action='updated' AND NOT (metadata->'after' ? 'company_id')) FROM activity_logs WHERE object_type=$1 AND object_id=$2`, kind, id).Scan(&total, &reduced); err != nil {
						t.Fatal(err)
					}
					if total != 2 || reduced != 1 {
						t.Fatalf("%s: expected creation plus one reduced edit, got total=%d reduced=%d", kind, total, reduced)
					}
					for _, path := range []string{fmt.Sprintf("/%ss/%d/activity", kind, id), fmt.Sprintf("/activity?object_type=%s&object_id=%d", kind, id)} {
						body := requestBody(t, web, cookie, "GET", path, 200)
						for _, value := range []string{key, old, next} {
							assertContains(t, body, value)
						}
					}
				}
				if reassign {
					// Historical anchors need independent broad Views, just like
					// commercial-history; parent View must not disclose them.
					u := client.User.Create().SetCompanyID(company).SetName("Document reader").SetEmail("activity-doc-reader@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
					auth := authorization.New(pool)
					role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Documents without anchors", []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "activity.export", Scope: authorization.All}})
					if err != nil {
						t.Fatal(err)
					}
					if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
						t.Fatal(err)
					}
					limited := sessionCookie(t, ctx, sessions, u.ID)
					for kind, id := range ids {
						filter := url.Values{"object_type": {kind}, "object_id": {fmt.Sprint(id)}}
						for _, path := range []string{fmt.Sprintf("/%ss/%d/activity", kind, id), "/activity?" + filter.Encode()} {
							body := requestBody(t, web, limited, "GET", path, 200)
							assertNotContains(t, body, "customer_id")
							assertNotContains(t, body, "job_id")
						}
						filter.Set("scope", "A")
						filter.Set("operation_id", newOperationKey())
						exportPath := masterPost(t, web, limited, "/activity/export", filter, 303)
						if worked, err := exports.Product(pool).ProcessOne(ctx); err != nil || !worked {
							t.Fatalf("limited export: %v %v", worked, err)
						}
						body := requestBody(t, web, limited, "GET", exportPath+"/download", 200)
						assertNotContains(t, body, "customer_id")
						assertNotContains(t, body, "job_id")
					}
					broad := []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "activity.export", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}, {Permission: "job.view", Scope: authorization.All}}
					broadRole, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Documents with broad anchors", broad)
					if err != nil {
						t.Fatal(err)
					}
					if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, broadRole); err != nil {
						t.Fatal(err)
					}
					filter := url.Values{"object_type": {"estimate"}, "object_id": {fmt.Sprint(ids["estimate"])}, "scope": {"A"}, "operation_id": {newOperationKey()}}
					exportPath := masterPost(t, web, limited, "/activity/export", filter, 303)
					if worked, err := exports.Product(pool).ProcessOne(ctx); err != nil || !worked {
						t.Fatalf("broad export: %v %v", worked, err)
					}
					assertContains(t, requestBody(t, web, limited, "GET", exportPath+"/download", 200), "job_id")
					if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID); err != nil {
						t.Fatal(err)
					}
					broad[len(broad)-1].Scope = authorization.Assigned
					assignedRole, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Documents with assigned Job view", broad)
					if err != nil {
						t.Fatal(err)
					}
					if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, assignedRole); err != nil {
						t.Fatal(err)
					}
					requestBody(t, web, limited, "GET", exportPath+"/download", 410)
				}
			})
		}
	}
}
