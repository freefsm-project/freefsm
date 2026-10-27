package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestCommercialArchiveRestorePersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Restore browser owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Historical browser Job").SaveX(ctx)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
	historyCheck(t, err)
	ids := map[string]int64{}
	before := map[string]string{}
	for _, kind := range []string{"estimate", "invoice"} {
		values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Browser history " + kind}, "line_items": {"[]"}, "tax_rate": {"0"}, "terms": {"Captured browser terms"}, "invoice_date": {"2026-01-02"}}
		fieldPost(t, web, adminCookie, "/"+kind+"s", values, 303)
		var id int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM `+kind+`s WHERE customer_id=$1`, c.ID).Scan(&id))
		ids[kind] = id
		path := fmt.Sprintf("/%ss/%d", kind, id)
		if kind == "invoice" {
			fieldPost(t, web, adminCookie, path+"/finalize", nil, 303)
		}
		expectStatus(t, web, adminCookie, "POST", path+"/pdf/save", 303)
		version := compositionInput(t, requestBody(t, web, adminCookie, "GET", path+"/pdf/preview", 200), "version")
		fieldPost(t, web, adminCookie, path+"/email", url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"retained@example.test"}, "subject": {"Snapshot"}, "body": {"Retained message"}}, 303)
		fieldPost(t, web, adminCookie, path+"/delete", nil, 303)
		var snapshot string
		historyCheck(t, pool.QueryRow(ctx, `SELECT (to_jsonb(d)-'deleted_at'-'updated_at'-'commercial_revision')::text FROM `+kind+`s d WHERE id=$1`, id).Scan(&snapshot))
		before[kind] = snapshot
	}
	client.Job.UpdateOneID(j.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	for _, kind := range []string{"estimate", "invoice"} {
		fieldPost(t, web, adminCookie, "/settings/custom-fields", url.Values{"object_type": {kind}, "name": {"New required field"}, "field_type": {"text"}, "required": {"true"}, "default_value": {"Must not backfill restored history"}}, 303)
	}
	_, err = pool.Exec(ctx, `INSERT INTO customer_document_defaults(customer_id,defaults) VALUES($1,'{"estimate_terms":"New Customer default","invoice_terms":"New Customer default"}')`, c.ID)
	historyCheck(t, err)
	mailBefore := historyDurableRows(t, pool, []string{"document_deliveries", "document_delivery_events", "document_pdf_versions"})
	var cancelled int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_deliveries WHERE state='cancelled' AND last_error<>''`).Scan(&cancelled))
	if cancelled != 2 {
		t.Fatalf("cancelled deliveries with reason=%d", cancelled)
	}
	svc := authorization.New(pool)
	var grants []authorization.Grant
	for _, kind := range []string{"estimate", "invoice"} {
		for _, action := range []string{"view", "archived.view", "restore"} {
			grants = append(grants, authorization.Grant{Permission: kind + "." + action, Scope: authorization.All})
		}
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser historical restoration", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("browser-restorer@test").SetName("Historical custodian").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, phase := range []string{"denied", "inactive", "allowed"} {
		next := grants
		if phase == "denied" {
			next = []authorization.Grant{grants[0], grants[1], grants[3], grants[4]}
		}
		var revision int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
		_, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Renamed browser custodian", next, revision)
		historyCheck(t, err)
		if phase == "inactive" {
			client.Customer.UpdateOneID(c.ID).SetDeletedAt(time.Now()).SaveX(ctx)
		} else {
			client.Customer.UpdateOneID(c.ID).ClearDeletedAt().SaveX(ctx)
		}
		browserCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		script, err := filepath.Abs("testdata/browser/commercial-archive.cjs")
		historyCheck(t, err)
		command := exec.CommandContext(browserCtx, "node", script, server.URL, sessionCookie(t, ctx, sessions, u.ID).Value, fmt.Sprint(ids["estimate"]), fmt.Sprint(ids["invoice"]), phase)
		command.Dir = t.TempDir()
		if runtime.GOOS == "linux" {
			command.Env = append(os.Environ(), "TMPDIR=/proc/self/cwd")
		}
		out, err := command.CombinedOutput()
		cancel()
		t.Logf("browser %s: %s", phase, out)
		historyCheck(t, err)
		for kind, id := range ids {
			var snapshot string
			var archived bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT (to_jsonb(d)-'deleted_at'-'updated_at'-'commercial_revision')::text,deleted_at IS NOT NULL FROM `+kind+`s d WHERE id=$1`, id).Scan(&snapshot, &archived))
			if snapshot != before[kind] || archived != (phase != "allowed") {
				t.Fatalf("%s browser %s changed history or failed to persist archive state", kind, phase)
			}
		}
		historyEqual(t, "restore must not revive mail or regenerate PDF", historyDurableRows(t, pool, []string{"document_deliveries", "document_delivery_events", "document_pdf_versions"}), mailBefore)
		if client.Job.GetX(ctx, j.ID).DeletedAt == nil {
			t.Fatal("browser restore reopened Job")
		}
	}
}
