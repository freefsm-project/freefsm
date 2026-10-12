package handlers

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestFieldPDFInclusionIsOptInAndVersionsRemainImmutableHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("PDF fields customer").SaveX(ctx)
	definition := url.Values{"name": {"Private reading"}, "object_type": {"invoice"}, "field_type": {"text"}}
	fieldPost(t, router, cookie, "/settings/custom-fields", definition, 303)
	created := fieldPost(t, router, cookie, "/invoices", url.Values{"title": {"Field PDF"}, "customer_id": {fmt.Sprint(customer.ID)}, "line_items": {"[]"}, "invoice_date": {"2026-10-12"}, "custom_field_1": {"PDF FIELD VALUE"}}, 303)
	path := strings.Split(created.Header().Get("Location"), "?")[0]
	fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 303)
	text := func(data string) string {
		t.Helper()
		cmd := exec.Command("pdftotext", "-", "-")
		cmd.Stdin = strings.NewReader(data)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("PDF text: %v %s", err, out)
		}
		return string(out)
	}
	original := requestBody(t, router, cookie, "GET", path+"/pdf", 200)
	preview := requestBody(t, router, cookie, "GET", path+"/pdf/preview", 200)
	version := regexp.MustCompile(`version=([0-9]+)`).FindStringSubmatch(preview)
	if len(version) != 2 {
		t.Fatal("missing immutable PDF version")
	}
	assertNotContains(t, text(original), "PDF FIELD VALUE")
	definition.Set("include_pdf", "true")
	definition.Set("help_text", "Current opt-in")
	fieldPost(t, router, cookie, "/settings/custom-fields/1", definition, 303)
	fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 303)
	assertContains(t, text(requestBody(t, router, cookie, "GET", path+"/pdf", 200)), "PDF FIELD VALUE")
	if old := requestBody(t, router, cookie, "GET", path+"/pdf?version="+version[1]+"&confirm_stale=1", 200); old != original {
		t.Fatal("old PDF bytes changed")
	}
}
