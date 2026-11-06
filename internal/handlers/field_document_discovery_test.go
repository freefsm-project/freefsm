package handlers

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestFieldDocumentCreationDiscoveryHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reviewed binding").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Discovery").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, job.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Creation discovery")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	read := func(path string) (map[string]any, string) {
		t.Helper()
		w := serviceAPIRequest(api, token, "GET", path, nil)
		if w.Code != 200 {
			t.Fatalf("discovery: %d %s", w.Code, w.Body.String())
		}
		serviceEntrySchemaValidator(t, api)("JobSyncEnvelope", w.Body.Bytes())
		var page struct {
			Data struct {
				Records []map[string]any `json:"records"`
				Cursor  string           `json:"completion_cursor"`
			} `json:"data"`
		}
		historyCheck(t, json.Unmarshal(w.Body.Bytes(), &page))
		if len(page.Data.Records) != 1 {
			t.Fatalf("records: %s", w.Body.String())
		}
		creation, ok := page.Data.Records[0]["document_creation"].(map[string]any)
		if !ok {
			t.Fatalf("missing creation inputs: %s", w.Body.String())
		}
		return creation, page.Data.Cursor
	}
	creation, cursor := read("/sync/jobs")
	for _, kind := range []string{"estimate", "invoice"} {
		input := creation[kind].(map[string]any)
		if input["can_create"] != true || input["customer_id"] != float64(customer.ID) || input["job_id"] != float64(job.ID) || input["company_today"] == "" {
			t.Fatalf("binding/capability: %+v", input)
		}
		if _, ok := input["definitions"].([]any); !ok {
			t.Fatalf("definitions: %+v", input)
		}
	}
	_, err = pool.Exec(ctx, `UPDATE company_settings SET default_invoice_notes='Changed creation default' WHERE company_id=$1`, company)
	historyCheck(t, err)
	changed, _ := read("/sync/jobs?cursor=" + url.QueryEscape(cursor))
	if changed["invoice"].(map[string]any)["company_defaults"].(map[string]any)["invoice_notes"] != "Changed creation default" {
		t.Fatalf("missing default delta: %+v", changed)
	}
}

func TestFieldDocumentDiscoveryAuthorityHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Purpose limited").SetNotes("PRIVATE MASTER").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned creation").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Creator").SetEmail("creation-discovery@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.Context}, {Permission: "pricing.view", Scope: authorization.Assigned}, {Permission: "estimate.view", Scope: authorization.Assigned}, {Permission: "estimate.create", Scope: authorization.Assigned}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Creation discovery", grants)
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	assignFieldDocumentCreator(t, pool, j.ID, u.ID)
	var field int64
	historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,precision,required,options,retired_options,default_value,help_text) VALUES($1,'estimate','Creation choice','select',NULL,true,'["Old","Current"]','["Old"]','Current','Choose current') RETURNING id`, company).Scan(&field))
	token, err := sessions.CreateMobile(ctx, u.ID, "Limited creator")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	w := serviceAPIRequest(api, token, "GET", "/sync/jobs?scope=all", nil)
	if w.Code != 200 {
		t.Fatalf("discovery %d %s", w.Code, w.Body.String())
	}
	serviceEntrySchemaValidator(t, api)("JobSyncEnvelope", w.Body.Bytes())
	assertNotContains(t, w.Body.String(), "PRIVATE MASTER")
	var page struct {
		Data struct {
			Records []struct {
				Creation map[string]json.RawMessage `json:"document_creation"`
			} `json:"records"`
			Cursor string `json:"completion_cursor"`
		} `json:"data"`
	}
	historyCheck(t, json.Unmarshal(w.Body.Bytes(), &page))
	if string(page.Data.Records[0].Creation["invoice"]) != `{"can_create":false}` {
		t.Fatalf("invoice inputs leaked: %s", w.Body.String())
	}
	var input authorization.DocumentCreationInputs
	historyCheck(t, json.Unmarshal(page.Data.Records[0].Creation["estimate"], &input))
	if len(input.Definitions) != 1 || input.Definitions[0].ID != field || *input.Definitions[0].Default != "Current" || len(input.Definitions[0].RetiredOptions) != 1 {
		t.Fatalf("incomplete definition: %+v", input)
	}
	_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, u.ID)
	historyCheck(t, err)
	w = serviceAPIRequest(api, token, "GET", "/sync/jobs?scope=all&cursor="+url.QueryEscape(page.Data.Cursor), nil)
	if w.Code != 409 {
		t.Fatalf("assignment loss must purge: %d %s", w.Code, w.Body.String())
	}
	w = serviceAPIRequest(api, token, "GET", "/sync/jobs?scope=all", nil)
	if w.Code != 200 {
		t.Fatalf("broad Job view: %d %s", w.Code, w.Body.String())
	}
	historyCheck(t, json.Unmarshal(w.Body.Bytes(), &page))
	for _, kind := range []string{"estimate", "invoice"} {
		if string(page.Data.Records[0].Creation[kind]) != `{"can_create":false}` {
			t.Fatalf("unassigned input leak: %s", w.Body.String())
		}
	}
}

func TestFieldDocumentDiscoveryCoherentPagesHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Coherent discovery").SaveX(ctx)
	for i := 0; i < 2; i++ {
		j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Coherent work").SaveX(ctx)
		assignFieldDocumentCreator(t, pool, j.ID, admin)
	}
	_, err := pool.Exec(ctx, `UPDATE company_settings SET default_invoice_notes='Before' WHERE company_id=$1`, company)
	historyCheck(t, err)
	var definition int64
	historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,precision,default_value) VALUES($1,'invoice','Exact field','number',2,'1.23') RETURNING id`, company).Scan(&definition))
	token, err := sessions.CreateMobile(ctx, admin, "Coherent pages")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	type pageData struct {
		Records []struct {
			Creation map[string]authorization.DocumentCreationInputs `json:"document_creation"`
		} `json:"records"`
		Next   *string `json:"next_page"`
		Cursor *string `json:"completion_cursor"`
	}
	read := func(path, wantNote, wantDefault string) pageData {
		t.Helper()
		w := serviceAPIRequest(api, token, "GET", path, nil)
		if w.Code != 200 {
			t.Fatalf("page %d %s", w.Code, w.Body.String())
		}
		serviceEntrySchemaValidator(t, api)("JobSyncEnvelope", w.Body.Bytes())
		var page struct {
			Data pageData `json:"data"`
		}
		historyCheck(t, json.Unmarshal(w.Body.Bytes(), &page))
		if len(page.Data.Records) != 1 {
			t.Fatalf("page length %s", w.Body.String())
		}
		input := page.Data.Records[0].Creation["invoice"]
		if *input.CompanyDefaults["invoice_notes"] != wantNote || len(input.Definitions) != 1 || input.Definitions[0].ID != definition || *input.Definitions[0].Default != wantDefault {
			t.Fatalf("mixed discovery snapshot: %+v", input)
		}
		return page.Data
	}
	first := read("/sync/jobs?page_size=1", "Before", "1.23")
	if first.Next == nil {
		t.Fatal("missing continuation")
	}
	_, err = pool.Exec(ctx, `UPDATE company_settings SET default_invoice_notes='After' WHERE company_id=$1`, company)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `UPDATE custom_field_definitions SET default_value='4.56' WHERE id=$1`, definition)
	historyCheck(t, err)
	last := read("/sync/jobs?page_size=1&page="+url.QueryEscape(*first.Next), "Before", "1.23")
	if last.Cursor == nil {
		t.Fatal("missing completion")
	}
	delta := read("/sync/jobs?page_size=1&cursor="+url.QueryEscape(*last.Cursor), "After", "4.56")
	if delta.Next == nil {
		t.Fatal("delta missed second changed record")
	}
	read("/sync/jobs?page_size=1&page="+url.QueryEscape(*delta.Next), "After", "4.56")
}
