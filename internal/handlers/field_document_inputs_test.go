package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestFieldDocumentCreationFieldDefaultsHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Field defaults").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Field defaults").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Field inputs")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			var field int64
			historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,required,options,retired_options,default_value) VALUES($1,$2,'Choice','select',true,'["Old","Current"]','["Old"]','Old') RETURNING id`, company, kind).Scan(&field))
			body := func(value string) string {
				return fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Explicit beats default","custom_fields":%s}`, newOperationKey(), newOperationKey(), j.ID, c.ID, value)
			}
			lifecycleCall(t, api, token, "POST", "/"+kind+"s", body("[]"), 422)
			accepted := lifecycleCall(t, api, token, "POST", "/"+kind+"s", body(fmt.Sprintf(`[{"definition_id":%d,"value":"Current"}]`, field)), 200)
			var receipt struct {
				Data struct {
					ID int64 `json:"id"`
				} `json:"data"`
			}
			historyCheck(t, json.Unmarshal([]byte(accepted), &receipt))
			read := lifecycleCall(t, api, token, "GET", fmt.Sprintf("/%ss/%d", kind, receipt.Data.ID), "", 200)
			assertContains(t, read, `Current`)
			// An invalid installed numeric default is not a valid creation value.
			_, err = pool.Exec(ctx, `UPDATE custom_field_definitions SET required=false,default_value=NULL WHERE id=$1`, field)
			historyCheck(t, err)
			var number int64
			historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,precision,default_value) VALUES($1,$2,'Precision','number',2,'1.234') RETURNING id`, company, kind).Scan(&number))
			lifecycleCall(t, api, token, "POST", "/"+kind+"s", body("[]"), 422)
			lifecycleCall(t, api, token, "POST", "/"+kind+"s", body(fmt.Sprintf(`[{"definition_id":%d,"value":"1.23"}]`, number)), 200)
		})
	}
}

func TestFieldDocumentCreationDatesHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Date defaults").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Date defaults").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Dates")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	zone := "Pacific/Kiritimati"
	if time.Now().UTC().Hour() < 10 {
		zone = "Etc/GMT+12"
	}
	loc, err := time.LoadLocation(zone)
	historyCheck(t, err)
	today := time.Now().In(loc).Format("2006-01-02")
	base, err := time.Parse("2006-01-02", today)
	historyCheck(t, err)
	for _, tc := range []struct {
		name, kind, customer, explicit, date, due, valid string
		status                                           int
	}{
		{"company invoice", "invoice", `{}`, ``, today, base.AddDate(0, 0, 30).Format("2006-01-02"), "", 200},
		{"company no due", "invoice", `{}`, ``, today, "", "", 200},
		{"customer invoice", "invoice", `{"invoice_due_days":"7"}`, ``, today, base.AddDate(0, 0, 7).Format("2006-01-02"), "", 200},
		{"explicit invoice date anchors due", "invoice", `{"invoice_due_days":"7"}`, `,"invoice_date":"2024-02-28"`, "2024-02-28", "2024-03-06", "", 200},
		{"explicit due wins", "invoice", `{"invoice_due_days":"7"}`, `,"invoice_date":"2024-02-28","due_date":"2024-02-29"`, "2024-02-28", "2024-02-29", "", 200},
		{"explicit no due", "invoice", `{}`, `,"due_date":""`, today, "", "", 200},
		{"customer no due", "invoice", `{"invoice_due_days":""}`, ``, today, "", "", 200},
		{"required invoice date", "invoice", `{}`, `,"invoice_date":""`, "", "", "", 422},
		{"invalid calendar date", "invoice", `{}`, `,"invoice_date":"2024-02-30"`, "", "", "", 422},
		{"company estimate", "estimate", `{}`, ``, "", "", base.AddDate(0, 0, 14).Format("2006-01-02"), 200},
		{"company no validity", "estimate", `{}`, ``, "", "", "", 200},
		{"customer estimate", "estimate", `{"estimate_valid_days":"2"}`, ``, "", "", base.AddDate(0, 0, 2).Format("2006-01-02"), 200},
		{"explicit estimate", "estimate", `{"estimate_valid_days":"2"}`, `,"valid_through":"2024-02-29"`, "", "", "2024-02-29", 200},
		{"explicit no validity", "estimate", `{}`, `,"valid_through":""`, "", "", "", 200},
		{"customer no validity", "estimate", `{"estimate_valid_days":""}`, ``, "", "", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err = pool.Exec(ctx, `UPDATE company_settings SET timezone=$2,default_due_days=30,estimate_valid_days=14,default_invoice_notes='Company notes',default_estimate_notes='Company notes' WHERE company_id=$1`, company, zone)
			historyCheck(t, err)
			if tc.name == "company no due" || tc.name == "company no validity" {
				_, err = pool.Exec(ctx, `UPDATE company_settings SET default_due_days=NULL,estimate_valid_days=NULL WHERE company_id=$1`, company)
				historyCheck(t, err)
			}
			_, err = pool.Exec(ctx, `INSERT INTO customer_document_defaults(customer_id,defaults) VALUES($1,$2) ON CONFLICT(customer_id) DO UPDATE SET defaults=EXCLUDED.defaults`, c.ID, tc.customer)
			historyCheck(t, err)
			body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Calendar dates"%s}`, newOperationKey(), newOperationKey(), j.ID, c.ID, tc.explicit)
			first := lifecycleCall(t, api, token, "POST", "/"+tc.kind+"s", body, tc.status)
			if tc.status != 200 {
				return
			}
			var receipt struct {
				Data struct {
					ID int64 `json:"id"`
				} `json:"data"`
			}
			historyCheck(t, json.Unmarshal([]byte(first), &receipt))
			path := fmt.Sprintf("/%ss/%d", tc.kind, receipt.Data.ID)
			before := lifecycleCall(t, api, token, "GET", path, "", 200)
			var read struct {
				Data map[string]any `json:"data"`
			}
			historyCheck(t, json.Unmarshal([]byte(before), &read))
			for key, want := range map[string]string{"invoice_date": tc.date, "due_date": tc.due, "valid_through": tc.valid} {
				got, _ := read.Data[key].(string)
				if want == "" && got != "" || want != "" && !strings.HasPrefix(got, want) {
					t.Fatalf("%s=%q want %q: %s", key, got, want, before)
				}
			}
			_, err = pool.Exec(ctx, `UPDATE company_settings SET timezone='UTC',default_due_days=99,estimate_valid_days=99 WHERE company_id=$1`, company)
			historyCheck(t, err)
			_, err = pool.Exec(ctx, `UPDATE customer_document_defaults SET defaults='{"invoice_due_days":"99","estimate_valid_days":"99"}' WHERE customer_id=$1`, c.ID)
			historyCheck(t, err)
			if replay := lifecycleCall(t, api, token, "POST", "/"+tc.kind+"s", body, 200); replay != first {
				t.Fatal("replay changed receipt")
			}
			if after := lifecycleCall(t, api, token, "GET", path, "", 200); after != before {
				t.Fatal("replay rewrote accepted dates/defaults")
			}
		})
	}
}

func TestFieldDocumentCreationFieldTypesHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Typed creation").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Typed creation").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Typed creation")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			types := []struct {
				typ            string
				valid, invalid any
			}{
				{"text", "Single line", "two\nlines"},
				{"textarea", "Multiple\nlines", 42},
				{"number", "-1.23", "1.234"},
				{"date", "2024-02-29", "2023-02-29"},
				{"checkbox", "false", "maybe"},
				{"select", "Current", "Old"},
				{"multiselect", []string{"Current", "Other"}, []string{"Current", "Old"}},
			}
			fields := make([]int64, len(types))
			for i, tc := range types {
				historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,precision,required,options,retired_options) VALUES($1,$2,$3,$3,2,true,'["Old","Current","Other"]','["Old"]') RETURNING id`, company, kind, tc.typ).Scan(&fields[i]))
			}
			var retired int64
			historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,required,retired) VALUES($1,$2,'Retired required','text',true,true) RETURNING id`, company, kind).Scan(&retired))
			values := func(omit, invalid int) []map[string]any {
				out := []map[string]any{}
				for i, tc := range types {
					if i == omit {
						continue
					}
					v := tc.valid
					if i == invalid {
						v = tc.invalid
					}
					out = append(out, map[string]any{"definition_id": fields[i], "value": v})
				}
				return out
			}
			create := func(values []map[string]any, status int) int64 {
				t.Helper()
				before := fieldCreationEffects(t, pool, company)
				body := map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": j.ID, "customer_id": c.ID, "title": "Full creation validation", "custom_fields": values}
				w := serviceAPIRequest(api, token, "POST", "/"+kind+"s", body)
				if w.Code != status {
					t.Fatalf("typed creation %d want %d: %s", w.Code, status, w.Body.String())
				}
				if status != 200 {
					if after := fieldCreationEffects(t, pool, company); after != before {
						t.Fatal("partial invalid typed creation")
					}
					return 0
				}
				var receipt struct {
					Data struct {
						ID int64 `json:"id"`
					} `json:"data"`
				}
				historyCheck(t, json.Unmarshal(w.Body.Bytes(), &receipt))
				return receipt.Data.ID
			}
			id := create(values(-1, -1), 200)
			original := lifecycleCall(t, api, token, "GET", fmt.Sprintf("/%ss/%d", kind, id), "", 200)
			for i, tc := range types {
				t.Run(tc.typ, func(t *testing.T) { create(values(i, -1), 422); create(values(-1, i), 422) })
			}
			for i, tc := range types {
				value, ok := tc.valid.(string)
				if !ok {
					raw, err := json.Marshal(tc.valid)
					historyCheck(t, err)
					value = string(raw)
				}
				_, err = pool.Exec(ctx, `UPDATE custom_field_definitions SET default_value=$2 WHERE id=$1`, fields[i], value)
				historyCheck(t, err)
			}
			create([]map[string]any{}, 200)
			for i, tc := range types {
				t.Run(tc.typ+" explicit beats default", func(t *testing.T) { create(values(-1, i), 422) })
			}
			create(append(values(-1, -1), map[string]any{"definition_id": retired, "value": "new retired value"}), 422)
			// Definition retirement does not rewrite accepted installed values or IDs.
			_, err = pool.Exec(ctx, `UPDATE custom_field_definitions SET retired=true WHERE id=ANY($1)`, fields)
			historyCheck(t, err)
			create([]map[string]any{}, 200)
			after := lifecycleCall(t, api, token, "GET", fmt.Sprintf("/%ss/%d", kind, id), "", 200)
			if after != original {
				t.Fatal("definition retirement rewrote accepted document")
			}
		})
	}
}
