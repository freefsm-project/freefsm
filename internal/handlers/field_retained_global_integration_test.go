package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These definitions are inserted AFTER the released company backfill, while
// the old nullable schema is still live. Migration must retain, not re-own them.
func TestRetainedGlobalFieldsConvertAndRecoverWithCapturedPairHTTP(t *testing.T) {
	for _, owned := range []string{"neither", "source", "target"} {
		t.Run(owned, func(t *testing.T) {
			ctx := context.Background()
			pool, client, router, sessions, company, admin := permissionHTTPFixtureSeeded(t, func(pool *pgxpool.Pool) {
				var company int64
				if err := pool.QueryRow(ctx, `SELECT company_id FROM users WHERE email='admin@test'`).Scan(&company); err != nil {
					t.Fatal(err)
				}
				var sourceOwner, targetOwner *int64
				if owned == "source" {
					sourceOwner = &company
				}
				if owned == "target" {
					targetOwner = &company
				}
				if _, err := pool.Exec(ctx, `INSERT INTO custom_field_definitions(id,company_id,object_type,name,field_type,options,conversion_key) VALUES(9100,$1,'estimate','Retained source','select','["North","South"]','retained'),(9101,$2,'invoice','Retained target','select','["North","South"]','retained')`, sourceOwner, targetOwner); err != nil {
					t.Fatal(err)
				}
			})
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained globals").SaveX(ctx)
			fieldPost(t, router, cookie, "/estimates", url.Values{"title": {"Global pair"}, "customer_id": {fmt.Sprint(c.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}, "custom_field_9100": {"North"}}, 303)
			match := regexp.MustCompile(`href="(/estimates/[0-9]+)"`).FindStringSubmatch(requestBody(t, router, cookie, "GET", "/estimates", 200))
			if len(match) != 2 {
				t.Fatal("missing Estimate")
			}
			estimate := match[1]
			converted := fieldPost(t, router, cookie, estimate+"/convert", url.Values{"idempotency_key": {"30000000-0000-4000-8000-000000000311"}}, 303)
			invoice := strings.Split(converted.Header().Get("Location"), "?")[0]
			assertContains(t, requestBody(t, router, cookie, "GET", invoice+"/custom-fields", 200), `value="North" selected`)
			fieldPost(t, router, cookie, invoice+"/custom-fields", url.Values{"custom_field_9101": {"South"}}, 303)
			fieldPost(t, router, cookie, "/settings/custom-fields/9101", url.Values{"name": {"Retained target renamed"}, "field_type": {"select"}, "options": {`["North","South"]`}, "conversion_key": {"future"}}, 303)
			fieldPost(t, router, cookie, invoice+"/revert-to-estimate", url.Values{"idempotency_key": {"30000000-0000-4000-8000-000000000312"}}, 303)
			assertContains(t, requestBody(t, router, cookie, "GET", estimate+"/custom-fields", 200), `value="South" selected`)
			var retained int
			want := 2
			if owned != "neither" {
				want = 1
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM custom_field_definitions WHERE id IN (9100,9101) AND company_id IS NULL`).Scan(&retained); err != nil || retained != want {
				t.Fatalf("global identities reassigned: %d %v", retained, err)
			}
		})
	}
}

func TestRetainedGlobalPairingEnforcesCompatibilityAndOverlappingUniquenessHTTP(t *testing.T) {
	ctx := context.Background()
	_, _, router, sessions, _, admin := permissionHTTPFixtureSeeded(t, func(pool *pgxpool.Pool) {
		if _, err := pool.Exec(ctx, `ALTER TABLE custom_field_definitions ADD COLUMN precision integer; INSERT INTO custom_field_definitions(id,object_type,name,field_type,precision,options,conversion_key) VALUES(9300,'estimate','Global choice','multiselect',NULL,'["North","South"]','global-choice'),(9301,'estimate','Global number','number',3,'[]','global-number'),(9302,'estimate','Global race','text',NULL,'[]',NULL)`); err != nil {
			t.Fatal(err)
		}
	})
	cookie := sessionCookie(t, ctx, sessions, admin)
	target := url.Values{"object_type": {"invoice"}, "name": {"Owned choice"}, "field_type": {"multiselect"}, "options": {`["South","North"]`}, "conversion_key": {"global-choice"}}
	fieldPost(t, router, cookie, "/settings/custom-fields", target, 422)
	target.Set("options", `["North","South"]`)
	fieldPost(t, router, cookie, "/settings/custom-fields", target, 303)
	fieldPost(t, router, cookie, "/settings/custom-fields/9300", url.Values{"name": {"Global choice"}, "field_type": {"multiselect"}, "options": {`["South","North"]`}, "conversion_key": {"global-choice"}}, 422)
	target.Set("name", "Duplicate source")
	target.Set("object_type", "estimate")
	fieldPost(t, router, cookie, "/settings/custom-fields", target, 422)
	number := url.Values{"object_type": {"invoice"}, "name": {"Owned number"}, "field_type": {"number"}, "precision": {"2"}, "conversion_key": {"global-number"}}
	fieldPost(t, router, cookie, "/settings/custom-fields", number, 422)
	number.Set("precision", "3")
	fieldPost(t, router, cookie, "/settings/custom-fields", number, 303)
	fieldPost(t, router, cookie, "/settings/custom-fields/9301", url.Values{"name": {"Global number"}, "field_type": {"number"}, "precision": {"4"}, "conversion_key": {"global-number"}}, 422)
	// Global edits and owned insertions overlap even though their nullable unique
	// index tuples differ. Both committing cannot leave a deterministic pairing.
	done := make(chan *httptest.ResponseRecorder, 2)
	for _, path := range []string{"/settings/custom-fields/9302", "/settings/custom-fields"} {
		go func(path string) {
			name := "Global race"
			if path == "/settings/custom-fields" {
				name = "Owned race"
			}
			body := url.Values{"object_type": {"estimate"}, "name": {name}, "field_type": {"text"}, "conversion_key": {"racing-owned-global"}}
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			done <- w
		}(path)
	}
	statuses := map[int]int{}
	for range 2 {
		w := <-done
		statuses[w.Code]++
		if w.Code != 303 && w.Code != 422 {
			t.Fatalf("race response %d: %s", w.Code, w.Body.String())
		}
	}
	if statuses[303] != 1 || statuses[422] != 1 {
		t.Fatalf("overlapping pair writers both committed: %v", statuses)
	}
}

func TestRetainedGlobalPairingUpgradeRejectsAmbiguityWithoutRewriting(t *testing.T) {
	ctx := context.Background()
	source := database.MigrationFS()
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "073_retained_global_fields.up.sql" {
			continue
		}
		b, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: b}
	}
	pool, _, _, _, _, _ := permissionHTTPFixtureMigrations(t, old)
	if _, err = pool.Exec(ctx, `INSERT INTO custom_field_definitions(id,object_type,name,field_type,conversion_key) VALUES(9400,'estimate','Legacy global one','text','ambiguous-global'),(9401,'estimate','Legacy global two','text','ambiguous-global')`); err != nil {
		t.Fatal(err)
	}
	inventory := func() string {
		t.Helper()
		var raw string
		if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(d) ORDER BY id)::text FROM custom_field_definitions d`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := inventory()
	err = (&database.DB{Pool: pool}).Migrate(ctx, source)
	if err == nil || !strings.Contains(err.Error(), "explicit review") || !strings.Contains(err.Error(), "9400") || !strings.Contains(err.Error(), "9401") {
		t.Fatalf("ambiguous retained globals were not classified: %v", err)
	}
	if after := inventory(); after != before {
		t.Fatal("failed upgrade rewrote retained global definitions")
	}
	var applied bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='073_retained_global_fields')`).Scan(&applied); err != nil || applied {
		t.Fatalf("failed migration recorded: %v %v", applied, err)
	}
}

func TestRetainedGlobalLegacyCycleRequiresReviewAndRemainsRecoverableHTTP(t *testing.T) {
	ctx := context.Background()
	const cycle = "30000000-0000-4000-8000-000000000320"
	var estimate, invoice int64
	var original string
	pool, _, router, sessions, _, admin := permissionHTTPFixtureSeeded(t, func(pool *pgxpool.Pool) {
		exec := func(query string, args ...any) {
			t.Helper()
			if _, err := pool.Exec(ctx, query, args...); err != nil {
				t.Fatal(err)
			}
		}
		exec(`INSERT INTO custom_field_definitions(id,object_type,name,field_type,options,conversion_key) VALUES(9200,'estimate','Historical global source','select','["North","South"]','historic-global'),(9201,'invoice','Historical global target','select','["North","South"]','historic-global'),(9202,'invoice','Historical unknown pairing','text','[]',NULL)`)
		var company, actor, customer int64
		if err := pool.QueryRow(ctx, `SELECT company_id,id FROM users WHERE email='admin@test'`).Scan(&company, &actor); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Legacy global customer') RETURNING id`, company).Scan(&customer); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,title,custom_fields) VALUES($1,$2,'Historical global cycle','[{"definition_id":9200,"value":"North"}]') RETURNING id`, company, customer).Scan(&estimate); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,title,invoice_number,custom_fields) VALUES($1,$2,'Historical global invoice',920,'[{"definition_id":9201,"value":"South"}]') RETURNING id`, company, customer).Scan(&invoice); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO estimate_invoice_conversion_cycles(id,company_id,estimate_id,invoice_id,invoice_number,source_snapshot,converted_by) SELECT $1,company_id,id,$2,920,to_jsonb(e),$3 FROM estimates e WHERE id=$4`, cycle, invoice, actor, estimate)
		exec(`UPDATE estimates SET conversion_hidden_at=now() WHERE id=$1`, estimate)
		if err := pool.QueryRow(ctx, `SELECT source_snapshot::text FROM estimate_invoice_conversion_cycles WHERE id=$1`, cycle).Scan(&original); err != nil {
			t.Fatal(err)
		}
	})
	cookie := sessionCookie(t, ctx, sessions, admin)
	invoicePath := fmt.Sprintf("/invoices/%d", invoice)
	fieldPost(t, router, cookie, invoicePath+"/revert-to-estimate", url.Values{"idempotency_key": {"30000000-0000-4000-8000-000000000321"}}, 409)
	fieldPost(t, router, cookie, "/settings/custom-fields/9202/delete", url.Values{}, 422)
	fieldPost(t, router, cookie, "/settings/custom-fields/9202", url.Values{"name": {"Historical unknown pairing"}, "field_type": {"number"}, "precision": {"2"}}, 422)
	evidence := `{"field_pairs":{"9200":9201},"field_definitions":[{"id":9200,"name":"Historical global source","field_type":"select","options":"[\"North\",\"South\"]"},{"id":9201,"name":"Historical global target","field_type":"select","options":"[\"North\",\"South\"]"}]}`
	var foreignCompany int64
	if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Foreign review scope','foreign-review') RETURNING id`).Scan(&foreignCompany); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO custom_field_definitions(id,company_id,object_type,name,field_type,options) VALUES(9801,$1,'invoice','Foreign historical target','select','["North","South"]')`, foreignCompany); err != nil {
		t.Fatal(err)
	}
	fieldPost(t, router, cookie, "/settings/custom-fields/cycles/"+cycle+"/review", url.Values{"evidence": {strings.ReplaceAll(evidence, "9201", "9801")}, "reason": {"Foreign identity must not be accepted"}, "reviewed": {"true"}}, 404)
	fieldPost(t, router, cookie, "/settings/custom-fields/cycles/"+cycle+"/review", url.Values{"evidence": {evidence}, "reason": {"Reviewed original retained-global configuration"}, "reviewed": {"true"}}, 303)
	fieldPost(t, router, cookie, invoicePath+"/revert-to-estimate", url.Values{"idempotency_key": {"30000000-0000-4000-8000-000000000322"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/estimates/%d/custom-fields", estimate), 200), `value="South" selected`)
	var after string
	if err := pool.QueryRow(ctx, `SELECT source_snapshot::text FROM estimate_invoice_conversion_cycles WHERE id=$1`, cycle).Scan(&after); err != nil || after != original {
		t.Fatalf("original evidence rewritten: %v", err)
	}
}

func TestRetainedGlobalOmittedCaptureRequiresExplicitReviewHTTP(t *testing.T) {
	ctx := context.Background()
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Incomplete retained capture").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO custom_field_definitions(id,object_type,name,field_type,options,conversion_key) VALUES(9500,'estimate','Omitted global source','select','["North","South"]','omitted'),(9501,'invoice','Omitted global target','select','["North","South"]','omitted')`); err != nil {
		t.Fatal(err)
	}
	e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Omitted capture source").SetCustomFields(`[{"definition_id":9500,"value":"North"}]`).SaveX(ctx)
	var invoice int64
	if err := pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,title,invoice_number,custom_fields) VALUES($1,$2,'Omitted capture invoice',950,'[{"definition_id":9501,"value":"South"}]') RETURNING id`, company, c.ID).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	const cycle = "30000000-0000-4000-8000-000000000350"
	if _, err := pool.Exec(ctx, `INSERT INTO estimate_invoice_conversion_cycles(id,company_id,estimate_id,invoice_id,invoice_number,source_snapshot,converted_by) SELECT $1,company_id,id,$2,950,to_jsonb(e)||'{"field_pairs":{},"field_definitions":[]}'::jsonb,$3 FROM estimates e WHERE id=$4`, cycle, invoice, admin, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE estimates SET conversion_hidden_at=now() WHERE id=$1`, e.ID); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d", invoice)
	fieldPost(t, router, cookie, path+"/revert-to-estimate", url.Values{"idempotency_key": {"30000000-0000-4000-8000-000000000351"}}, 409)
	fieldPost(t, router, cookie, "/settings/custom-fields/9501", url.Values{"name": {"Omitted global target"}, "field_type": {"select"}, "options": {`["North","South"]`}, "conversion_key": {"new-key"}}, 422)
	evidence := `{"field_pairs":{"9500":9501},"field_definitions":[{"id":9500,"name":"Omitted global source","field_type":"select","options":"[\"North\",\"South\"]"},{"id":9501,"name":"Omitted global target","field_type":"select","options":"[\"North\",\"South\"]"}]}`
	fieldPost(t, router, cookie, "/settings/custom-fields/cycles/"+cycle+"/review", url.Values{"evidence": {evidence}, "reason": {"Original capture omitted existing global definitions; historical pairing reviewed"}, "reviewed": {"true"}}, 303)
	fieldPost(t, router, cookie, path+"/revert-to-estimate", url.Values{"idempotency_key": {"30000000-0000-4000-8000-000000000352"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/estimates/%d/custom-fields", e.ID), 200), `value="South" selected`)
	var unchanged bool
	if err := pool.QueryRow(ctx, `SELECT source_snapshot->'field_pairs'='{}'::jsonb AND source_snapshot->'field_definitions'='[]'::jsonb AND source_snapshot->'custom_fields'->0->>'value'='North' FROM estimate_invoice_conversion_cycles WHERE id=$1`, cycle).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("original incomplete capture rewritten: %v %v", unchanged, err)
	}
}
