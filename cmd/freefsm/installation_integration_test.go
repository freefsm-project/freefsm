package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// These are executable-boundary tests, with real isolated databases and native
// tools. They do not install a service or claim reboot/release acceptance.
func installationFixture(t *testing.T) (string, []string, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to a disposable cluster")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(ctx) })
	name := fmt.Sprintf("installation_%d", time.Now().UnixNano())
	ident := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE ROLE "+ident+" LOGIN NOSUPERUSER NOCREATEDB"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(ctx, "DROP ROLE "+ident) })
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+ident+" OWNER "+ident+" TEMPLATE template0 ENCODING 'UTF8'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)") })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User, u.Path = url.User(name), "/"+name
	db, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(ctx) })
	root := t.TempDir()
	binary := filepath.Join(root, "freefsm")
	if supplied := os.Getenv("FREEFSM_INSTALLATION_BINARY"); supplied != "" {
		binary = supplied
	} else {
		cmd := exec.Command("go", "build", "-o", binary, ".")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, output)
		}
	}
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "FREEFSM_") && !strings.HasPrefix(value, "PG") {
			env = append(env, value)
		}
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	env = append(env, "FREEFSM_DB_HOST="+u.Hostname(), "FREEFSM_DB_PORT="+port,
		"FREEFSM_DB_NAME="+name, "FREEFSM_DB_USER="+name, "FREEFSM_DB_PASSWORD=",
		"FREEFSM_SESSION_SECRET=disposable-session", "FREEFSM_SETUP_TOKEN=disposable-setup",
		"FREEFSM_ADDR=127.0.0.1:0", "FREEFSM_UPLOAD_DIR="+filepath.Join(root, "uploads"),
		"FREEFSM_STATE_DIR="+filepath.Join(root, "state"))
	return binary, env, db
}

func TestStartupRejectsUnsupportedServerBeforeBootstrap(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var version int
	if err = db.QueryRow(context.Background(), `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version >= 160000 {
		t.Skip("negative server-major case requires disposable PostgreSQL <16")
	}
	binary, env, fixture := installationFixture(t)
	output, err := runInstallationCLI(binary, env, "")
	if err == nil || !bytes.Contains(output, []byte("unsupported; use PostgreSQL 16")) {
		t.Fatalf("unsupported server: %v: %s", err, output)
	}
	var tables int
	if err = fixture.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("unsupported server partially bootstrapped")
	}
}

func runInstallationCLI(binary string, env []string, input string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env, cmd.Dir, cmd.Stdin = env, filepath.Dir(binary), strings.NewReader(input)
	return cmd.CombinedOutput()
}

func TestStartupRejectsMissingToolsBeforeBootstrap(t *testing.T) {
	binary, env, db := installationFixture(t)
	env = append(env, "PATH="+t.TempDir())
	output, err := runInstallationCLI(binary, env, "")
	if err == nil || !bytes.Contains(output, []byte("pg_dump")) {
		t.Fatalf("expected actionable missing-tool failure, got %v: %s", err, output)
	}
	var tables int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("unsupported startup partially bootstrapped %d tables", tables)
	}
}

func TestStartupRejectsIncompatibleToolsBeforeBootstrap(t *testing.T) {
	binary, env, db := installationFixture(t)
	tools := t.TempDir()
	env = append(env, "PATH="+tools)
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if err := os.WriteFile(filepath.Join(tools, tool), []byte("#!/bin/sh\necho '"+tool+" (PostgreSQL) 15.0'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	output, err := runInstallationCLI(binary, env, "")
	if err == nil || !bytes.Contains(output, []byte("matching-major")) {
		t.Fatalf("missing mismatch diagnostic: %v: %s", err, output)
	}
	var tables int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("mismatched tools allowed partial bootstrap")
	}
}

func recoveryFixture(t *testing.T, db *pgx.Conn) int64 {
	t.Helper()
	ctx := context.Background()
	if err := database.RunMigrations(ctx, db.Config().ConnString(), database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var id int64
	err := db.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) SELECT id,'admin@example.test',$1,'Admin','admin' FROM companies RETURNING id`, recoveryOldHash).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"web", "mobile"} {
		if _, err = db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at,kind) VALUES($1,$2,NOW()+interval '1 day',$3)`, fmt.Sprintf("%x", sha256.Sum256([]byte("old-"+kind))), id, kind); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(ctx, `INSERT INTO password_reset_tokens(token_hash,user_id,company_id,recipient,credential_revision,expires_at,purpose)
	 SELECT $1,id,company_id,email,credential_revision,NOW()+interval '1 hour','password-recovery-v1' FROM users WHERE id=$2`, fmt.Sprintf("%x", sha256.Sum256([]byte("old-recovery"))), id); err != nil {
		t.Fatal(err)
	}
	proofPool, err := pgxpool.New(ctx, db.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer proofPool.Close()
	if target, err := services.NewPasswordResetService(nil, proofPool).Validate(ctx, "old-recovery"); err != nil || target != id {
		t.Fatalf("executable host fixture proof invalid before reset: target=%d err=%v", target, err)
	}
	return id
}

func TestHostRecoveryResetsExistingAdminAtomically(t *testing.T) {
	binary, env, db := installationFixture(t)
	id := recoveryFixture(t, db)
	output, err := runInstallationCLI(binary, env, "Temporary-107!\nTemporary-107!\n", "-recover-admin", "admin@example.test")
	if err != nil {
		t.Fatalf("recovery: %v: %s", err, output)
	}
	if bytes.Contains(output, []byte("Temporary-107!")) {
		t.Fatal("password leaked")
	}
	var hash, role string
	var force bool
	if err := db.QueryRow(context.Background(), `SELECT password_hash,role,force_password_change FROM users WHERE id=$1`, id).Scan(&hash, &role, &force); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("Temporary-107!")) != nil || !force || role != "admin" {
		t.Fatal("password, forced-change or Role invariant failed")
	}
	var sessions, tokens, events, users int
	if err := db.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM sessions WHERE user_id=$1), (SELECT count(*) FROM password_reset_tokens WHERE user_id=$1), (SELECT count(*) FROM activity_logs WHERE object_type='user' AND object_id=$1 AND action='admin_password_recovered' AND actor_id IS NULL AND metadata->>'channel'='host-local'), (SELECT count(*) FROM users)`, id).Scan(&sessions, &tokens, &events, &users); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || tokens != 0 || events != 1 || users != 1 {
		t.Fatalf("sessions=%d recovery=%d audit=%d users=%d", sessions, tokens, events, users)
	}
	var immutableEvents int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM authorization_audit WHERE target_type='user' AND target_id=$1 AND channel='host-local'`, id).Scan(&immutableEvents); err != nil || immutableEvents != 1 {
		t.Fatalf("immutable host audit=%d err=%v", immutableEvents, err)
	}
	var attribution bool
	if err := db.QueryRow(context.Background(), `SELECT actor_kind='deployment-operator' AND actor_id IS NULL AND role_id IS NULL AND scope IS NULL AND action='admin_password_recovered' AND host_uid=$2 AND outcome='success' AND detail->>'target_role_id'='1' FROM authorization_audit WHERE target_type='user' AND target_id=$1 AND channel='host-local'`, id, fmt.Sprint(os.Getuid())).Scan(&attribution); err != nil || !attribution {
		t.Fatalf("host audit impersonates a Product User or Role: %v", err)
	}
	var encoded string
	if err := db.QueryRow(context.Background(), `SELECT jsonb_agg(to_jsonb(a))::text FROM authorization_audit a WHERE channel='host-local'`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Temporary-107!", recoveryOldHash, "old-recovery", "old-web", "old-mobile"} {
		if strings.Contains(encoded, secret) {
			t.Fatal("credential material entered immutable audit")
		}
	}
	for _, statement := range []string{`UPDATE authorization_audit SET detail='{}' WHERE channel='host-local'`, `DELETE FROM authorization_audit WHERE channel='host-local'`, `TRUNCATE authorization_audit`} {
		if _, err := db.Exec(context.Background(), statement); err == nil {
			t.Fatalf("immutable audit accepted mutation: %s", statement)
		}
	}
	base, client := startInstallationHTTP(t, binary, env)
	resetResponse, err := client.Get(base + "/reset-password?token=old-recovery")
	if err != nil {
		t.Fatal(err)
	}
	resetBody, err := io.ReadAll(resetResponse.Body)
	resetResponse.Body.Close()
	if err != nil || !bytes.Contains(resetBody, []byte("Invalid or expired reset link")) {
		t.Fatal("old recovery link still accepted")
	}
	for _, tc := range []struct {
		path, cookie, bearer string
		status               int
		location             string
	}{
		{"/customers", "old-web", "", 303, "/login"},
		{"/api/v1/me", "", "old-mobile", 401, ""},
		{"/setup", "", "", 303, "/login"},
	} {
		req, _ := http.NewRequest("GET", base+tc.path, nil)
		if tc.cookie != "" {
			req.AddCookie(&http.Cookie{Name: "session", Value: tc.cookie})
		}
		if tc.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+tc.bearer)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status || res.Header.Get("Location") != tc.location {
			t.Fatalf("%s: status=%d location=%s", tc.path, res.StatusCode, res.Header.Get("Location"))
		}
	}
	res := postInstallationForm(t, client, base, "/login", url.Values{"email": {"admin@example.test"}, "password": {"Temporary-107!"}})
	res.Body.Close()
	if res.StatusCode != 303 || res.Header.Get("Location") != "/change-password" {
		t.Fatalf("recovered password did not require browser change: status=%d location=%s", res.StatusCode, res.Header.Get("Location"))
	}
	res, err = client.Get(base + "/customers")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Location") != "/change-password" {
		t.Fatal("forced change bypassed through product route")
	}
	res = postInstallationForm(t, client, base, "/change-password", url.Values{"current_password": {"Temporary-107!"}, "new_password": {"Replacement-107!"}, "confirm_password": {"Replacement-107!"}})
	res.Body.Close()
	if res.StatusCode != 303 || !strings.HasPrefix(res.Header.Get("Location"), "/?flash=") {
		t.Fatal("password replacement failed")
	}
	res, err = client.Get(base + "/customers")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("customer list inaccessible after required change: %d", res.StatusCode)
	}
}

func startInstallationHTTP(t *testing.T, binary string, env []string) (string, *http.Client) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	cmd := exec.Command(binary)
	cmd.Dir, cmd.Env = filepath.Dir(binary), append(env, "FREEFSM_ADDR="+addr)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server exit: %v: %s", err, logs.String())
			}
		case <-time.After(40 * time.Second):
			cmd.Process.Kill()
			<-done
			t.Error("server did not drain")
		}
	})
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := "http://" + addr
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		res, err := client.Get(base + "/login")
		if err == nil {
			res.Body.Close()
			return base, client
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("server did not become available")
	return "", nil
}

func postInstallationForm(t *testing.T, client *http.Client, base, path string, values url.Values, target ...string) *http.Response {
	t.Helper()
	res, err := client.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("missing CSRF form at %s (status %d)", path, res.StatusCode)
	}
	values.Set("csrf_token", string(match[1]))
	for _, input := range regexp.MustCompile(`<input[^>]*type="hidden"[^>]*>`).FindAll(body, -1) {
		name := regexp.MustCompile(`name="([^"]+)"`).FindSubmatch(input)
		value := regexp.MustCompile(`value="([^"]*)"`).FindSubmatch(input)
		if len(name) == 2 && len(value) == 2 && !values.Has(string(name[1])) {
			values.Set(string(name[1]), html.UnescapeString(string(value[1])))
		}
	}
	if len(target) > 0 {
		path = target[0]
	}
	req, err := http.NewRequest("POST", base+path, strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestHostRecoveryRechecksConcurrentRoleChange(t *testing.T) {
	binary, env, db := installationFixture(t)
	recoveryFixture(t, db)
	recoveryReplacementAdmin(t, db)
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019); LOCK TABLE users IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	output := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data, err := runInstallationCLI(binary, env, "Temporary-107!\nTemporary-107!\n", "-recover-admin", "admin@example.test")
		output <- struct {
			data []byte
			err  error
		}{data, err}
	}()
	blocked := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if _, err = tx.Exec(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
			t.Fatal(err)
		}
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("recovery did not wait for identity transaction")
	}
	// The competing command is rejected by the cross-process operation lock.
	second, err := runInstallationCLI(binary, env, "Second-107!\nSecond-107!\n", "-recover-admin", "admin@example.test")
	if err == nil || !bytes.Contains(second, []byte("another instance operation")) {
		t.Fatalf("concurrent recovery not excluded: %v: %s", err, second)
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET authorization_role_id=2 WHERE email='admin@example.test'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	result := <-output
	if result.err == nil || !bytes.Contains(result.data, []byte("existing enabled Administrator")) {
		t.Fatalf("lost Admin authority accepted: %v: %s", result.err, result.data)
	}
	var unchanged bool
	if err = db.QueryRow(ctx, `SELECT password_hash=$1 AND NOT force_password_change AND (SELECT count(*) FROM sessions)=2 AND (SELECT count(*) FROM password_reset_tokens)=1 AND (SELECT count(*) FROM activity_logs)=0 FROM users WHERE email='admin@example.test'`, recoveryOldHash).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("concurrent denial changed state", err)
	}
}

func TestInstallationBootstrapAndCoreWorkflowsWithoutProviders(t *testing.T) {
	binary, env, db := installationFixture(t)
	base, client := startInstallationHTTP(t, binary, env)
	res := postInstallationForm(t, client, base, "/setup", url.Values{"token": {"disposable-setup"}, "name": {"Admin"}, "email": {"admin@example.test"}, "password": {"Bootstrap-107!"}, "confirm_password": {"Bootstrap-107!"}})
	res.Body.Close()
	if res.StatusCode != 303 || res.Header.Get("Location") != "/setup/company" {
		t.Fatalf("bootstrap: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res = postInstallationForm(t, client, base, "/setup/company", url.Values{"business_name": {"Installation 107"}, "timezone": {"UTC"}})
	res.Body.Close()
	if res.StatusCode != 303 || !strings.HasPrefix(res.Header.Get("Location"), "/?flash=") {
		t.Fatalf("company setup: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	for _, provider := range []string{"absent", "unreachable"} {
		t.Run(provider, func(t *testing.T) {
			if provider == "unreachable" {
				if _, err := db.Exec(context.Background(), `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=1,map_tile_url='http://127.0.0.1:1/{z}/{x}/{y}.png',geocoder_url='http://127.0.0.1:1'`); err != nil {
					t.Fatal(err)
				}
			}
			name := "Customer " + provider
			res := postInstallationForm(t, client, base, "/customers/new", url.Values{"display_name": {name}, "first_name": {"Test"}}, "/customers")
			res.Body.Close()
			if res.StatusCode != 303 {
				t.Fatalf("customer create: %d", res.StatusCode)
			}
			match := regexp.MustCompile(`/customers/(\d+)`).FindStringSubmatch(res.Header.Get("Location"))
			if len(match) != 2 {
				t.Fatal("customer redirect missing identity")
			}
			res = postInstallationForm(t, client, base, "/jobs/new", url.Values{"customer_id": {match[1]}, "job_type": {"Job " + provider}}, "/jobs")
			res.Body.Close()
			if res.StatusCode != 303 || res.Header.Get("Location") != "/jobs?flash=Job+created" {
				t.Fatalf("job create: %d %s", res.StatusCode, res.Header.Get("Location"))
			}
			for path, want := range map[string]string{"/customers": name, "/jobs": "Job " + provider} {
				res, err := client.Get(base + path)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil || res.StatusCode != 200 || !bytes.Contains(body, []byte(want)) {
					t.Fatalf("persisted %s unavailable: %d %v", path, res.StatusCode, err)
				}
			}
		})
	}
	res, err := client.Get(base + "/setup")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Location") != "/login" {
		t.Fatal("bootstrap not closed")
	}
}

func TestHostRecoveryRejectsWithoutPartialEffects(t *testing.T) {
	binary, env, db := installationFixture(t)
	id := recoveryFixture(t, db)
	recoveryReplacementAdmin(t, db)
	ctx := context.Background()
	for _, tc := range []struct{ name, email, input, setup, cleanup string }{
		{"missing", "missing@example.test", "Valid-Pass107!\nValid-Pass107!\n", "", ""},
		{"weak", "admin@example.test", "weak\nweak\n", "", ""},
		{"confirmation", "admin@example.test", "Valid-Pass107!\nDifferent107!\n", "", ""},
		{"policy", "admin@example.test", "Valid-Pass107!\nValid-Pass107!\n", "UPDATE company_settings SET password_min_length=30", "UPDATE company_settings SET password_min_length=8"},
		{"non-admin", "admin@example.test", "Valid-Pass107!\nValid-Pass107!\n", "UPDATE users SET authorization_role_id=2 WHERE email='admin@example.test'", "UPDATE users SET authorization_role_id=1 WHERE email='admin@example.test'"},
		{"disabled", "admin@example.test", "Valid-Pass107!\nValid-Pass107!\n", "UPDATE users SET is_active=false WHERE email='admin@example.test'", "UPDATE users SET is_active=true WHERE email='admin@example.test'"},
		{"audit-failure", "admin@example.test", "Valid-Pass107!\nValid-Pass107!\n", `CREATE FUNCTION reject_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$; CREATE TRIGGER reject_recovery_audit BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION reject_recovery_audit()`, "DROP TRIGGER reject_recovery_audit ON activity_logs; DROP FUNCTION reject_recovery_audit()"},
		{"protected-audit-failure", "admin@example.test", "Valid-Pass107!\nValid-Pass107!\n", `CREATE FUNCTION reject_protected_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$; CREATE TRIGGER reject_protected_recovery_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_protected_recovery_audit()`, "DROP TRIGGER reject_protected_recovery_audit ON authorization_audit; DROP FUNCTION reject_protected_recovery_audit()"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != "" {
				if _, err := db.Exec(ctx, tc.setup); err != nil {
					t.Fatal(err)
				}
				defer db.Exec(ctx, tc.cleanup)
			}
			var priorTokens int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM password_reset_tokens`).Scan(&priorTokens); err != nil {
				t.Fatal(err)
			}
			output, err := runInstallationCLI(binary, env, tc.input, "-recover-admin", tc.email)
			if err == nil {
				t.Fatalf("invalid recovery succeeded: %s", output)
			}
			var hash string
			var force bool
			var sessions, tokens, events, users, immutable int
			err = db.QueryRow(ctx, `SELECT password_hash,force_password_change,(SELECT count(*) FROM sessions),(SELECT count(*) FROM password_reset_tokens),(SELECT count(*) FROM activity_logs),(SELECT count(*) FROM users),(SELECT count(*) FROM authorization_audit) FROM users WHERE id=$1`, id).Scan(&hash, &force, &sessions, &tokens, &events, &users, &immutable)
			if err != nil {
				t.Fatal(err)
			}
			if hash != recoveryOldHash || force || sessions != 2 || tokens != priorTokens || events != 0 || users != 2 || immutable != 0 {
				t.Fatal("failed recovery changed persisted state")
			}
		})
	}
}

// Structurally valid bootstrap credential, as required by the last-Admin guard.
const recoveryOldHash = "$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901"

func recoveryReplacementAdmin(t *testing.T, db *pgx.Conn) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `INSERT INTO users(company_id,email,password_hash,name,role,authorization_role_id) SELECT id,'replacement@example.test',$1,'Replacement','tech',1 FROM companies`, recoveryOldHash); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationBrowserRecoveryAndCoreWorkflows(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and install playwright-core for persisted browser acceptance")
	}
	binary, env, db := installationFixture(t)
	recoveryCutoverFixture(t, binary, env, db)
	output, err := runInstallationCLI(binary, env, "Temporary-107!\nTemporary-107!\n", "-recover-admin", "admin@example.test")
	if err != nil {
		t.Fatalf("recovery: %v: %s", err, output)
	}
	base, _ := startInstallationHTTP(t, binary, env)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "../../deploy/testdata/installation-browser.cjs", base)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("persisted browser: %v: %s", err, output)
	}
	t.Log(string(output))
}

// Upgrade through the real reviewed cutover with deliberately divergent legacy
// and protected identities. Both CLI denial and the browser journey use this.
func recoveryCutoverFixture(t *testing.T, binary string, env []string, db *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() > "054_backup_activity.up.sql" {
			continue
		}
		data, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err := database.RunMigrations(ctx, db.Config().ConnString(), old); err != nil {
		t.Fatal(err)
	}
	var approver, target int64
	for _, fixture := range []struct {
		email, role string
		id          *int64
	}{{"legacy@example.test", "admin", &approver}, {"admin@example.test", "dispatcher", &target}} {
		if err := db.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) SELECT id,$1,$2,'Recovery upgrade',$3 FROM companies RETURNING id`, fixture.email, recoveryOldHash, fixture.role).Scan(fixture.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.RunMigrations(ctx, db.Config().ConnString(), database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	assertDenied := func() {
		t.Helper()
		output, err := runInstallationCLI(binary, env, "Temporary-107!\nTemporary-107!\n", "-recover-admin", "legacy@example.test")
		if err == nil || !bytes.Contains(output, []byte("existing enabled Administrator")) {
			t.Fatalf("legacy authority accepted: %v: %s", err, output)
		}
	}
	assertDenied()
	pool, err := pgxpool.New(ctx, db.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: approver}
	for _, decision := range []struct{ user, role int64 }{{approver, 4}, {target, 1}} {
		review, err := svc.Review(ctx, actor)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.ApproveUser(ctx, actor, decision.user, decision.role, review.Snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Cutover(ctx, actor); err != nil {
		t.Fatal(err)
	}
	assertDenied()
}

func TestHostRecoveryAfterReviewedCutover(t *testing.T) {
	binary, env, db := installationFixture(t)
	recoveryCutoverFixture(t, binary, env, db)
	output, err := runInstallationCLI(binary, env, "Temporary-107!\nTemporary-107!\n", "-recover-admin", "admin@example.test")
	if err != nil {
		t.Fatalf("protected identity recovery: %v: %s", err, output)
	}
	var hash, legacy string
	var protected, force bool
	if err := db.QueryRow(context.Background(), `SELECT u.password_hash,u.role,r.protected,u.force_password_change FROM users u JOIN authorization_roles r ON r.id=u.authorization_role_id WHERE email='admin@example.test'`).Scan(&hash, &legacy, &protected, &force); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("Temporary-107!")) != nil || legacy != "dispatcher" || !protected || !force {
		t.Fatal("post-cutover recovery changed identity or failed to reset credential")
	}
	var immutable int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM authorization_audit a JOIN users u ON u.id=a.target_id WHERE a.action='admin_password_recovered' AND a.actor_kind='deployment-operator' AND a.actor_id IS NULL AND a.role_id IS NULL AND a.scope IS NULL AND a.host_uid=$1 AND u.email='admin@example.test'`, fmt.Sprint(os.Getuid())).Scan(&immutable); err != nil || immutable != 1 {
		t.Fatalf("post-cutover immutable host attribution=%d err=%v", immutable, err)
	}
}
