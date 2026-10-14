package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

type associationRepairFixture struct {
	pool           *pgxpool.Pool
	client         *ent.Client
	router         http.Handler
	sessions       *services.SessionService
	company, admin int64
	cookie         *http.Cookie
	today          string
}

func associationRepair(t *testing.T) *associationRepairFixture {
	t.Helper()
	p, c, r, s, company, admin := permissionHTTPFixture(t)
	f := &associationRepairFixture{pool: p, client: c, router: r, sessions: s, company: company, admin: admin, cookie: sessionCookie(t, context.Background(), s, admin)}
	if root := os.Getenv("ASSET_ASSOCIATION_EVIDENCE"); root != "" {
		t.Cleanup(func() {
			var schema string
			if err := p.QueryRow(context.Background(), `SELECT current_schema()`).Scan(&schema); err != nil {
				t.Error(err)
				return
			}
			output, err := exec.Command("pg_dump", "--dbname", os.Getenv("FREEFSM_TEST_DATABASE_URL"), "--schema", schema, "--no-owner", "--no-acl", "--file", filepath.Join(root, t.Name()+".sql")).CombinedOutput()
			if err != nil {
				t.Errorf("retain repair fixture: %v %s", err, output)
			}
		})
	}
	if err := p.QueryRow(context.Background(), `SELECT (clock_timestamp() AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=$1),'UTC'))::date::text`, company).Scan(&f.today); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *associationRepairFixture) asset(t *testing.T, customer int64, number string) *ent.Asset {
	t.Helper()
	ctx := context.Background()
	typ := f.client.AssetType.Create().SetCompanyID(f.company).SetName(number).SaveX(ctx)
	return f.client.Asset.Create().SetCompanyID(f.company).SetCustomerID(customer).SetOwnershipClass("customer_owned").SetNumber(number).SetName(number + " equipment").SetAssetTypeID(typ.ID).SetManufacturer("OLD-MAKER").SetModel("OLD-MODEL").SetSerialNumber("OLD-SERIAL").SaveX(ctx)
}

func (f *associationRepairFixture) post(t *testing.T, cookie *http.Cookie, path string, v url.Values, want int) *httptest.ResponseRecorder {
	t.Helper()
	v = currentJobFormValues(t, f.router, cookie, path, v)
	r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("POST %s: %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	return w
}

func (f *associationRepairFixture) reader(t *testing.T, grants []authorization.Grant) (int64, int64, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	auth := authorization.New(f.pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: f.admin}, 0, "Independent association reader", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := f.client.User.Create().SetCompanyID(f.company).SetEmail("association-repair@test").SetName("Independent reader").SetPasswordHash("fixture").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: f.admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	return u.ID, role, sessionCookie(t, ctx, f.sessions, u.ID)
}

func (f *associationRepairFixture) pdf(t *testing.T, cookie *http.Cookie, path, evidence string) string {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Code != 200 || !bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")) {
		t.Fatalf("actual PDF: %d %s", w.Code, w.Body.String())
	}
	cmd := exec.Command("pdftotext", "-", "-")
	cmd.Stdin = bytes.NewReader(w.Body.Bytes())
	text, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if root := os.Getenv("ASSET_ASSOCIATION_EVIDENCE"); root != "" {
		for name, data := range map[string][]byte{evidence + ".pdf": w.Body.Bytes(), evidence + ".txt": text} {
			if err = os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return string(text)
}

func TestAssetAssociationRepairPDFReviewedContext(t *testing.T) {
	f := associationRepair(t)
	ctx := context.Background()
	old := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Old PDF customer").SaveX(ctx)
	next := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Private new customer").SaveX(ctx)
	a := f.asset(t, old.ID, "PDF-REPAIR")
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(old.ID).SetAssetID(a.ID).SetJobType("Old work").SaveX(ctx)
	w := f.post(t, f.cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(old.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Retained invoice"}, "notes": {"OLD-DOCUMENT-CONTROL"}, "invoice_date": {f.today}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	path := u.Path
	f.post(t, f.cookie, path+"/pdf/save", url.Values{}, 303)
	before := f.pdf(t, f.cookie, path+"/pdf", "repair-before-transfer")
	var originalVersion int64
	if err := f.pool.QueryRow(ctx, `SELECT max(id) FROM document_pdf_versions WHERE document_type='invoice'`).Scan(&originalVersion); err != nil {
		t.Fatal(err)
	}
	assertContains(t, before, "OLD-MODEL")
	assertContains(t, before, "OLD-SERIAL")
	uid, _, limited := f.reader(t, []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "invoice.view", Scope: authorization.Assigned}, {Permission: "pricing.view", Scope: authorization.Assigned}, {Permission: "invoice.pdf.download", Scope: authorization.Assigned}})
	f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	f.post(t, f.cookie, fmt.Sprintf("/assets/%d/associations/transfer", a.ID), url.Values{"customer_id": {fmt.Sprint(next.ID)}, "effective_date": {f.today}, "reason": {"Actual sale"}, "revision": {"0"}}, 303)
	f.post(t, f.cookie, fmt.Sprintf("/assets/%d", a.ID), url.Values{"name": {"PRIVATE-NEW-NAME"}, "manufacturer": {"PRIVATE-NEW-MAKER"}, "model": {"PRIVATE-NEW-MODEL"}, "serial_number": {"PRIVATE-NEW-SERIAL"}}, 303)
	assertNotContains(t, requestBody(t, f.router, f.cookie, "GET", path, 200), "PRIVATE-NEW-")
	f.post(t, f.cookie, path+"/pdf/save", url.Values{}, 303)
	after := f.pdf(t, limited, path+"/pdf", "repair-limited-after-transfer")
	assertContains(t, after, "OLD-DOCUMENT-CONTROL")
	assertNotContains(t, after, "PRIVATE-NEW-")
	retained := f.pdf(t, limited, fmt.Sprintf("%s/pdf?version=%d&confirm_stale=1", path, originalVersion), "repair-retained-original")
	if retained != before {
		t.Fatal("retained old PDF changed after transfer or regeneration")
	}
}

func TestAssetAssociationRepairIndependentJobProjection(t *testing.T) {
	f := associationRepair(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Owner").SaveX(ctx)
	a := f.asset(t, c.ID, "PRIVATE-ASSET-LABEL")
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetJobType("Visible work").SaveX(ctx)
	f.post(t, f.cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"link"}, "asset_id": {fmt.Sprint(a.ID)}}, 303)
	uid, role, cookie := f.reader(t, []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}})
	f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	assertNotContains(t, requestBody(t, f.router, cookie, "GET", path, 200), a.Number)
	auth := authorization.New(f.pool)
	for _, scope := range []authorization.Scope{authorization.Context, authorization.All} {
		var revision int64
		if err := f.pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		_, err := auth.SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "Independent association reader", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: scope}}, revision)
		if err != nil {
			t.Fatal(err)
		}
		assertContains(t, requestBody(t, f.router, cookie, "GET", path, 200), a.Number)
	}
	var revision int64
	if err := f.pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	_, err := auth.SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "Independent association reader", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}}, revision)
	if err != nil {
		t.Fatal(err)
	}
	assertNotContains(t, requestBody(t, f.router, cookie, "GET", path, 200), a.Number)
}

func TestAssetAssociationRepairHistoryReferences(t *testing.T) {
	f := associationRepair(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("PRIVATE-CUSTOMER").SaveX(ctx)
	l := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("PRIVATE-LOCATION").SaveX(ctx)
	a := f.asset(t, c.ID, "HISTORY-CONTROL")
	// The initial observation records this Location through a real placement correction in later tests;
	// this fixture creates a second Asset with its Location present at observation.
	a = f.client.Asset.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetLocationID(l.ID).SetOwnershipClass("customer_owned").SetAssetTypeID(a.AssetTypeID).SetName("History equipment").SaveX(ctx)
	_, role, cookie := f.reader(t, []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}})
	path := fmt.Sprintf("/assets/%d/associations", a.ID)
	body := requestBody(t, f.router, cookie, "GET", path, 200)
	assertNotContains(t, body, c.DisplayName)
	assertNotContains(t, body, l.Title)
	for _, grants := range [][]authorization.Grant{
		{{Permission: "asset.view", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}},
		{{Permission: "asset.view", Scope: authorization.All}, {Permission: "location.view", Scope: authorization.All}},
	} {
		var revision int64
		if err := f.pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if _, err := authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "Independent association reader", grants, revision); err != nil {
			t.Fatal(err)
		}
		body = requestBody(t, f.router, cookie, "GET", path, 200)
		if grants[1].Permission == "customer.view" {
			assertContains(t, body, c.DisplayName)
			assertNotContains(t, body, l.Title)
		} else {
			assertNotContains(t, body, c.DisplayName)
			assertContains(t, body, l.Title)
		}
	}
	// Archived targets retain only bounded label + badge, never navigation or
	// unrestricted details through the association source.
	f.post(t, f.cookie, fmt.Sprintf("/customers/%d/locations/%d/delete", c.ID, l.ID), url.Values{}, 303)
	body = requestBody(t, f.router, cookie, "GET", path, 200)
	assertContains(t, body, "Archived")
	assertContains(t, body, l.Title)
	assertNotContains(t, body, fmt.Sprintf("href=\"/locations/%d", l.ID))
}

func TestAssetAssociationRepairLinkEditorsCompose(t *testing.T) {
	f := associationRepair(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Link owner").SaveX(ctx)
	a := f.asset(t, c.ID, "LINK-A")
	b := f.asset(t, c.ID, "LINK-B")
	for _, explicitFirst := range []bool{true, false} {
		j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetJobType("Composed links").SaveX(ctx)
		explicit := fmt.Sprintf("/jobs/%d/assets", j.ID)
		single := fmt.Sprintf("/jobs/%d/links", j.ID)
		link := func(a *ent.Asset) {
			f.post(t, f.cookie, explicit, url.Values{"action": {"link"}, "asset_id": {fmt.Sprint(a.ID)}}, 303)
		}
		selectAsset := func(a *ent.Asset) {
			f.post(t, f.cookie, single, url.Values{"asset_id": {fmt.Sprint(a.ID)}, "location_id": {"0"}, "project_id": {"0"}}, 303)
		}
		if explicitFirst {
			link(a)
		} else {
			selectAsset(a)
		}
		var before string
		if err := f.pool.QueryRow(ctx, `SELECT to_jsonb(l)::text FROM job_asset_links l WHERE job_id=$1 AND asset_id=$2`, j.ID, a.ID).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if explicitFirst {
			selectAsset(a)
		} else {
			link(a)
		}
		var after string
		if err := f.pool.QueryRow(ctx, `SELECT to_jsonb(l)::text FROM job_asset_links l WHERE job_id=$1 AND asset_id=$2`, j.ID, a.ID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("same link selection rewrote immutable identity: %s -> %s", before, after)
		}
		link(b)
		selectAsset(b)
		selectAsset(a)
		body := requestBody(t, f.router, f.cookie, "GET", fmt.Sprintf("/jobs/%d", j.ID), 200)
		assertContains(t, body, a.Number)
		assertContains(t, body, b.Number)
		var links int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM job_asset_links WHERE job_id=$1 AND operational AND unlinked_at IS NULL`, j.ID).Scan(&links); err != nil {
			t.Fatal(err)
		}
		if links != 2 {
			t.Fatalf("switching existing selections replaced a canonical link: %d", links)
		}
	}
}

func TestAssetAssociationRepairOriginCorrection(t *testing.T) {
	f := associationRepair(t)
	ctx := context.Background()
	wrong := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Mistaken initial owner").SaveX(ctx)
	right := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Actual initial owner").SaveX(ctx)
	a := f.asset(t, wrong.ID, "ORIGIN-IDENTITY")
	_, _, cookie := f.reader(t, []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}, {Permission: "asset.history.correct", Scope: authorization.All}})
	path := fmt.Sprintf("/assets/%d/associations", a.ID)
	var before string
	if err := f.pool.QueryRow(ctx, `SELECT to_jsonb(o)::text FROM asset_association_origins o WHERE asset_id=$1`, a.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, f.router, cookie, "GET", path, 200)
	assertContains(t, body, "Initial observed association")
	v := url.Values{"event_id": {"0"}, "revision": {"0"}, "customer_id": {fmt.Sprint(right.ID)}, "effective_date": {f.today}, "reason": {"Wrong original entry; no actual transfer"}}
	f.post(t, cookie, path+"/correct", v, 303)
	f.post(t, cookie, path+"/correct", v, 409)
	current := f.client.Asset.GetX(ctx, a.ID)
	if current.CustomerID != right.ID || current.Number != a.Number || current.OwnershipClass != a.OwnershipClass {
		t.Fatal("origin correction did not preserve identity and project the factual owner")
	}
	var after string
	var unknown bool
	var moves, audits int
	if err := f.pool.QueryRow(ctx, `SELECT to_jsonb(o)::text FROM asset_association_origins o WHERE asset_id=$1`, a.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("original observation was rewritten")
	}
	if err := f.pool.QueryRow(ctx, `SELECT starts_on IS NULL AND customer_id=$2 FROM asset_association_periods WHERE asset_id=$1 AND event_id=0`, a.ID, right.ID).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if !unknown {
		t.Fatal("correction invented a historical acquisition date")
	}
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM asset_association_events WHERE asset_id=$1 AND action<>'correct'),(SELECT count(*) FROM authorization_audit WHERE target_type='asset' AND target_id=$1 AND action='asset.history.correct')`, a.ID).Scan(&moves, &audits); err != nil {
		t.Fatal(err)
	}
	if moves != 0 || audits != 1 {
		t.Fatalf("invented moves or non-atomic audit: moves %d audits %d", moves, audits)
	}
	body = requestBody(t, f.router, cookie, "GET", path, 200)
	assertContains(t, body, v.Get("reason"))
	assertContains(t, body, "Unknown — observed association")
}

func TestAssetAssociationRepairOriginAtomicValidation(t *testing.T) {
	f := associationRepair(t)
	ctx := context.Background()
	c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Original").SaveX(ctx)
	n := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Corrected").SaveX(ctx)
	a := f.asset(t, c.ID, "ORIGIN-REPAIR")
	j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Retained Job").SaveX(ctx)
	path := fmt.Sprintf("/assets/%d/associations", a.ID)
	v := url.Values{"event_id": {"0"}, "revision": {"0"}, "customer_id": {fmt.Sprint(n.ID)}, "effective_date": {f.today}, "reason": {"Reasoned origin correction"}}
	// A correction cannot silently retarget the original Job evidence.
	f.post(t, f.cookie, path+"/correct", v, 409)
	assertNotContains(t, requestBody(t, f.router, f.cookie, "GET", path, 200), v.Get("reason"))
	var link int64
	if err := f.pool.QueryRow(ctx, `SELECT id FROM job_asset_links WHERE job_id=$1`, j.ID).Scan(&link); err != nil {
		t.Fatal(err)
	}
	f.post(t, f.cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"unlink"}, "link_id": {fmt.Sprint(link)}}, 303)
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_origin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='asset.history.correct' THEN RAISE EXCEPTION 'injected origin audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_origin_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_origin_audit()`); err != nil {
		t.Fatal(err)
	}
	f.post(t, f.cookie, path+"/correct", v, 500)
	assertNotContains(t, requestBody(t, f.router, f.cookie, "GET", path, 200), v.Get("reason"))
	if f.client.Asset.GetX(ctx, a.ID).CustomerID != c.ID {
		t.Fatal("audit failure committed origin projection")
	}
	if _, err := f.pool.Exec(ctx, `DROP TRIGGER reject_origin_audit ON authorization_audit; DROP FUNCTION reject_origin_audit()`); err != nil {
		t.Fatal(err)
	}
	// Two corrections of the same reviewed observation have one winner.
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", path+"/correct", strings.NewReader(v.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(f.cookie)
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	wg.Wait()
	x, y := <-codes, <-codes
	if !(x == 303 && y == 409 || x == 409 && y == 303) {
		t.Fatalf("origin correction race %d/%d", x, y)
	}
	// Revising the origin must also revalidate later actual moves.
	b := f.asset(t, c.ID, "ORIGIN-OVERLAP")
	root := fmt.Sprintf("/assets/%d/associations", b.ID)
	f.post(t, f.cookie, root+"/transfer", url.Values{"customer_id": {fmt.Sprint(n.ID)}, "revision": {"0"}, "effective_date": {f.today}, "reason": {"Real later move"}}, 303)
	v.Set("revision", "1")
	f.post(t, f.cookie, root+"/correct", v, 409) // would make the later transfer n → n
	assertNotContains(t, requestBody(t, f.router, f.cookie, "GET", root, 200), v.Get("reason"))
}
