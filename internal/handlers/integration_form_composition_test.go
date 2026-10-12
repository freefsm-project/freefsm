package handlers

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/google/uuid"
)

func compositionInput(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatalf("missing input %s", name)
	}
	return html.UnescapeString(m[1])
}

func TestIntegrationCustomerTypedErrorIdentityHTTP(t *testing.T) {
	_, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Readout"}, "object_type": {"customer"}, "field_type": {"number"}, "precision": {"2"}}, 303)
	form := requestBody(t, router, cookie, "GET", "/customers/new", 200)
	values := url.Values{"operation_id": {compositionInput(t, form, "operation_id")}, "display_name": {"Retained customer"}, "first_name": {"Retained first"}, "service_notes": {"Retained service"}, "billing_city": {"Retained city"}, "tax_exempt": {"true"}, "custom_field_1": {"not-a-number"}}
	errorForm := fieldPost(t, router, cookie, "/customers", values, 422).Body.String()
	check := func(body, revision string) {
		t.Helper()
		key := compositionInput(t, body, "operation_id")
		if _, err := uuid.Parse(key); err != nil || key == values.Get("operation_id") {
			t.Fatalf("error form did not issue a fresh operation ID: %q (%v)", key, err)
		}
		if got := compositionInput(t, body, "revision"); got != revision {
			t.Fatalf("revision=%q want submitted %q", got, revision)
		}
		for _, retained := range []string{"Retained customer", "Retained first", "Retained service", "Retained city", "not-a-number"} {
			assertContains(t, body, retained)
		}
		values.Set("operation_id", key)
	}
	check(errorForm, "")
	values.Set("custom_field_1", "1.25")
	created := fieldPost(t, router, cookie, "/customers", values, 303)
	path := strings.TrimSuffix(created.Header().Get("Location"), "/edit")
	c := client.Customer.Query().OnlyX(ctx)
	if c.BillingCity != "Retained city" || !c.TaxExempt || c.ServiceNotes != "Retained service" {
		t.Fatalf("corrected creation lost submitted values: %+v", c)
	}
	form = requestBody(t, router, cookie, "GET", path+"/edit", 200)
	revision := compositionInput(t, form, "revision")
	values.Set("revision", revision)
	values.Set("operation_id", compositionInput(t, form, "operation_id"))
	values.Set("custom_field_1", "not-a-number")
	errorForm = fieldPost(t, router, cookie, path, values, 422).Body.String()
	check(errorForm, revision)
	// A competing save after the validation response must not be overwritten
	// by correcting the field in that retained form.
	current := client.Customer.UpdateOneID(c.ID).SetDisplayName("Concurrent saved name").SetUpdatedAt(time.Now().Add(time.Second)).SaveX(ctx)
	values.Set("custom_field_1", "2.50")
	conflict := fieldPost(t, router, cookie, path, values, 409).Body.String()
	assertContains(t, conflict, "Conflict: compare with the current saved Customer")
	assertContains(t, conflict, "Concurrent saved name")
	assertContains(t, conflict, "Retained customer")
	if got := client.Customer.GetX(ctx, c.ID); got.DisplayName != current.DisplayName || got.CustomFields != c.CustomFields {
		t.Fatal("stale correction overwrote the competing save")
	}
	// The explicit comparison response can offer the new revision for review.
	if compositionInput(t, conflict, "revision") != authorization.CustomerRevision(current) {
		t.Fatal("conflict comparison did not identify the current revision")
	}
	values.Set("revision", compositionInput(t, conflict, "revision"))
	values.Set("operation_id", compositionInput(t, conflict, "operation_id"))
	values.Set("conflict_review", "on")
	fieldPost(t, router, cookie, path, values, 303)
	if got := client.Customer.GetX(ctx, c.ID); got.DisplayName != "Retained customer" || !strings.Contains(got.CustomFields, "2.50") {
		t.Fatalf("reviewed correction not saved: %+v", got)
	}
}

type compositionSender struct{ messages []delivery.Delivery }

func (s *compositionSender) Send(_ context.Context, d delivery.Delivery) (delivery.SendResult, error) {
	s.messages = append(s.messages, d)
	return delivery.SendResult{ProviderIdentifier: d.MessageID}, nil
}

func TestIntegrationSelectedPDFHTMLCompositionHTTP(t *testing.T) {
	for _, kind := range []string{"invoice", "estimate"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Captured & customer").SetEmail("captured@example.test").SaveX(ctx)
			var id int64
			if kind == "invoice" {
				id = client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Captured <title>").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx).ID
			} else {
				id = client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Captured <title>").SaveX(ctx).ID
			}
			path := fmt.Sprintf("/%ss/%d", kind, id)
			if _, err := pool.Exec(ctx, `UPDATE company_settings SET invoice_email_html=$1, estimate_email_html=$1, smtp_from='frozen@example.test', smtp_host='smtp.example.test', smtp_port=2525 WHERE company_id=$2`, "<p>{customer_name}: {document_title}</p>", company); err != nil {
				t.Fatal(err)
			}
			expectStatus(t, router, cookie, "POST", path+"/pdf/save", 303)
			preview := requestBody(t, router, cookie, "GET", path+"/pdf/preview", 200)
			version := compositionInput(t, preview, "version")
			original := requestBody(t, router, cookie, "GET", path+"/pdf?version="+version, 200)
			client.Customer.UpdateOne(c).SetDisplayName("Live replacement customer").SaveX(ctx)
			if kind == "invoice" {
				client.Invoice.UpdateOneID(id).SetTitle("Live replacement title").SaveX(ctx)
			} else {
				client.Estimate.UpdateOneID(id).SetTitle("Live replacement title").SaveX(ctx)
			}
			compose := requestBody(t, router, cookie, "GET", path+"/email?version="+version, 200)
			assertContains(t, compose, html.EscapeString("<p>Captured &amp; customer: Captured &lt;title&gt;</p>"))
			assertNotContains(t, compose, "Live replacement")
			htmlBody := "<h2>Submitted &amp; exact HTML</h2>"
			values := url.Values{"pdf_version": {version}, "idempotency_key": {uuid.NewString()}, "to": {"invalid"}, "subject": {"Selected message"}, "body": {"Text alternative"}, "html_body": {htmlBody}}
			invalid := fieldPost(t, router, cookie, path+"/email", values, 422).Body.String()
			assertContains(t, invalid, html.EscapeString(htmlBody))
			values.Set("to", "recipient@example.test")
			stale := fieldPost(t, router, cookie, path+"/email", values, 409).Body.String()
			assertContains(t, stale, html.EscapeString(htmlBody))
			assertContains(t, stale, `name="pdf_version" value="`+version+`"`)
			values.Set("confirm_stale_pdf", "1")
			fieldPost(t, router, cookie, path+"/email", values, 303)
			var savedHTML, sender string
			var pdf []byte
			var savedVersion int64
			if err := pool.QueryRow(ctx, `SELECT html_body,pdf_data,pdf_version_id,sender_address FROM document_deliveries WHERE idempotency_key=$1`, values.Get("idempotency_key")).Scan(&savedHTML, &pdf, &savedVersion, &sender); err != nil {
				t.Fatal(err)
			}
			if savedHTML != htmlBody || !bytes.Equal(pdf, []byte(original)) || fmt.Sprint(savedVersion) != version || sender != "frozen@example.test" {
				t.Fatal("queued snapshot changed HTML, PDF selection/bytes or sender")
			}
			if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='changed@example.test', invoice_email_html='changed', estimate_email_html='changed' WHERE company_id=$1`, company); err != nil {
				t.Fatal(err)
			}
			worker := delivery.New(pool, "")
			transport := &compositionSender{}
			worked, err := worker.ProcessOne(ctx, transport, delivery.NopAcceptanceHook{})
			if err != nil || !worked || len(transport.messages) != 1 {
				t.Fatalf("process delivery: worked=%v err=%v messages=%d", worked, err, len(transport.messages))
			}
			sent := transport.messages[0]
			if sent.HTMLBody != htmlBody || sent.TextBody != values.Get("body") || sent.From != sender || !bytes.Equal(sent.PDF, pdf) {
				t.Fatal("worker did not send the frozen HTML/text/PDF/sender snapshot")
			}
			fieldPost(t, router, cookie, path+"/email", values, 303)
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM document_deliveries`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("replay delivery count=%d err=%v", count, err)
			}
			values.Set("idempotency_key", uuid.NewString())
			values.Set("html_body", "")
			values.Set("body", "Plain <text>\nSecond line")
			fieldPost(t, router, cookie, path+"/email", values, 303)
			if err := pool.QueryRow(ctx, `SELECT html_body FROM document_deliveries WHERE idempotency_key=$1`, values.Get("idempotency_key")).Scan(&savedHTML); err != nil {
				t.Fatal(err)
			}
			if savedHTML != "<!doctype html><html><body><p>Plain &lt;text&gt;<br>\nSecond line</p></body></html>" {
				t.Fatalf("empty HTML did not use escaped text fallback: %s", savedHTML)
			}
		})
	}
}
