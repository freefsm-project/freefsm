package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestFieldPairingChangesOnlyFutureCyclesHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Cycle customer").SaveX(ctx)
	for _, typ := range []string{"estimate", "invoice"} {
		fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {typ + " reading"}, "object_type": {typ}, "field_type": {"text"}, "conversion_key": {"reading"}}, 303)
	}
	fieldPost(t, router, cookie, "/estimates", url.Values{"title": {"Cycle evidence"}, "customer_id": {fmt.Sprint(customer.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}, "custom_field_1": {"Source"}}, 303)
	match := regexp.MustCompile(`href="(/estimates/[0-9]+)"`).FindStringSubmatch(requestBody(t, router, cookie, "GET", "/estimates", 200))
	if len(match) != 2 {
		t.Fatal("missing Estimate")
	}
	path := match[1]
	converted := fieldPost(t, router, cookie, path+"/convert", url.Values{"idempotency_key": {"30111111-1111-4111-8111-111111111111"}}, 303)
	invoice := strings.Split(converted.Header().Get("Location"), "?")[0]
	fieldPost(t, router, cookie, invoice+"/custom-fields", url.Values{"custom_field_2": {"Changed on Invoice"}}, 303)
	fieldPost(t, router, cookie, "/settings/custom-fields/2", url.Values{"name": {"Invoice reading"}, "field_type": {"number"}, "precision": {"2"}}, 422)
	fieldPost(t, router, cookie, "/settings/custom-fields/2", url.Values{"name": {"Invoice reading"}, "field_type": {"text"}, "conversion_key": {"future"}}, 303)
	fieldPost(t, router, cookie, invoice+"/revert-to-estimate", url.Values{"idempotency_key": {"30222222-2222-4222-8222-222222222222"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Changed on Invoice")
}

func TestActiveCycleRejectsValuesOutsideCapturedPrecisionHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Precision customer").SaveX(ctx)
	for _, typ := range []string{"estimate", "invoice"} {
		fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {typ}, "object_type": {typ}, "field_type": {"number"}, "precision": {"2"}, "conversion_key": {"reading"}}, 303)
	}
	fieldPost(t, router, cookie, "/estimates", url.Values{"title": {"Precision cycle"}, "customer_id": {fmt.Sprint(customer.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}, "custom_field_1": {"1.23"}}, 303)
	match := regexp.MustCompile(`href="(/estimates/[0-9]+)"`).FindStringSubmatch(requestBody(t, router, cookie, "GET", "/estimates", 200))
	if len(match) != 2 {
		t.Fatal("missing Estimate")
	}
	converted := fieldPost(t, router, cookie, match[1]+"/convert", url.Values{"idempotency_key": {"30333333-3333-4333-8333-333333333333"}}, 303)
	invoice := strings.Split(converted.Header().Get("Location"), "?")[0]
	fieldPost(t, router, cookie, "/settings/custom-fields/2", url.Values{"name": {"invoice"}, "field_type": {"number"}, "precision": {"3"}}, 303)
	fieldPost(t, router, cookie, invoice+"/custom-fields", url.Values{"custom_field_2": {"1.234"}}, 422)
	assertContains(t, requestBody(t, router, cookie, "GET", invoice, 200), "1.23")
}

func TestFieldPairRequiresExactPrecisionAndOrderedOptionsHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Source"}, "object_type": {"estimate"}, "field_type": {"number"}, "precision": {"2"}, "conversion_key": {"exact"}}, 303)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Target"}, "object_type": {"invoice"}, "field_type": {"number"}, "precision": {"3"}, "conversion_key": {"exact"}}, 422)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Choices"}, "object_type": {"estimate"}, "field_type": {"multiselect"}, "options": {`["A","B"]`}, "conversion_key": {"choices"}}, 303)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Choices"}, "object_type": {"invoice"}, "field_type": {"multiselect"}, "options": {`["B","A"]`}, "conversion_key": {"choices"}}, 422)
}

func TestConversionDoesNotInventMissingPairedSourceFromDefaultHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Missing paired value").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Source"}, "object_type": {"estimate"}, "field_type": {"text"}, "conversion_key": {"missing"}}, 303)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Target"}, "object_type": {"invoice"}, "field_type": {"text"}, "conversion_key": {"missing"}, "default_value": {"Must not invent source"}}, 303)
	fieldPost(t, router, cookie, "/estimates", url.Values{"title": {"No invented values"}, "customer_id": {fmt.Sprint(c.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	match := regexp.MustCompile(`href="(/estimates/[0-9]+)"`).FindStringSubmatch(requestBody(t, router, cookie, "GET", "/estimates", 200))
	if len(match) != 2 {
		t.Fatal("missing Estimate")
	}
	converted := fieldPost(t, router, cookie, match[1]+"/convert", url.Values{"idempotency_key": {"30000000-0000-4000-8000-000000000099"}}, 303)
	invoice := strings.Split(converted.Header().Get("Location"), "?")[0]
	assertNotContains(t, requestBody(t, router, cookie, "GET", invoice, 200), "Must not invent source")
}

func TestRetiredOptionsRetainSelectionsButRejectNewSelectionsHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	definition := url.Values{"name": {"Region"}, "object_type": {"customer"}, "field_type": {"select"}, "options": {`["North","South"]`}}
	fieldPost(t, router, cookie, "/settings/custom-fields", definition, 303)
	created := fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Old selection"}, "custom_field_1": {"North"}}, 303)
	path := strings.TrimSuffix(created.Header().Get("Location"), "/edit")
	definition.Set("retired_options", `["North"]`)
	fieldPost(t, router, cookie, "/settings/custom-fields/1", definition, 303)
	fieldPost(t, router, cookie, path, url.Values{"display_name": {"Unchanged selection"}, "custom_field_1": {"North"}}, 303)
	fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Rejected selection"}, "custom_field_1": {"North"}}, 422)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "North")
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/customers/new", 200), `value="North"`)
	definition.Set("options", `["South"]`)
	fieldPost(t, router, cookie, "/settings/custom-fields/1", definition, 422)
}

func TestConversionRequiresInvoiceValuesAndAppliesDefaultsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Required conversion").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Approval"}, "object_type": {"invoice"}, "field_type": {"checkbox"}, "required": {"true"}}, 303)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Default memo"}, "object_type": {"invoice"}, "field_type": {"text"}, "default_value": {"Prospective invoice default"}}, 303)
	fieldPost(t, router, cookie, "/estimates", url.Values{"title": {"Invoice inputs"}, "customer_id": {fmt.Sprint(customer.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	match := regexp.MustCompile(`href="(/estimates/[0-9]+)"`).FindStringSubmatch(requestBody(t, router, cookie, "GET", "/estimates", 200))
	if len(match) != 2 {
		t.Fatal("missing estimate")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", match[1]+"/convert", 200), "Approval")
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Conversion without Invoice Edit", []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.convert", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.create", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("converter@test").SetName("Converter").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	limited := sessionCookie(t, ctx, sessions, user.ID)
	fieldPost(t, router, limited, match[1]+"/convert", url.Values{"idempotency_key": {"30888888-8888-4888-8888-888888888888"}, "custom_field_1": {"false"}}, 403)
	fieldPost(t, router, cookie, match[1]+"/convert", url.Values{"idempotency_key": {"30444444-4444-4444-8444-444444444444"}}, 422)
	assertContains(t, requestBody(t, router, cookie, "GET", match[1], 200), "Invoice inputs")
	converted := fieldPost(t, router, cookie, match[1]+"/convert", url.Values{"idempotency_key": {"30555555-5555-4555-8555-555555555555"}, "custom_field_1": {"false"}}, 303)
	invoice := strings.Split(converted.Header().Get("Location"), "?")[0]
	assertContains(t, requestBody(t, router, cookie, "GET", invoice, 200), "Prospective invoice default")
}

func TestFieldRetirementAndReactivationHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	definition := url.Values{"name": {"Historic reading"}, "object_type": {"customer"}, "field_type": {"text"}, "required": {"true"}}
	fieldPost(t, router, cookie, "/settings/custom-fields", definition, 303)
	created := fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"History customer"}, "custom_field_1": {"Recorded reading"}}, 303)
	path := strings.TrimSuffix(created.Header().Get("Location"), "/edit")
	definition.Set("retired", "true")
	fieldPost(t, router, cookie, "/settings/custom-fields/1", definition, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Recorded reading")
	assertNotContains(t, requestBody(t, router, cookie, "GET", path+"/edit", 200), `name="custom_field_1"`)
	fieldPost(t, router, cookie, path, url.Values{"display_name": {"Preserved customer"}, "custom_field_1": {"Overwrite"}}, 422)
	fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"No backfill"}}, 303)
	definition.Set("retired", "false")
	definition.Set("default_value", "Prospective only")
	fieldPost(t, router, cookie, "/settings/custom-fields/1", definition, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Recorded reading")
	assertContains(t, requestBody(t, router, cookie, "GET", path+"/edit", 200), `name="custom_field_1"`)
}
