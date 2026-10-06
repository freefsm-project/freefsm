package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/justinas/nosurf"
	"golang.org/x/crypto/bcrypt"
)

// This test uses only uniquely named disposable databases/NO-CREATEDB roles in
// the explicitly supplied disposable cluster. It exercises real sessions, CSRF,
// bcrypt, migrations, native PostgreSQL tools and the handler/engine boundary.
func TestBackupHTTPRoundTrip(t *testing.T) {
	for _, scenario := range []string{"development", "development-different-identity", "development-schema-mismatch", "release-mismatch", "development-to-release", "release-to-development"} {
		t.Run(scenario, func(t *testing.T) { backupHTTPRoundTrip(t, scenario) })
	}
}

func backupHTTPRoundTrip(t *testing.T, scenario string) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL disposable cluster required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	type fixture struct {
		router            http.Handler
		manager           *backup.Manager
		control           *instancecontrol.Control
		client            *ent.Client
		pool              *pgxpool.Pool
		sessions          *services.SessionService
		web, mobile, csrf string
		cookie            *http.Cookie
		uploads           string
	}
	var instances []*fixture
	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("backup_http_%d_%d", time.Now().UnixNano(), i)
		ident := pgx.Identifier{name}.Sanitize()
		if _, err := admin.Exec(ctx, "CREATE ROLE "+ident+" LOGIN NOCREATEDB NOSUPERUSER"); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident+" OWNER "+ident+" TEMPLATE template0 ENCODING 'UTF8'"); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = admin.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)")
			_, _ = admin.Exec(ctx, "DROP ROLE "+ident)
		}()
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		u.User, u.Path = url.User(name), "/"+name
		root := t.TempDir()
		f := &fixture{uploads: filepath.Join(root, "uploads")}
		f.control, err = instancecontrol.New(filepath.Join(root, "state"))
		if err != nil {
			t.Fatal(err)
		}
		db, err := database.Connect(ctx, u.String())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		f.pool = db.Pool
		kind, version, commit := backup.BuildDevelopment, config.Version, config.Commit
		if scenario == "release-mismatch" || (scenario == "development-to-release" && i == 1) || (scenario == "release-to-development" && i == 0) {
			kind, version, commit = backup.BuildRelease, fmt.Sprintf("v1.2.%d", i), "abcdef1234567"
		}
		if scenario == "development-different-identity" && i == 1 {
			version, commit = "v1.2.3-42-gabcdef-dirty", "abcdef1234567"
		}
		f.manager, err = backup.New(backup.Config{DSN: u.String(), UploadDir: f.uploads, StateDir: f.control.StateDir(), BuildKind: kind, Version: version, Commit: commit, Control: f.control, ResetConnections: db.Pool.Reset})
		if err != nil {
			t.Fatal(err)
		}
		defer f.manager.Shutdown()
		if err := f.manager.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(f.uploads, 0700); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx, os.DirFS("../database/migrations")); err != nil {
			t.Fatal(err)
		}
		pgcfg, err := pgx.ParseConfig(u.String())
		if err != nil {
			t.Fatal(err)
		}
		pgcfg.DefaultQueryExecMode = pgx.QueryExecModeExec
		pgcfg.StatementCacheCapacity, pgcfg.DescriptionCacheCapacity = 0, 0
		f.client = ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, stdlib.OpenDB(*pgcfg))))
		defer f.client.Close()
		var company int64
		if err := db.Pool.QueryRow(ctx, "INSERT INTO companies(name,slug) VALUES($1,$2) RETURNING id", name, name).Scan(&company); err != nil {
			t.Fatal(err)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(fmt.Sprintf("account-%d", i)), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		actor := f.client.User.Create().SetCompanyID(company).SetName(name).SetEmail(name + "@example.test").SetRole("admin").SetIsActive(true).SetPasswordHash(string(hash)).SaveX(ctx)
		logo := filepath.Join(f.uploads, "logo.png")
		if err := os.WriteFile(logo, []byte(fmt.Sprintf("source-bytes-%d", i)), 0600); err != nil {
			t.Fatal(err)
		}
		settings := f.client.CompanySettings.Query().FirstX(ctx)
		f.client.CompanySettings.UpdateOne(settings).SetCompanyID(company).SetBusinessName(fmt.Sprintf("Instance %d", i)).SetSMTPPassword(fmt.Sprintf("smtp-%d", i)).SetInvoiceLogoPath(logo).SaveX(ctx)
		f.sessions = services.NewSessionService(db.Pool)
		f.web, err = f.sessions.Create(ctx, actor.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.mobile, err = f.sessions.CreateMobile(ctx, actor.ID, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		f.router = NewBackupRouter(f.manager, f.control, services.NewUserService(f.client), services.NewCompanySettingsService(f.client), f.sessions, "", backupTestActivityHandler(f.client))
		r := httptest.NewRequest("GET", "/settings/backup", nil)
		r.AddCookie(&http.Cookie{Name: "session", Value: f.web})
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("page %d: %s", w.Code, w.Body)
		}
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == "csrf_token" {
				f.cookie = cookie
			}
		}
		match := regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(w.Body.String())
		if len(match) != 2 || f.cookie == nil {
			t.Fatal("missing CSRF page token")
		}
		f.csrf = html.UnescapeString(match[1])
		instances = append(instances, f)
	}
	request := func(f *fixture, action string, body io.Reader, raw bool, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/settings/backup/"+action, body)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.AddCookie(f.cookie)
		r.Header.Set("X-CSRF-Token", f.csrf)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "session", Value: f.web})
		}
		if raw {
			r.Header.Set("Content-Type", "application/octet-stream")
			r.Header.Set("X-Archive-Password", `"archive-secret"`)
		} else {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) backup.Operation {
		if w.Code != 200 {
			t.Fatalf("operation: %d %s", w.Code, w.Body)
		}
		var op backup.Operation
		if err := json.Unmarshal(w.Body.Bytes(), &op); err != nil {
			t.Fatal(err)
		}
		return op
	}
	poll := func(f *fixture, op backup.Operation) backup.Operation {
		deadline := time.Now().Add(60 * time.Second)
		for op.Phase != "ready" && op.Phase != "complete" && op.Phase != "failed" {
			if time.Now().After(deadline) {
				t.Fatalf("timeout: %+v", op)
			}
			time.Sleep(20 * time.Millisecond)
			r := httptest.NewRequest("POST", "/settings/backup/status", nil)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.AddCookie(f.cookie)
			r.Header.Set("X-CSRF-Token", f.csrf)
			r.Header.Set("X-Backup-Operation", op.ID)
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, r)
			op = decode(w)
		}
		if op.Phase == "failed" {
			if scenario != "development-schema-mismatch" && scenario != "release-mismatch" {
				t.Fatalf("operation failed: %+v", op)
			}
		}
		return op
	}
	src, dst := instances[0], instances[1]
	// Every mutation, including raw upload, must reject absent CSRF before DB work.
	for _, action := range []string{"create", "restore", "download", "upload"} {
		r := httptest.NewRequest("POST", "/settings/backup/"+action, strings.NewReader("bad"))
		r.AddCookie(&http.Cookie{Name: "session", Value: src.web})
		w := httptest.NewRecorder()
		src.router.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("missing CSRF %s: %d", action, w.Code)
		}
	}
	op := poll(src, decode(request(src, "create", strings.NewReader("archive_password=archive-secret"), false, true)))
	values := url.Values{"operation": {op.ID}, "current_password": {"wrong"}}
	if w := request(src, "download", strings.NewReader(values.Encode()), false, true); w.Code != 403 {
		t.Fatalf("wrong password: %d", w.Code)
	}
	values.Set("current_password", "account-0")
	archive := request(src, "download", strings.NewReader(values.Encode()), false, true)
	if archive.Code != 200 || !strings.Contains(archive.Header().Get("Content-Disposition"), ".age") {
		t.Fatalf("download %d", archive.Code)
	}
	if scenario == "development-schema-mismatch" {
		if _, err := dst.pool.Exec(ctx, "ALTER TABLE companies ADD COLUMN development_only text"); err != nil {
			t.Fatal(err)
		}
	}
	op = poll(dst, decode(request(dst, "upload", strings.NewReader(archive.Body.String()), true, true)))
	if scenario == "development-schema-mismatch" || scenario == "release-mismatch" {
		want := backup.FailureSchemaMismatch
		if scenario == "release-mismatch" {
			want = backup.FailureReleaseMismatch
		}
		if op.Phase != "failed" || op.Failure.Category != want {
			t.Fatalf("expected %s: %+v", want, op)
		}
		if scenario == "development-schema-mismatch" && op.Failure.Message != "The archive schema differs from the trusted destination schema. Verify release and migration state before retrying." {
			t.Fatalf("genuine schema mismatch message changed: %q", op.Failure.Message)
		}
		settings := dst.client.CompanySettings.Query().OnlyX(ctx)
		content, err := os.ReadFile(filepath.Join(dst.uploads, "logo.png"))
		if settings.BusinessName != "Instance 1" || err != nil || string(content) != "source-bytes-1" {
			t.Fatal("rejected archive modified destination")
		}
		if _, err := dst.sessions.Validate(ctx, dst.web); err != nil {
			t.Fatal("rejected archive invalidated session", err)
		}
		return
	}
	wantKind := backup.BuildDevelopment
	if scenario == "release-to-development" {
		wantKind = backup.BuildRelease
	}
	if op.SourceName != "Instance 0" || op.ArchiveDigest == "" || op.BuildKind != wantKind {
		t.Fatalf("review %+v", op)
	}
	values = url.Values{"operation": {op.ID}, "archive_digest": {op.ArchiveDigest}, "current_password": {"account-1"}, "confirmation": {"Instance 0"}}
	if w := request(dst, "restore", strings.NewReader(values.Encode()), false, true); w.Code != 400 {
		t.Fatalf("destination mismatch %d", w.Code)
	}
	values.Set("confirmation", "Instance 1")
	op = poll(dst, decode(request(dst, "restore", strings.NewReader(values.Encode()), false, true)))
	if op.Phase != "complete" || !dst.control.EmailDisabled() {
		t.Fatal("restore protections missing")
	}
	if err := dst.manager.RefreshConnections(); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{src.web, dst.web} {
		if _, err := dst.sessions.Validate(ctx, token); err == nil {
			t.Fatal("web session survived")
		}
	}
	for _, token := range []string{src.mobile, dst.mobile} {
		if _, err := dst.sessions.ValidateMobile(ctx, token); err == nil {
			t.Fatal("mobile session survived")
		}
	}
	settings, err := services.NewCompanySettingsService(dst.client).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.BusinessName != "Instance 0" || settings.SMTPPassword != "smtp-0" || settings.InvoiceLogoPath != filepath.Join(dst.uploads, "logo.png") {
		t.Fatalf("restored settings: %+v", settings)
	}
	content, err := os.ReadFile(settings.InvoiceLogoPath)
	if err != nil || string(content) != "source-bytes-0" {
		t.Fatalf("file: %q %v", content, err)
	}
	// A capability still cannot mutate after the old session has gone.
	if w := request(dst, "restore", strings.NewReader(values.Encode()), false, false); w.Code != 401 {
		t.Fatalf("sessionless mutation: %d", w.Code)
	}
	// Normal login and the dedicated gate action use the restored account.
	reopened, err := instancecontrol.New(dst.control.StateDir())
	if err != nil || !reopened.EmailDisabled() {
		t.Fatalf("durable gate: %v", err)
	}
	web := New(dst.pool, dst.client, dst.sessions, &config.Config{UploadDir: dst.uploads}, reopened)
	csrf := nosurf.New(middleware.CSRFToken(web))
	csrf.SetIsTLSFunc(func(*http.Request) bool { return false })
	web = reopened.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := dst.manager.RefreshConnections(); err != nil {
			t.Fatal(err)
		}
		csrf.ServeHTTP(w, r)
	}))
	user := dst.client.User.Query().OnlyX(ctx)
	login := url.Values{"email": {user.Email}, "password": {"account-0"}}
	post := func(path string, form url.Values, session *http.Cookie, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("X-CSRF-Token", token)
		r.AddCookie(dst.cookie)
		if session != nil {
			r.AddCookie(session)
		}
		w := httptest.NewRecorder()
		web.ServeHTTP(w, r)
		return w
	}
	w := post("/login", login, nil, dst.csrf)
	if w.Code != 303 {
		t.Fatalf("restored login: %d %s", w.Code, w.Body)
	}
	var session *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "session" {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("restored login missing session")
	}
	r := httptest.NewRequest("GET", "/settings", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	web.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Outbound email is disabled.") {
		t.Fatalf("persistent notice: %d", w.Code)
	}
	if w := post("/settings/test-email", nil, session, dst.csrf); w.Code != 503 {
		t.Fatalf("disabled test-email: %d", w.Code)
	}
	if w := post("/settings/enable-email", nil, session, ""); w.Code != 400 || !reopened.EmailDisabled() {
		t.Fatal("enable lacked CSRF protection")
	}
	if w := post("/settings/enable-email", nil, session, dst.csrf); w.Code != 303 || reopened.EmailDisabled() {
		t.Fatalf("explicit enable: %d", w.Code)
	}
}
