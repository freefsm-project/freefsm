package handlers

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPDFCurrentPresentationFieldsLogoAndDateSnapshotHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Presentation customer").SaveX(ctx)
	types := []string{"text", "textarea", "number", "date", "checkbox", "select", "multiselect"}
	for n, kind := range types {
		fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {fmt.Sprintf("Rendered field %d", n+1)}, "object_type": {"invoice"}, "field_type": {kind}, "include_pdf": {"true"}, "precision": {"2"}, "options": {`["One","Two"]`}}, 303)
	}
	fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Presentation invoice"}, "invoice_date": {"2026-01-02"}, "due_date": {"2026-02-03"}, "tax_rate": {"0"}, "line_items": {`[{"title":"Work","description":"Visible description","quantity":1,"unit_price":100}]`}, "custom_field_1": {"Visible text"}, "custom_field_2": {"Visible multiline"}, "custom_field_3": {"12.30"}, "custom_field_4": {"2026-03-04"}, "custom_field_5": {"true"}, "custom_field_6": {"One"}, "custom_field_7[]": {"One", "Two"}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id))
	path := fmt.Sprintf("/invoices/%d", id)
	logo := filepath.Join(t.TempDir(), "captured.png")
	writeLogo := func(shade color.RGBA) {
		t.Helper()
		picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
		picture.Set(0, 0, shade)
		file, err := os.Create(logo)
		historyCheck(t, err)
		historyCheck(t, png.Encode(file, picture))
		historyCheck(t, file.Close())
	}
	writeLogo(color.RGBA{R: 255, A: 255})
	_, err := pool.Exec(ctx, `UPDATE company_settings SET business_name='Current PDF branding',invoice_footer='Captured footer',invoice_logo_path=$1,pdf_show_line_item_descriptions=true WHERE company_id=$2`, logo, company)
	historyCheck(t, err)
	fieldPost(t, web, cookie, path+"/pdf/save", url.Values{}, 303)
	original := requestBody(t, web, cookie, "GET", path+"/pdf", 200)
	text := func(pdf string) string {
		t.Helper()
		command := exec.Command("pdftotext", "-", "-")
		command.Stdin = strings.NewReader(pdf)
		out, err := command.CombinedOutput()
		historyCheck(t, err)
		return string(out)
	}
	for _, want := range []string{"DRAFT", "Current PDF branding", "Captured footer", "Visible description", "Visible text", "Visible multiline", "12.30", "2026-03-04", "Yes", "One", "Two"} {
		assertContains(t, text(original), want)
	}
	var version int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM document_pdf_versions`).Scan(&version))
	_, err = pool.Exec(ctx, `UPDATE invoices SET tax_rate='0.00%',line_items=jsonb_set(line_items,'{0,quantity}','1.00') WHERE id=$1`, id)
	historyCheck(t, err)
	if got := requestBody(t, web, cookie, "GET", path+"/pdf", 200); got != original {
		t.Fatal("equivalent numeric aliases changed selection")
	}
	writeLogo(color.RGBA{B: 255, A: 255})
	expectStatus(t, web, cookie, "GET", path+"/pdf", 404)
	_, err = pool.Exec(ctx, `UPDATE company_settings SET invoice_footer='New footer',pdf_show_line_item_descriptions=false WHERE company_id=$1`, company)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `UPDATE invoices SET due_date='2026-04-05' WHERE id=$1`, id)
	historyCheck(t, err)
	fieldPost(t, web, cookie, path+"/pdf/save", url.Values{}, 303)
	current := requestBody(t, web, cookie, "GET", path+"/pdf", 200)
	assertContains(t, text(current), "New footer")
	assertNotContains(t, text(current), "Visible description")
	_, err = pool.Exec(ctx, `UPDATE invoices SET line_items=jsonb_set(line_items,'{0,description}','"Unrendered description edit"') WHERE id=$1`, id)
	historyCheck(t, err)
	logoBytes, err := os.ReadFile(logo)
	historyCheck(t, err)
	relocated := filepath.Join(t.TempDir(), "relocated.png")
	historyCheck(t, os.WriteFile(relocated, logoBytes, 0600))
	_, err = pool.Exec(ctx, `UPDATE company_settings SET invoice_logo_path=$1 WHERE company_id=$2`, relocated, company)
	historyCheck(t, err)
	if got := requestBody(t, web, cookie, "GET", path+"/pdf", 200); got != current {
		t.Fatal("unrendered description or same-byte branding relocation caused drift")
	}
	if got := requestBody(t, web, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d&confirm_stale=1", path, version), 200); got != original {
		t.Fatal("presentation changed old bytes")
	}
}
