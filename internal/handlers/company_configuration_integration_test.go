package handlers

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justinas/nosurf"
)

func TestCompanyConfigurationIndependentViewHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Company reader", []authorization.Grant{{Permission: "configuration.company.view", Scope: authorization.Configuration}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("company-reader@test").SetName("Reader").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, router, cookie, http.MethodGet, "/settings", 200)
	assertContains(t, body, "Business name")
	assertNotContains(t, body, "smtp_password")
	assertNotContains(t, body, "password_min_length")
	assertNotContains(t, body, "next_invoice_number")
	expectStatus(t, router, cookie, http.MethodPost, "/settings?section=company&business_name=Forbidden", 403)
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "No configuration grants", nil, 1); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", "/settings/invoice-logo", 403)
}

func TestCompanyConfigurationUpgradePreservesRecordedFactsHTTP(t *testing.T) {
	ctx := context.Background()
	var invoiceID, customerID int64
	logo := t.TempDir() + "/legacy-logo.png"
	var logoBytes bytes.Buffer
	if err := png.Encode(&logoBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logo, logoBytes.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	_, client, router, sessions, _, admin := permissionHTTPFixtureSeeded(t, func(pool *pgxpool.Pool) {
		var company, customer int64
		if err := pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`).Scan(&company); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE company_settings SET business_name='Preserved company',invoice_prefix='LEG-',next_invoice_number=90,default_due_days=45,invoice_color='legacy-color',invoice_footer='Preserved footer',invoice_logo_path=$1 WHERE company_id=$2`, logo, company); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Preserved customer') RETURNING id`, company).Scan(&customer); err != nil {
			t.Fatal(err)
		}
		customerID = customer
		if err := pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,invoice_number,title,notes,invoice_date,due_date,settlement_state) VALUES($1,$2,70,'Preserved invoice','Recorded note','2025-04-20','2025-05-20','paid') RETURNING id`, company, customer).Scan(&invoiceID); err != nil {
			t.Fatal(err)
		}
	})
	router = middleware.Company(services.NewCompanySettingsService(client))(router)
	cookie := sessionCookie(t, ctx, sessions, admin)
	body := requestBody(t, router, cookie, "GET", "/settings", 200)
	for _, value := range []string{"Preserved company", "Preserved footer", `name="next_invoice_number" value="90"`, `name="default_due_days" value="45"`, "Legacy numbering requires explicit review"} {
		assertContains(t, body, value)
	}
	assertContains(t, body, `type="text" name="invoice_color" value="legacy-color"`)
	assertContains(t, body, "Stored configuration values require review")
	if got := requestBody(t, router, cookie, "GET", "/settings/invoice-logo", 200); got != logoBytes.String() {
		t.Fatal("upgrade changed branding bytes")
	}
	path := fmt.Sprintf("/invoices/%d", invoiceID)
	unreviewed := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, unreviewed, "Recorded note")
	assertContains(t, unreviewed, "identity requires review")
	assertNotContains(t, unreviewed, "LEG-00070")
	expectStatus(t, router, cookie, "GET", path+"/email", 409)
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {configurationRevision(t, body)}, "invoice_color": {"#112233"}}, 303)
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	values := url.Values{"revision": {configurationRevision(t, body)}, "id": {fmt.Sprint(invoiceID)}, "type": {"invoice"}, "number": {"70"}, "next_number": {"90"}, "label": {"VERIFIED-00070"}, "confirmation": {"REVIEWED IDENTITY"}}
	r := httptest.NewRequest("POST", "/settings/numbering-review", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("numbering review %d %s", w.Code, w.Body.String())
	}
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	assertContains(t, body, `name="next_invoice_number" value="90"`)
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "invoice_prefix": {"NEW-"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "VERIFIED-00070")
	expectStatus(t, router, cookie, "POST", path+"/pdf/save", 409)
	expectStatus(t, router, cookie, "GET", path+"/email", 409)
	assertContains(t, requestBody(t, router, cookie, "GET", path+"/edit", 200), "Legacy terms were not recorded")
	r = httptest.NewRequest("POST", path, strings.NewReader(url.Values{"customer_id": {fmt.Sprint(customerID)}, "invoice_number": {"70"}, "title": {"Preserved invoice"}, "notes": {"Recorded note"}, "terms": {"Reviewed legacy terms"}, "invoice_date": {"2025-04-20"}, "due_date": {"2025-05-20"}, "tax_rate": {"0"}, "line_items": {"[]"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	expectStatus(t, router, cookie, "POST", path+"/pdf/save", 303)
}

func TestCompanyConfigurationIsNotExposedThroughFieldAPI(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "Settings boundary test")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "/settings", strings.NewReader(`{"business_name":"API bypass"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("field API %s settings=%d", method, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/settings?section=company&business_name=API+bypass", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("bearer on browser route=%d", w.Code)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/settings", 200), "API bypass")
}

func TestCompanyConfigurationPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser defaults customer").SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(middleware.Company(services.NewCompanySettingsService(client))(router))))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/company-configuration.cjs", server.URL, cookie.Value, fmt.Sprint(customer.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", "/settings", 200)
	assertContains(t, body, "Browser persisted company")
	assertContains(t, body, "Changed while invoice open")
}

func TestCompanyConfigurationLegacySettingsDoNotDiscloseCredentialsHTTP(t *testing.T) {
	_, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cs := client.CompanySettings.Query().OnlyX(ctx)
	client.CompanySettings.UpdateOne(cs).SetSMTPPassword("SECRET_MUST_NOT_RENDER").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/settings/advanced", 200), "SECRET_MUST_NOT_RENDER")
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/setup/company", 200), "SECRET_MUST_NOT_RENDER")
}

func TestCompanyConfigurationRequiresExplicitLegacyNumberReviewHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	// A restored pre-v1 record has no recorded label. Do not infer its old prefix.
	_, err := pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE invoices DISABLE TRIGGER invoice_identity; INSERT INTO customers(company_id,display_name) VALUES(%d,'Legacy'); INSERT INTO invoices(company_id,customer_id,invoice_number,title,invoice_date,due_date) SELECT %d,id,70,'Preserved',current_date,current_date FROM customers WHERE display_name='Legacy'; ALTER TABLE invoices ENABLE TRIGGER invoice_identity`, company, company))
	if err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	body := requestBody(t, router, cookie, "GET", "/settings", 200)
	assertContains(t, body, "Legacy numbering requires explicit review")
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "invoice_prefix": {"NEW-"}}, 409)
}

func TestCompanyConfigurationLogoFailurePreservesBytesHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	uploadColor := uint8(0)
	upload := func(revision string, want int) {
		t.Helper()
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		if err := writer.WriteField("revision", revision); err != nil {
			t.Fatal(err)
		}
		file, err := writer.CreateFormFile("file", "logo.png")
		if err != nil {
			t.Fatal(err)
		}
		image := image.NewRGBA(image.Rect(0, 0, 1, 1))
		image.Set(0, 0, color.RGBA{R: uploadColor, A: 255})
		uploadColor++
		if err = png.Encode(file, image); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/settings/invoice-logo", &buf)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("logo upload=%d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	body := requestBody(t, router, cookie, "GET", "/settings", 200)
	revision := configurationRevision(t, body)
	upload(revision, 303)
	original := requestBody(t, router, cookie, "GET", "/settings/invoice-logo", 200)
	// Fault injection affects new files while retaining read access to the
	// published logo. Assertions remain at the production HTTP boundary.
	dir := filepath.Dir(client.CompanySettings.Query().OnlyX(ctx).InvoiceLogoPath)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	upload(configurationRevision(t, requestBody(t, router, cookie, "GET", "/settings", 200)), 500)
	if after := requestBody(t, router, cookie, "GET", "/settings/invoice-logo", 200); after != original {
		t.Fatal("filesystem failure changed published logo")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	upload(revision, 409)
	if after := requestBody(t, router, cookie, "GET", "/settings/invoice-logo", 200); after != original {
		t.Fatal("stale logo replaced published bytes")
	}
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_config_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_config_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_config_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	upload(configurationRevision(t, body), 409)
	if after := requestBody(t, router, cookie, "GET", "/settings/invoice-logo", 200); after != original {
		t.Fatal("failed logo save replaced published bytes")
	}
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {configurationRevision(t, body)}, "business_name": {"Must rollback"}}, 409)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/settings", 200), "Must rollback")
	t.Log(fmt.Sprintf("stale and audit-failed uploads retained %d original bytes", len(original)))
}

func settingsPost(t *testing.T, router http.Handler, cookie *http.Cookie, values url.Values, want int) string {
	t.Helper()
	r := httptest.NewRequest("POST", "/settings", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("settings POST=%d want %d: %s", w.Code, want, w.Body.String())
	}
	return w.Body.String()
}

func configurationRevision(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatal("missing reviewed revision")
	}
	return m[1]
}

func TestCompanyConfigurationReviewedSaveHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	body := requestBody(t, router, cookie, "GET", "/settings", 200)
	revision := configurationRevision(t, body)
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {revision}, "business_name": {"Reviewed company"}, "timezone": {"Pacific/Auckland"}}, 303)
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	assertContains(t, body, "Reviewed company")
	assertContains(t, body, "Pacific/Auckland")
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {revision}, "business_name": {"Stale overwrite"}}, 409)
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {configurationRevision(t, body)}, "business_name": {"Invalid overwrite"}, "timezone": {"Mars/Olympus"}}, 422)
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	assertContains(t, body, "Reviewed company")
	assertNotContains(t, body, "Invalid overwrite")
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "invoice_prefix": {"ÅR/2026-"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", "/settings", 200), "ÅR/2026-")
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "default_due_days": {"-10"}}, 303)
}

func TestCompanyConfigurationFutureInvoiceDefaultsHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	router = middleware.Company(services.NewCompanySettingsService(client))(router)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	assertContains(t, requestBody(t, router, cookie, "GET", "/invoices/new", 200), `name="due_date" value=""`)
	body := requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "default_invoice_notes": {"Future invoice note"}, "invoice_payment_terms": {"Due upon inspection"}, "default_line_description": {"Service performed"}}, 303)
	form := requestBody(t, router, cookie, "GET", "/invoices/new", 200)
	assertContains(t, form, "Future invoice note")
	assertContains(t, form, "Due upon inspection")
	assertContains(t, form, "Service performed")
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Minimal defaults customer").SaveX(context.Background())
	r := httptest.NewRequest("POST", "/invoices", strings.NewReader(url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Minimal initialized invoice"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":1}]`}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	initializedPath := strings.Split(w.Header().Get("Location"), "?")[0]
	initialized := requestBody(t, router, cookie, "GET", initializedPath+"/edit", 200)
	assertContains(t, initialized, "Future invoice note")
	assertContains(t, initialized, "Due upon inspection")
	assertContains(t, initialized, "Service performed")
	expectStatus(t, router, cookie, "POST", initializedPath+"/finalize", 303)
	dashboard := requestBody(t, router, cookie, "GET", "/", 200)
	assertContains(t, dashboard, "1 unpaid")
	assertNotContains(t, dashboard, "1 overdue")
	settingsBody := requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {configurationRevision(t, settingsBody)}, "timezone": {"Etc/GMT+12"}}, 303)
	zone, err := time.LoadLocation("Etc/GMT+12")
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().In(zone).Format("2006-01-02")
	r = httptest.NewRequest("POST", initializedPath, strings.NewReader(url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "invoice_number": {"1"}, "due_date": {today}, "tax_rate": {"0"}, "line_items": {`[{"title":"Work","description":"Service performed","quantity":1,"unit_price":1}]`}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	todayDashboard := requestBody(t, router, cookie, "GET", "/", 200)
	assertNotContains(t, todayDashboard, "1 overdue")
	assertNotContains(t, todayDashboard, `dashboard-danger">$1.00`)
	settingsBody = requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {configurationRevision(t, settingsBody)}, "timezone": {"UTC"}}, 303)
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "default_due_days": {""}}, 303)
	noDue := requestBody(t, router, cookie, "GET", "/invoices/new", 200)
	assertContains(t, noDue, `name="due_date" value=""`)
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	assertContains(t, body, "next_estimate_number")
	assertContains(t, body, "lease_prefix")
	assertContains(t, body, "next_lease_number")
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "default_estimate_notes": {"Future estimate note"}, "estimate_terms": {"Estimate terms"}, "next_estimate_number": {"100"}, "estimate_valid_days": {"14"}}, 303)
	form = requestBody(t, router, cookie, "GET", "/estimates/new", 200)
	assertContains(t, form, "Future estimate note")
	assertContains(t, form, "Estimate terms")
	assertContains(t, form, `name="valid_through" value="`+time.Now().UTC().AddDate(0, 0, 14).Format("2006-01-02")+`"`)
}

func TestCompanyConfigurationInvoiceRetainsExplicitContentHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	router = middleware.Company(services.NewCompanySettingsService(client))(router)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Defaults customer").SaveX(context.Background())
	values := url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {""}, "notes": {"Entered note"}, "terms": {"Entered terms"}, "invoice_date": {"2026-10-11"}, "due_date": {"2026-10-25"}, "tax_rate": {"0"}, "line_items": {"[]"}}
	post := func(want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/invoices", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("invoice POST=%d want %d %s", w.Code, want, w.Body.String())
		}
		return w
	}
	w := post(422)
	assertContains(t, w.Body.String(), "Entered note")
	assertContains(t, w.Body.String(), "Entered terms")
	values.Set("title", "Explicit document")
	values.Set("invoice_date", "not-a-date")
	w = post(422)
	assertContains(t, w.Body.String(), "Entered note")
	values.Set("invoice_date", "2026-10-11")
	body := requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "invoice_payment_terms": {"Changed default"}}, 303)
	values.Set("title", "Explicit document")
	w = post(303)
	location := strings.Split(w.Header().Get("Location"), "?")[0]
	edit := requestBody(t, router, cookie, "GET", location+"/edit", 200)
	assertContains(t, edit, "Entered note")
	assertContains(t, edit, "Entered terms")
	assertNotContains(t, edit, "Changed default")
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "invoice_prefix": {"NEW-"}}, 303)
	show := requestBody(t, router, cookie, "GET", location, 200)
	assertContains(t, show, "INV-00001")
	assertNotContains(t, show, "NEW-00001")
	expectStatus(t, router, cookie, "GET", location+"/pdf", 404)
	expectStatus(t, router, cookie, "POST", location+"/pdf/save", 303)
	pdf := requestBody(t, router, cookie, "GET", location+"/pdf", 200)
	assertContains(t, pdf, "%PDF-")
	preview := requestBody(t, router, cookie, "GET", location+"/pdf/preview", 200)
	version := regexp.MustCompile(`name="version" value="([0-9]+)"`).FindStringSubmatch(preview)
	if len(version) != 2 {
		t.Fatal("saved PDF version is not selectable")
	}
	stampPattern := regexp.MustCompile(`Version ` + version[1] + ` · ([^<]+)`)
	beforeStamp := stampPattern.FindStringSubmatch(preview)
	if len(beforeStamp) != 2 {
		t.Fatal("PDF generation instant is not visible")
	}
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {configurationRevision(t, body)}, "invoice_footer": {"New PDF presentation"}}, 303)
	expectStatus(t, router, cookie, "GET", location+"/pdf?version="+version[1], 409)
	old := requestBody(t, router, cookie, "GET", location+"/pdf?version="+version[1]+"&confirm_stale=1", 200)
	if old != pdf {
		t.Fatal("presentation change rewrote prior PDF bytes")
	}
	expectStatus(t, router, cookie, "POST", location+"/pdf/save", 303)
	fresh := requestBody(t, router, cookie, "GET", location+"/pdf", 200)
	if fresh == pdf {
		t.Fatal("regeneration did not use current presentation")
	}
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {configurationRevision(t, body)}, "timezone": {"America/Los_Angeles"}}, 303)
	show = requestBody(t, router, cookie, "GET", location, 200)
	assertContains(t, show, "Oct 11, 2026")
	assertNotContains(t, show, "Oct 10, 2026")
	afterStamp := stampPattern.FindStringSubmatch(requestBody(t, router, cookie, "GET", location+"/pdf/preview", 200))
	if len(afterStamp) != 2 || afterStamp[1] == beforeStamp[1] {
		t.Fatal("company timezone did not change display of saved PDF instant")
	}
	cs := client.CompanySettings.Query().OnlyX(context.Background())
	client.CompanySettings.UpdateOne(cs).SetInvoiceLogoPath("/nonexistent/ticket-25-logo.png").SaveX(context.Background())
	expectStatus(t, router, cookie, "POST", location+"/pdf/save", 500)
	if old := requestBody(t, router, cookie, "GET", location+"/pdf?version="+version[1]+"&confirm_stale=1", 200); old != pdf {
		t.Fatal("failed regeneration replaced saved bytes")
	}
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "default_due_days": {"365"}}, 303)
	expectStatus(t, router, cookie, "POST", location+"/finalize", 303)
	finalized := requestBody(t, router, cookie, "GET", location, 200)
	assertContains(t, finalized, "Oct 11, 2026")
	assertContains(t, finalized, "Oct 25, 2026")
}

func TestCompanyConfigurationEstimateSnapshotsHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	router = middleware.Company(services.NewCompanySettingsService(client))(router)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Estimate customer").SaveX(context.Background())
	body := requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "next_estimate_number": {"100"}}, 303)
	r := httptest.NewRequest("POST", "/estimates", strings.NewReader(url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Saved estimate"}, "notes": {"Entered estimate note"}, "terms": {"Entered estimate terms"}, "tax_rate": {"0"}, "line_items": {"[]"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("estimate: %d %s", w.Code, w.Body.String())
	}
	body = requestBody(t, router, cookie, "GET", "/estimates", 200)
	match := regexp.MustCompile(`href="(/estimates/[0-9]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("created estimate missing from list")
	}
	edit := requestBody(t, router, cookie, "GET", match[1]+"/edit", 200)
	assertContains(t, edit, "Entered estimate terms")
	assertContains(t, requestBody(t, router, cookie, "GET", match[1], 200), "EST-00100")
	expectStatus(t, router, cookie, "GET", match[1]+"/pdf", 404)
	expectStatus(t, router, cookie, "POST", match[1]+"/pdf/save", 303)
	assertContains(t, requestBody(t, router, cookie, "GET", match[1]+"/pdf", 200), "%PDF-")
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "default_due_days": {"12"}}, 303)
	r = httptest.NewRequest("POST", match[1]+"/convert", strings.NewReader(url.Values{"idempotency_key": {uuid.NewString()}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	convertedPath := strings.Split(w.Header().Get("Location"), "?")[0]
	converted := requestBody(t, router, cookie, "GET", convertedPath+"/edit", 200)
	assertContains(t, converted, "Entered estimate terms")
	assertContains(t, converted, `name="due_date" value="`+time.Now().UTC().AddDate(0, 0, 12).Format("2006-01-02")+`"`)
}

func TestCompanyConfigurationCustomerDefaultsPrecedenceHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	router = middleware.Company(services.NewCompanySettingsService(client))(router)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Default precedence").SaveX(ctx)
	path := fmt.Sprintf("/customers/%d/document-defaults", c.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	r := httptest.NewRequest("POST", path, strings.NewReader(url.Values{"revision": {configurationRevision(t, body)}, "invoice_notes": {"Customer note"}, "invoice_terms": {""}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("customer defaults %d %s", w.Code, w.Body.String())
	}
	form := requestBody(t, router, cookie, "GET", fmt.Sprintf("/invoices/new?customer_id=%d", c.ID), 200)
	assertContains(t, form, "Customer note")
	assertContains(t, form, `<textarea name="terms" rows="3"></textarea>`)
	fromCustomer := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/create-invoice", c.ID), 200)
	assertContains(t, fromCustomer, "Customer note")
	defaultsBody := requestBody(t, router, cookie, "GET", path, 200)
	r = httptest.NewRequest("POST", path, strings.NewReader(url.Values{"revision": {configurationRevision(t, defaultsBody)}, "invoice_terms": {"Customer job invoice terms"}, "estimate_terms": {"Customer job estimate terms"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Defaults source job").SetNotes("Explicit source Job note").SaveX(ctx)
	jobForm := requestBody(t, router, cookie, "POST", fmt.Sprintf("/jobs/%d/create-invoice", job.ID), 200)
	assertContains(t, jobForm, "Customer job invoice terms")
	assertContains(t, jobForm, "Explicit source Job note")
	r = httptest.NewRequest("POST", fmt.Sprintf("/jobs/%d/create-estimate", job.ID), nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	estimatePath := strings.Split(w.Header().Get("Location"), "?")[0]
	assertContains(t, requestBody(t, router, cookie, "GET", estimatePath+"/edit", 200), "Customer job estimate terms")
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Operational Customer editor", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("operational-defaults@test").SetName("Operational editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	operational := sessionCookie(t, ctx, sessions, u.ID)
	expectStatus(t, router, operational, "GET", path, 403)
	expectStatus(t, router, operational, "POST", path, 403)
}

func TestCompanyConfigurationConcurrentSaveAndAuthorityLossHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Delegated company management", []authorization.Grant{{Permission: "configuration.company.view", Scope: authorization.Configuration}, {Permission: "configuration.company.manage", Scope: authorization.Configuration}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("manager@test").SetName("Manager").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, router, cookie, "GET", "/settings", 200)
	revision := configurationRevision(t, body)
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {revision}, "business_name": {"Rejected mixed edit"}, "invoice_prefix": {"OTHER-"}}, 422)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/settings", 200), "Rejected mixed edit")
	post := func(name string) int {
		r := httptest.NewRequest("POST", "/settings", strings.NewReader(url.Values{"section": {"company"}, "revision": {revision}, "business_name": {name}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	results := make(chan int, 2)
	go func() { results <- post("First reviewed editor") }()
	go func() { results <- post("Second reviewed editor") }()
	a, b := <-results, <-results
	if !((a == 303 && b == 409) || (a == 409 && b == 303)) {
		t.Fatalf("racing writes=%d,%d", a, b)
	}
	body = requestBody(t, router, cookie, "GET", "/settings", 200)
	revision = configurationRevision(t, body)
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {revision}, "invoice_prefix": {"DENIED-"}}, 403)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	go func() { results <- post("Revoked while waiting") }()
	if _, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='configuration.company.manage'`, role); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if status := <-results; status != 403 {
		t.Fatalf("revoked write=%d", status)
	}
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/settings", 200), "Revoked while waiting")
}

func TestCompanyConfigurationConcurrentNumberAllocationHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	router = middleware.Company(services.NewCompanySettingsService(client))(router)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Concurrent numbering").SaveX(ctx)
	reviewedCompanyRevision := configurationRevision(t, requestBody(t, router, cookie, "GET", "/settings", 200))
	results := make(chan *httptest.ResponseRecorder, 6)
	for n := 0; n < 6; n++ {
		go func(n int) {
			r := httptest.NewRequest("POST", "/invoices", strings.NewReader(url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {fmt.Sprintf("Concurrent invoice %d", n)}, "invoice_date": {"2026-10-11"}, "due_date": {"2026-10-25"}, "tax_rate": {"0"}, "line_items": {"[]"}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			results <- w
		}(n)
	}
	labels := map[string]bool{}
	for n := 0; n < 6; n++ {
		w := <-results
		if w.Code != 303 {
			t.Fatalf("invoice allocation %d %s", w.Code, w.Body.String())
		}
		body := requestBody(t, router, cookie, "GET", w.Header().Get("Location"), 200)
		label := regexp.MustCompile(`INV-[0-9]{5}`).FindString(body)
		if label == "" || labels[label] {
			t.Fatalf("duplicate or missing label %q", label)
		}
		labels[label] = true
	}
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {reviewedCompanyRevision}, "business_name": {"Profile survives ordinary number allocation"}}, 303)
	body := requestBody(t, router, cookie, "GET", "/settings", 200)
	settingsPost(t, router, cookie, url.Values{"section": {"documents"}, "revision": {configurationRevision(t, body)}, "next_invoice_number": {"1"}}, 422)
}

func TestCompanyConfigurationPDFRejectsPresentationABADuringGenerationHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	router = middleware.Company(services.NewCompanySettingsService(client))(router)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("PDF concurrency").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Reviewed source").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("POST", fmt.Sprintf("/invoices/%d/pdf/save", i.ID), nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		result <- w.Code
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid WHERE a.application_name=$1 AND l.locktype='advisory' AND NOT l.granted)`, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("generation did not reach commit guard")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := tx.Exec(ctx, `UPDATE company_settings SET invoice_footer='Intervening presentation' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE company_settings SET invoice_footer='' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if status := <-result; status != 409 {
		t.Fatalf("ABA generation=%d", status)
	}
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/invoices/%d/pdf", i.ID), 404)
}

func TestCompanyConfigurationPDFUsesIndependentGrantsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	router = middleware.Company(services.NewCompanySettingsService(client))(router)
	ctx := context.Background()
	svc := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.pdf.generate", Scope: authorization.All}, {Permission: "invoice.pdf.download", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}, {Permission: "pricing.view", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Only reviewed PDF capabilities", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("pdf-grants@test").SetName("PDF user").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("PDF grants customer").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("PDF grants invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/invoices/%d/pdf/save", i.ID), 303)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/invoices/%d/pdf", i.ID), 200), "%PDF-")
	expectStatus(t, router, cookie, "GET", "/settings", 403)
	for n := range grants {
		if strings.HasPrefix(grants[n].Permission, "invoice.pdf.") {
			grants[n].Scope = authorization.Assigned
		}
	}
	grants = append(grants, authorization.Grant{Permission: "job.view", Scope: authorization.Assigned})
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Assigned PDF capabilities", grants, 1); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/invoices/%d/pdf/save", i.ID), 403)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned PDF Job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(u.ID).SaveX(ctx)
	client.Invoice.UpdateOne(i).SetJobID(job.ID).SaveX(ctx)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/invoices/%d/pdf/save", i.ID), 303)
	var versionsBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM document_pdf_versions WHERE document_type='invoice' AND document_id=$1`, i.ID).Scan(&versionsBefore); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// Assignment writers now acquire the authorization fence through a
	// statement trigger. Take it first, matching production lock order, so
	// revocation wins without manufacturing a Job/fence deadlock.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	var blockerPID int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	pending := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("POST", fmt.Sprintf("/invoices/%d/pdf/save", i.ID), nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		pending <- w.Code
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)) AND wait_event='advisory')`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PDF did not reach its authorization commit fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if status := <-pending; status != 403 {
		t.Errorf("generation after assignment loss=%d", status)
	}
	var versionsAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM document_pdf_versions WHERE document_type='invoice' AND document_id=$1`, i.ID).Scan(&versionsAfter); err != nil {
		t.Fatal(err)
	}
	if versionsAfter != versionsBefore {
		t.Fatalf("assignment loss published a PDF: versions before=%d after=%d", versionsBefore, versionsAfter)
	}
	client.Job.UpdateOne(job).SetDeletedAt(time.Now()).SaveX(ctx)
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/invoices/%d/pdf", i.ID), 403)
	assertNotContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/invoices/%d/pdf/preview", i.ID), 200), "Generate new PDF version")
}

func TestCompanyConfigurationCreationRechecksAuthorityAndAuditHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.create", Scope: authorization.All}, {Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.create", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}, {Permission: "pricing.view", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Creation grants", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("creation-race@test").SetName("Creator").SetPasswordHash("hash").SetRole("dispatcher").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Creation scope").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	post := func(kind, title string) int {
		r := httptest.NewRequest("POST", "/"+kind+"s", strings.NewReader(url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {title}, "line_items": {"[]"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	for _, kind := range []string{"invoice", "estimate"} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019); SELECT id FROM company_settings FOR UPDATE`); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		result := make(chan int, 1)
		go func() { result <- post(kind, "Rejected "+kind) }()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var waiting bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&waiting); err != nil {
				tx.Rollback(ctx)
				t.Fatal(err)
			}
			if waiting {
				break
			}
			if time.Now().After(deadline) {
				tx.Rollback(ctx)
				t.Fatal("creation did not reach its commit lock")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, kind+".create"); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if status := <-result; status != 403 {
			t.Fatalf("%s creation after revocation=%d", kind, status)
		}
		assertNotContains(t, requestBody(t, router, cookie, "GET", "/"+kind+"s", 200), "Rejected "+kind)
	}
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_creation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('invoice.create','estimate.create') THEN RAISE EXCEPTION 'injected creation audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_creation_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_creation_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	cookie = sessionCookie(t, ctx, sessions, admin)
	for _, kind := range []string{"invoice", "estimate"} {
		if status := post(kind, "Audit rejected "+kind); status < 400 {
			t.Fatalf("%s accepted without immutable audit", kind)
		}
		assertNotContains(t, requestBody(t, router, cookie, "GET", "/"+kind+"s", 200), "Audit rejected "+kind)
	}
	e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Conversion audit source").SaveX(ctx)
	r := httptest.NewRequest("POST", fmt.Sprintf("/estimates/%d/convert", e.ID), strings.NewReader(url.Values{"idempotency_key": {uuid.NewString()}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatalf("conversion without creation audit=%d %s", w.Code, w.Body.String())
	}
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/estimates/%d", e.ID), 200)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/invoices", 200), "Conversion audit source")
}
