package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/customfields"
)

func TestConversionTypedPairingAndRecoveryMatrixHTTP(t *testing.T) {
	for _, tc := range []struct {
		kind            string
		source, current []string
	}{
		{"text", []string{"Original short"}, []string{"Current short"}},
		{"textarea", []string{"Original\nmultiline"}, []string{"Current\nmultiline"}},
		{"number", []string{"1.23"}, []string{"9.87"}},
		{"date", []string{"2026-01-02"}, []string{"2026-10-23"}},
		{"checkbox", []string{"false"}, []string{"true"}},
		{"select", []string{"North"}, []string{"South"}},
		{"multiselect", []string{"North", "South"}, []string{"South"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			definitions := []url.Values{}
			for _, kind := range []string{"estimate", "invoice"} {
				v := url.Values{"object_type": {kind}, "name": {kind + " distinct label"}, "help_text": {kind + " distinct help"}, "field_type": {tc.kind}, "conversion_key": {"explicit-key"}, "required": {"true"}}
				if tc.kind == "number" {
					v.Set("precision", "2")
				}
				if tc.kind == "select" || tc.kind == "multiselect" {
					v.Set("options", `["North","South"]`)
				}
				fieldPost(t, web, cookie, "/settings/custom-fields", v, 303)
				definitions = append(definitions, v)
			}
			var pdf bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT bool_or(include_pdf) FROM custom_field_definitions`).Scan(&pdf))
			if pdf {
				t.Fatal("PDF inclusion did not default off")
			}
			definitions[1].Set("include_pdf", "true")
			fieldPost(t, web, cookie, "/settings/custom-fields/2", definitions[1], 303)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Typed pairing").SaveX(ctx)
			fieldKey := func(id int) string {
				key := fmt.Sprintf("custom_field_%d", id)
				if tc.kind == "multiselect" {
					key += "[]"
				}
				return key
			}
			v := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Typed source"}, "line_items": {"[]"}, fieldKey(1): tc.source}
			fieldPost(t, web, cookie, "/estimates", v, 303)
			var id int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&id))
			source := fmt.Sprintf("/estimates/%d", id)
			converted := fieldPost(t, web, cookie, source+"/convert", url.Values{"idempotency_key": {newOperationKey()}, fieldKey(2): tc.current}, 303)
			invoice := strings.Split(converted.Header().Get("Location"), "?")[0]
			readValue := func(table string, definition int) any {
				t.Helper()
				var raw string
				historyCheck(t, pool.QueryRow(ctx, `SELECT custom_fields::text FROM `+table+` WHERE conversion_hidden_at IS NULL LIMIT 1`).Scan(&raw))
				var values []customfields.Value
				historyCheck(t, json.Unmarshal([]byte(raw), &values))
				for _, value := range values {
					if value.DefinitionID == int64(definition) {
						return value.Value
					}
				}
				return nil
			}
			want := func(values []string) any {
				if tc.kind == "multiselect" {
					out := []any{}
					for _, v := range values {
						out = append(out, v)
					}
					return out
				}
				return values[0]
			}
			if !reflect.DeepEqual(readValue("invoices", 2), want(tc.source)) {
				t.Fatal("explicit target overrode paired source")
			}
			fieldPost(t, web, cookie, invoice+"/custom-fields", url.Values{fieldKey(2): tc.current}, 303)
			// Type changes and deletion must remain forbidden even when a cycle's
			// hidden source is the only recorded use of an original definition.
			unsafe := url.Values{"name": {"Unsafe type"}, "field_type": {"number"}, "precision": {"2"}}
			if tc.kind == "number" {
				unsafe.Set("field_type", "text")
			}
			fieldPost(t, web, cookie, "/settings/custom-fields/1", unsafe, 422)
			fieldPost(t, web, cookie, "/settings/custom-fields/1/delete", url.Values{}, 422)
			if tc.kind == "select" || tc.kind == "multiselect" {
				bad := url.Values{"name": {"Wrong order"}, "field_type": {tc.kind}, "conversion_key": {"explicit-key"}, "options": {`["South","North"]`}}
				fieldPost(t, web, cookie, "/settings/custom-fields/2", bad, 422)
				bad.Set("options", `["Norte","South"]`)
				fieldPost(t, web, cookie, "/settings/custom-fields/2", bad, 422)
			}
			if tc.kind == "number" {
				fieldPost(t, web, cookie, "/settings/custom-fields/2", url.Values{"name": {"Unsafe precision"}, "field_type": {"number"}, "precision": {"1"}}, 422)
			}
			for i, definition := range definitions {
				definition.Set("retired", "true")
				fieldPost(t, web, cookie, fmt.Sprintf("/settings/custom-fields/%d", i+1), definition, 303)
			}
			fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Required after capture"}, "object_type": {"estimate"}, "field_type": {"text"}, "required": {"true"}, "default_value": {"Never backfill recovery"}}, 303)
			fieldPost(t, web, cookie, invoice+"/revert-to-estimate", url.Values{"idempotency_key": {newOperationKey()}}, 303)
			if !reflect.DeepEqual(readValue("estimates", 1), want(tc.current)) || readValue("estimates", 3) != nil {
				t.Fatal("recovery lost current shared value or invented a new required value")
			}
			page := requestBody(t, web, cookie, "GET", source+"/edit", 200)
			assertNotContains(t, page, `name="custom_field_1`)
			save := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Recovery save"}, "line_items": {"[]"}}
			fieldPost(t, web, cookie, source, save, 422)
			save.Set("custom_field_3", "Explicit new required value")
			fieldPost(t, web, cookie, source, save, 303)
			if !reflect.DeepEqual(readValue("estimates", 1), want(tc.current)) {
				t.Fatal("ordinary save changed retired recovered value")
			}
			definitions[0].Set("retired", "false")
			fieldPost(t, web, cookie, "/settings/custom-fields/1", definitions[0], 303)
			if !reflect.DeepEqual(readValue("estimates", 1), want(tc.current)) {
				t.Fatal("reactivation backfilled a retained value")
			}
		})
	}
}

func TestConversionReversionRetainsRetiredOptionWithoutReselectionHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, kind := range []string{"estimate", "invoice"} {
		fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"object_type": {kind}, "name": {kind + " option"}, "field_type": {"select"}, "options": {`["North","South"]`}, "conversion_key": {"direction"}}, 303)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retired recovery").SaveX(ctx)
	fieldPost(t, web, cookie, "/estimates", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Retained retired option"}, "line_items": {"[]"}, "custom_field_1": {"North"}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&id))
	source := fmt.Sprintf("/estimates/%d", id)
	converted := fieldPost(t, web, cookie, source+"/convert", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	invoice := strings.Split(converted.Header().Get("Location"), "?")[0]
	fieldPost(t, web, cookie, invoice+"/custom-fields", url.Values{"custom_field_2": {"South"}}, 303)
	definition := url.Values{"name": {"estimate option"}, "field_type": {"select"}, "options": {`["North","South"]`}, "retired_options": {`["South"]`}, "conversion_key": {"direction"}, "default_value": {"North"}}
	fieldPost(t, web, cookie, "/settings/custom-fields/1", definition, 303)
	fieldPost(t, web, cookie, invoice+"/revert-to-estimate", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	assertContains(t, requestBody(t, web, cookie, "GET", source+"/custom-fields", 200), `value="South" selected`)
	fieldPost(t, web, cookie, source+"/custom-fields", url.Values{"custom_field_1": {"South"}}, 303)
	fieldPost(t, web, cookie, source+"/custom-fields", url.Values{"custom_field_1": {"North"}}, 303)
	fieldPost(t, web, cookie, source+"/custom-fields", url.Values{"custom_field_1": {"South"}}, 422)
}
