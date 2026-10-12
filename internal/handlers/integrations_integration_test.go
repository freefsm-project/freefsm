package handlers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

func TestIntegrationSettingsIndependentAuthorityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	authority := authorization.New(pool)
	role, err := authority.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Mail reviewer", []authorization.Grant{{Permission: "configuration.mail.view", Scope: authorization.Configuration}})
	if err != nil {
		t.Fatal(err)
	}
	var user int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,'reviewer@test','Reviewer','x','tech',$2) RETURNING id`, company, role).Scan(&user); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user)
	request := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodGet, "/settings/mail", nil); w.Code != 200 {
		t.Fatalf("mail view: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/settings/maps", "/settings/open-tracking"} {
		if w := request("GET", path, nil); w.Code != 403 {
			t.Fatalf("denied %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/settings/mail", "/settings/test-email"} {
		if w := request("POST", path, nil); w.Code != 403 {
			t.Fatalf("denied %s: %d", path, w.Code)
		}
	}
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "No configuration API")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/settings/mail", "/settings/maps", "/settings/test-email"} {
		r := httptest.NewRequest("POST", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("configuration exposed to field API: %s %d", path, w.Code)
		}
	}
}

func TestGeocodingRequiresReviewedExplicitUpdateHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[{"lat":"40.25","lon":"-73.5"}]`) }))
	defer provider.Close()
	var customer, location int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Mapped customer') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO locations(company_id,object_type,object_id,title,address_1,latitude,longitude) VALUES($1,'customer',$2,'Depot','Old address',10,20) RETURNING id`, company, customer).Scan(&location); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET geocoder_url=$1`, provider.URL); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/locations/%d/coordinates", location)
	request := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	w := request("GET", path, nil)
	if w.Code != 200 {
		t.Fatalf("coordinate review: %d %s", w.Code, w.Body.String())
	}
	rev := regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(w.Body.String())[1]
	w = request("POST", path+"/lookup", url.Values{"revision": {rev}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "40.25") {
		t.Fatalf("lookup: %d %s", w.Code, w.Body.String())
	}
	w = request("GET", path, nil)
	if !strings.Contains(w.Body.String(), `value="10"`) {
		t.Fatal("lookup changed saved coordinates")
	}
	if _, err := pool.Exec(ctx, `UPDATE locations SET address_1='New reviewed address' WHERE id=$1`, location); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"revision": {rev}, "latitude": {"40.25"}, "longitude": {"-73.5"}}
	if w = request("POST", path, form); w.Code != 409 {
		t.Fatalf("stale result accepted: %d", w.Code)
	}
	w = request("GET", path, nil)
	form.Set("revision", regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(w.Body.String())[1])
	if w = request("POST", path, form); w.Code != 303 {
		t.Fatalf("explicit update: %d %s", w.Code, w.Body.String())
	}
	provider.Close()
	w = request("GET", path, nil)
	form.Set("revision", regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(w.Body.String())[1])
	if w = request("POST", path+"/lookup", form); w.Code != 503 {
		t.Fatalf("provider outage: %d", w.Code)
	}
	w = request("GET", path, nil)
	if !strings.Contains(w.Body.String(), `value="40.25"`) {
		t.Fatal("provider failure lost coordinates")
	}
}

func TestSavedTransportExplicitRecipientHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan string, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		p := textproto.NewConn(conn)
		_ = p.PrintfLine("220 test smtp")
		var recipient string
		for {
			line, e := p.ReadLine()
			if e != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "RCPT TO:"):
				recipient = line
				_ = p.PrintfLine("250 ok")
			case line == "DATA":
				_ = p.PrintfLine("354 send")
				data, _ := io.ReadAll(p.DotReader())
				received <- recipient + "\n" + string(data)
				_ = p.PrintfLine("250 accepted")
			case line == "QUIT":
				_ = p.PrintfLine("221 bye")
				return
			default:
				_ = p.PrintfLine("250 ok")
			}
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := pool.Exec(context.Background(), `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='saved@example.com'`, port); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	post := func(form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/settings/test-email", strings.NewReader(form.Encode()))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := post(nil); w.Code != 400 {
		t.Fatalf("recipient required: %d", w.Code)
	}
	w := post(url.Values{"recipient": {"explicit@example.com"}, "smtp_host": {"ignored.invalid"}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Inbox delivery is not confirmed") {
		t.Fatalf("truthful acceptance: %d %s", w.Code, w.Body.String())
	}
	select {
	case msg := <-received:
		if !strings.Contains(msg, "RCPT TO:<explicit@example.com>") || !strings.Contains(msg, "From: saved@example.com") {
			t.Fatal(fmt.Sprintf("wrong saved transport envelope: %s", msg))
		}
	case <-time.After(time.Second):
		t.Fatal("no SMTP submission")
	}
}

func TestMailSettingsSecretsAndAtomicFailuresHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	request := func(method string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/settings/mail", strings.NewReader(form.Encode()))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	revision := func() string {
		t.Helper()
		w := request("GET", nil)
		m := regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
		if len(m) != 2 {
			t.Fatalf("missing revision: %d %s", w.Code, w.Body.String())
		}
		return m[1]
	}
	form := url.Values{"revision": {revision()}, "smtp_host": {"localhost"}, "smtp_port": {"2525"}, "smtp_from": {"sender@example.com"}, "smtp_password": {"canary-secret-26"}}
	form.Set("invoice_email_html", "<p>Reviewed HTML template</p>")
	t.Setenv("FREEFSM_CREDENTIAL_KEYS", "")
	if w := request("POST", form); w.Code != 503 {
		t.Fatalf("missing key must reject save: %d %s", w.Code, w.Body.String())
	}
	t.Setenv("FREEFSM_CREDENTIAL_KEYS", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if w := request("POST", form); w.Code != 303 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	body := request("GET", nil).Body.String()
	if !strings.Contains(body, "Reviewed HTML template") {
		t.Fatal("HTML template not preserved")
	}
	if strings.Contains(body, "canary-secret") {
		t.Fatal("secret disclosed")
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT smtp_password FROM company_settings LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "" || strings.Contains(stored, "canary-secret") {
		t.Fatal("credential not encrypted at rest")
	}
	if w := request("POST", form); w.Code != 409 {
		t.Fatalf("stale save: %d", w.Code)
	}
	t.Setenv("FREEFSM_CREDENTIAL_KEYS", "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	form.Set("revision", revision())
	form.Set("smtp_password", "")
	if w := request("POST", form); w.Code != 303 {
		t.Fatalf("lossless key rotation: %d %s", w.Code, w.Body.String())
	}
	t.Setenv("FREEFSM_CREDENTIAL_KEYS", "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if strings.Contains(request("GET", nil).Body.String(), "key unavailable") {
		t.Fatal("saved secret still requires retired live key")
	}
	form.Set("revision", revision())
	form.Set("smtp_password", "")
	form.Set("smtp_host", "replacement.example")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_integration_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_integration_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_integration_audit()`); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", form); w.Code != 409 {
		t.Fatalf("audit failure: %d", w.Code)
	}
	if body := request("GET", nil).Body.String(); strings.Contains(body, "replacement.example") || !strings.Contains(body, "localhost") {
		t.Fatal("failed save changed configuration")
	}
}

func TestLegacyProviderPreflightIsReadOnlyAndSecretFreeHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_password='legacy-secret-canary',geocoder_url='https://user:map-secret-canary@example.com/search?key=map-secret-canary'`); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, section := range []string{"mail", "maps"} {
		body := requestBody(t, router, cookie, "GET", "/settings/"+section, 200)
		if !strings.Contains(body, "explicit review") || strings.Contains(body, "secret-canary") {
			t.Fatalf("unsafe preflight %s: %s", section, body)
		}
	}
	var unchanged bool
	if err := pool.QueryRow(ctx, `SELECT smtp_password='legacy-secret-canary' AND geocoder_url LIKE '%map-secret-canary%' FROM company_settings LIMIT 1`).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("preflight mutated legacy settings: %v", err)
	}
}

func TestIntegrationSaveRechecksWaitingAuthorityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Delegated maps", []authorization.Grant{{Permission: "configuration.maps.view", Scope: authorization.Configuration}, {Permission: "configuration.maps.manage", Scope: authorization.Configuration}})
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("maps-race@test").SetName("Maps manager").SetRole("tech").SetPasswordHash("x").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	body := requestBody(t, router, cookie, "GET", "/settings/maps", 200)
	revision := regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(body)[1]
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		form := url.Values{"revision": {revision}, "map_tile_url": {"https://unexpected.example/{z}/{x}/{y}"}}
		r := httptest.NewRequest("POST", "/settings/maps", strings.NewReader(form.Encode()))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=190019 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("save did not wait for authority mutex")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := blocker.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='configuration.maps.manage'`, role); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != 403 {
			t.Fatalf("revoked waiting save: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("save did not finish")
	}
	if strings.Contains(requestBody(t, router, cookie, "GET", "/settings/maps", 200), "unexpected.example") {
		t.Fatal("revoked save committed")
	}
}

func TestRestoreMailGateRequiresAuditedAdministratorEnableHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	control, err := instancecontrol.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.DisableEmail(); err != nil {
		t.Fatal(err)
	}
	control, err = instancecontrol.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	router := New(pool, client, sessions, &config.Config{UploadDir: t.TempDir()}, control)
	cookie := sessionCookie(t, ctx, sessions, admin)
	post := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader("recipient=explicit%40example.com"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := post("/settings/test-email"); w.Code != 503 {
		t.Fatalf("restart lost gate: %d", w.Code)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_enable_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_enable_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_enable_audit()`); err != nil {
		t.Fatal(err)
	}
	w := post("/settings/enable-email")
	if w.Code == 303 || !control.EmailDisabled() {
		t.Fatal("audit failure bypassed restore gate")
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_enable_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	if w = post("/settings/enable-email"); w.Code != 303 || control.EmailDisabled() {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
}

func TestConcurrentIntegrationSavesHaveOneReviewedWinnerHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	body := requestBody(t, router, cookie, "GET", "/settings/maps", 200)
	revision := regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(body)[1]
	start := make(chan struct{})
	results := make(chan int, 2)
	for _, host := range []string{"first", "second"} {
		go func(host string) {
			<-start
			form := url.Values{"revision": {revision}, "geocoder_url": {"https://" + host + ".example"}}
			r := httptest.NewRequest("POST", "/settings/maps", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			results <- w.Code
		}(host)
	}
	close(start)
	counts := map[int]int{}
	for i := 0; i < 2; i++ {
		select {
		case code := <-results:
			counts[code]++
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent saves did not complete")
		}
	}
	if counts[303] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent save outcomes: %v", counts)
	}
}

func TestMapSaveRejectsAmbiguousURLWithoutReplacingProviderHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET geocoder_url='https://usable.example'`); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	body := requestBody(t, router, cookie, "GET", "/settings/maps", 200)
	rev := regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(body)[1]
	form := url.Values{"revision": {rev}, "geocoder_url": {"https://replacement.example/#embedded-token"}}
	r := httptest.NewRequest("POST", "/settings/maps", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("ambiguous URL was accepted: %d", w.Code)
	}
	if !strings.Contains(requestBody(t, router, cookie, "GET", "/settings/maps", 200), "https://usable.example") {
		t.Fatal("failed save replaced usable provider")
	}
}
