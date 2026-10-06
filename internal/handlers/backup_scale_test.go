//go:build linux

package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
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
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/justinas/nosurf"
	"golang.org/x/crypto/bcrypt"
)

// This is acceptance INTEGRATION composition, not a released binary. Only here
// are clean matching release identities injected. No production gate is bypassed.
// The supplied admin URL MUST identify a freshly provisioned disposable cluster.
func TestBackupBrowserNginxScale(t *testing.T) {
	if os.Getenv("FREEFSM_BACKUP_BROWSER_SCALE") != "1" {
		t.Skip("opt-in: see docs/backup-browser-acceptance.md")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Fatal("FREEFSM_BACKUP_TEST_ADMIN_URL must name a disposable cluster")
	}
	logical := int64(1 << 30)
	if s := os.Getenv("FREEFSM_BACKUP_BROWSER_BYTES"); s != "" {
		var err error
		logical, err = strconv.ParseInt(s, 10, 64)
		scaleCheck(t, err)
		if logical < 1 {
			t.Fatal("positive dataset required")
		}
	}
	root := t.TempDir()
	var fs syscall.Statfs_t
	scaleCheck(t, syscall.Statfs(root, &fs))
	if uint64(fs.Bavail)*uint64(fs.Bsize) < uint64(8*logical+(1<<30)) {
		t.Fatal("need 8x logical bytes plus 1 GiB free on TMPDIR; use disk-backed scratch")
	}
	admin, err := pgx.Connect(ctx, dsn)
	scaleCheck(t, err)
	defer admin.Close(context.Background())
	deployment, err := os.ReadFile("../../deploy/linux/freefsm.nginx.conf")
	scaleCheck(t, err)
	location := regexp.MustCompile(`(?s)    location \^~ /settings/backup \{.*?\n    \}`).Find(deployment)
	if len(location) == 0 {
		t.Fatal("supported deployment location missing")
	}
	locationHash := sha256.Sum256(location)
	var downloaded, uploaded atomic.Int64
	type instance struct {
		manager                      *backup.Manager
		cfg                          backup.Config
		control                      *instancecontrol.Control
		client                       *ent.Client
		sessions                     *services.SessionService
		web, mobile, uploads, origin string
		email                        string
		company, actor               int64
	}
	instances := make([]instance, 2)
	for i := range instances {
		f := &instances[i]
		name := fmt.Sprintf("browser_scale_%d_%d", time.Now().UnixNano(), i)
		ident := pgx.Identifier{name}.Sanitize()
		_, err = admin.Exec(ctx, "CREATE ROLE "+ident+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION")
		scaleCheck(t, err)
		_, err = admin.Exec(ctx, "CREATE DATABASE "+ident+" OWNER "+ident+" TEMPLATE template0 ENCODING 'UTF8'")
		scaleCheck(t, err)
		defer func() {
			_, e := admin.Exec(context.Background(), "DROP DATABASE "+ident+" WITH (FORCE)")
			if e != nil {
				t.Error(e)
			}
			_, e = admin.Exec(context.Background(), "DROP ROLE "+ident)
			if e != nil {
				t.Error(e)
			}
		}()
		u, e := url.Parse(dsn)
		scaleCheck(t, e)
		u.User, u.Path = url.User(name), "/"+name
		base := filepath.Join(root, strconv.Itoa(i))
		f.uploads = filepath.Join(base, "uploads")
		scaleCheck(t, os.MkdirAll(f.uploads, 0700))
		f.control, err = instancecontrol.New(filepath.Join(base, "state"))
		scaleCheck(t, err)
		db, e := database.Connect(ctx, u.String())
		scaleCheck(t, e)
		defer db.Close()
		f.cfg = backup.Config{DSN: u.String(), UploadDir: f.uploads, StateDir: f.control.StateDir(), Version: "v1.2.3", Commit: "abcdef1234567", Control: f.control, ResetConnections: db.Pool.Reset}
		f.manager, err = backup.New(f.cfg)
		scaleCheck(t, err)
		defer f.manager.Shutdown()
		scaleCheck(t, f.manager.Recover(ctx))
		scaleCheck(t, db.Migrate(ctx, os.DirFS("../database/migrations")))
		pgcfg, e := pgx.ParseConfig(u.String())
		scaleCheck(t, e)
		pgcfg.DefaultQueryExecMode = pgx.QueryExecModeExec
		pgcfg.StatementCacheCapacity, pgcfg.DescriptionCacheCapacity = 0, 0
		f.client = ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, stdlib.OpenDB(*pgcfg))))
		defer f.client.Close()
		var privileged bool
		scaleCheck(t, db.Pool.QueryRow(ctx, "SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication FROM pg_roles WHERE rolname=current_user").Scan(&privileged))
		if privileged {
			t.Fatal("privileged application role")
		}
		scaleCheck(t, db.Pool.QueryRow(ctx, "INSERT INTO companies(name,slug) VALUES($1,$1) RETURNING id", name).Scan(&f.company))
		hash, e := bcrypt.GenerateFromPassword([]byte(fmt.Sprintf("account-%d", i)), bcrypt.MinCost)
		scaleCheck(t, e)
		actor := f.client.User.Create().SetCompanyID(f.company).SetName(name).SetEmail(name + "@example.test").SetRole("admin").SetIsActive(true).SetPasswordHash(string(hash)).SaveX(ctx)
		f.actor = actor.ID
		f.email = actor.Email
		settings := f.client.CompanySettings.Query().FirstX(ctx)
		f.client.CompanySettings.UpdateOne(settings).SetCompanyID(f.company).SetBusinessName(fmt.Sprintf("Instance %d", i)).SetSMTPPassword(fmt.Sprintf("smtp-%d", i)).SaveX(ctx)
		f.sessions = services.NewSessionService(db.Pool)
		f.web, err = f.sessions.Create(ctx, actor.ID)
		scaleCheck(t, err)
		f.mobile, err = f.sessions.CreateMobile(ctx, actor.ID, "scale")
		scaleCheck(t, err)
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		router := NewBackupRouter(f.manager, f.control, services.NewUserService(f.client), services.NewCompanySettingsService(f.client), f.sessions, "", backupTestActivityHandler(f.client))
		mux.Handle("/settings/backup", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) }))
		mux.Handle("/settings/backup/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/settings/backup/upload" {
				r.Body = &scaleReadCounter{ReadCloser: r.Body, bytes: &uploaded}
			}
			if r.URL.Path == "/settings/backup/download" {
				w = &scaleWriteCounter{ResponseWriter: w, bytes: &downloaded}
			}
			router.ServeHTTP(w, r)
		}))
		web := New(db.Pool, f.client, f.sessions, &config.Config{UploadDir: f.uploads}, f.control)
		csrf := nosurf.New(middleware.CSRFToken(web))
		csrf.SetIsTLSFunc(middleware.IsHTTPS)
		mux.Handle("/", f.control.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := f.manager.RefreshConnections(); err != nil {
				http.Error(w, "Instance connections unavailable", 503)
				return
			}
			csrf.ServeHTTP(w, r)
		})))
		server := httptest.NewServer(mux)
		defer server.Close()
		f.origin = scaleNginx(t, ctx, root, i, server.URL, string(location))
	}
	src, dst := &instances[0], &instances[1]
	// Real database-linked customer attachments, each below the ordinary 25 MiB
	// attachment limit. crypto/rand content cannot compress away the transfer.
	customer := src.client.Customer.Create().SetCompanyID(src.company).SetDisplayName("Scale attachments").SaveX(ctx)
	want := map[string]string{}
	for remaining, n := logical, 0; remaining > 0; n++ {
		size := min(remaining, int64(16<<20))
		name := fmt.Sprintf("attachment-%03d.bin", n)
		path := filepath.Join(src.uploads, name)
		file, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		scaleCheck(t, e)
		h := sha256.New()
		_, e = io.CopyN(io.MultiWriter(file, h), rand.Reader, size)
		scaleCheck(t, e)
		scaleCheck(t, file.Close())
		want[name] = hex.EncodeToString(h.Sum(nil))
		conn, e := pgx.Connect(ctx, src.cfg.DSN)
		scaleCheck(t, e)
		_, e = conn.Exec(ctx, `INSERT INTO files(company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by) VALUES($1,'customer',$2,$3,$3,'application/octet-stream',$4,$5,$6)`, src.company, customer.ID, name, size, path, src.actor)
		scaleCheck(t, e)
		scaleCheck(t, conn.Close(ctx))
		remaining -= size
	}
	scaleCheck(t, os.WriteFile(filepath.Join(dst.uploads, "destination-only"), []byte("must disappear"), 0600))
	// Give recovery a substantial incompressible destination file too (128 MiB at
	// full scale), so capacity measurements include a genuine rollback copy.
	f, err := os.Create(filepath.Join(dst.uploads, "old-content.bin"))
	scaleCheck(t, err)
	_, err = io.CopyN(f, rand.Reader, min(logical, int64(128<<20)))
	scaleCheck(t, err)
	scaleCheck(t, f.Close())
	spec := map[string]any{"source": src.origin, "destination": dst.origin, "sourceSession": src.web, "destinationSession": dst.web, "sourceEmail": src.email, "minimumBytes": logical, "downloadDir": root}
	b, err := json.Marshal(spec)
	scaleCheck(t, err)
	specPath := filepath.Join(root, "browser.json")
	scaleCheck(t, os.WriteFile(specPath, b, 0600))
	metrics := scaleMetrics{LogicalAttachmentBytes: logical, NginxLocationSHA256: hex.EncodeToString(locationHash[:])}
	stop := make(chan struct{})
	measured := make(chan struct{})
	go func() {
		defer close(measured)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				app, children := scaleRSS()
				metrics.PeakAppRSSBytes = max(metrics.PeakAppRSSBytes, app)
				metrics.PeakAppAndPGChildrenRSSBytes = max(metrics.PeakAppAndPGChildrenRSSBytes, app+children)
				logical, allocated := scaleStorage(root)
				metrics.PeakScratchLogicalBytes = max(metrics.PeakScratchLogicalBytes, logical)
				metrics.PeakScratchAllocatedBytes = max(metrics.PeakScratchAllocatedBytes, allocated)
			}
		}
	}()
	started := time.Now()
	command := exec.CommandContext(ctx, "node", "testdata/browser/backup-scale.cjs", specPath)
	output, browserErr := command.CombinedOutput()
	metrics.WorkflowSeconds = time.Since(started).Seconds()
	close(stop)
	<-measured
	metrics.DownloadedBytes, metrics.UploadedBytes = downloaded.Load(), uploaded.Load()
	if b, e := os.ReadFile(filepath.Join(root, "browser-measurements.json")); e == nil {
		metrics.Browser = json.RawMessage(b)
	}
	t.Logf("browser: %s", output)
	// Preserve measurements even on a failed UI workflow; failed runs are not
	// acceptance. No credentials or operation capabilities enter this report.
	defer func() {
		b, e := json.MarshalIndent(metrics, "", "  ")
		scaleCheck(t, e)
		t.Logf("MEASUREMENTS %s", b)
		if path := os.Getenv("FREEFSM_BACKUP_BROWSER_REPORT"); path != "" {
			scaleCheck(t, os.WriteFile(path, b, 0600))
		}
	}()
	scaleCheck(t, browserErr)
	if metrics.DownloadedBytes < logical || metrics.UploadedBytes != metrics.DownloadedBytes {
		t.Fatal("incomplete or undersized browser transfer")
	}
	// A ceiling independent of archive size catches whole-archive buffering. The
	// default 1 GiB run must stay under 768 MiB for the app plus pg tool children.
	if logical >= 1<<30 && metrics.PeakAppAndPGChildrenRSSBytes >= 768<<20 {
		t.Fatal("app + PostgreSQL children exceeded 768 MiB RSS budget")
	}
	scaleCheck(t, dst.manager.RefreshConnections())
	entries, err := os.ReadDir(dst.uploads)
	scaleCheck(t, err)
	if len(entries) != len(want) {
		t.Fatalf("restored inventory: got %d want %d", len(entries), len(want))
	}
	for name, digest := range want {
		file, e := os.Open(filepath.Join(dst.uploads, name))
		scaleCheck(t, e)
		h := sha256.New()
		_, e = io.Copy(h, file)
		scaleCheck(t, e)
		scaleCheck(t, file.Close())
		if hex.EncodeToString(h.Sum(nil)) != digest {
			t.Fatalf("restored digest differs: %s", name)
		}
	}
	conn, err := pgx.Connect(ctx, dst.cfg.DSN)
	scaleCheck(t, err)
	defer conn.Close(ctx)
	var count, bytes int64
	scaleCheck(t, conn.QueryRow(ctx, "SELECT count(*),sum(file_size) FROM files WHERE file_path LIKE $1", dst.uploads+"/%").Scan(&count, &bytes))
	if count != int64(len(want)) || bytes != logical {
		t.Fatalf("restored database file references: %d/%d", count, bytes)
	}
	settings := dst.client.CompanySettings.Query().OnlyX(ctx)
	if settings.BusinessName != "Instance 0" || settings.SMTPPassword != "smtp-0" || !dst.control.EmailDisabled() {
		t.Fatal("settings/email overrides not restored")
	}
	for _, f := range instances {
		if _, e := dst.sessions.Validate(ctx, f.web); e == nil {
			t.Fatal("old web session survived")
		}
		if _, e := dst.sessions.ValidateMobile(ctx, f.mobile); e == nil {
			t.Fatal("old mobile session survived")
		}
	}
	// Completed restore must promptly remove staging and recovery directories.
	scaleAssertClean(t, dst.control.StateDir())
	metrics.AfterRestoreLogicalBytes, _ = scaleStorage(root)
	// Download retention is intentional (one hour). Exercise documented restart
	// cleanup after closing its manager rather than waiting an hour or changing TTL.
	for _, f := range instances {
		f.manager.Shutdown()
		reopened, e := backup.New(f.cfg)
		scaleCheck(t, e)
		scaleCheck(t, reopened.Recover(ctx))
		reopened.Shutdown()
		scaleAssertClean(t, f.control.StateDir())
	}
	metrics.AfterRestartCleanupLogicalBytes, _ = scaleStorage(root)
	reopenedControl, err := instancecontrol.New(dst.control.StateDir())
	scaleCheck(t, err)
	if !reopenedControl.EmailDisabled() {
		t.Fatal("email gate lost after restart cleanup")
	}
	metrics.Passed = true
	metrics.ScaleTargetMet = logical >= 1<<30
}

func scaleCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type scaleReadCounter struct {
	io.ReadCloser
	bytes *atomic.Int64
}

func (r *scaleReadCounter) Read(p []byte) (int, error) {
	n, e := r.ReadCloser.Read(p)
	r.bytes.Add(int64(n))
	return n, e
}

type scaleWriteCounter struct {
	http.ResponseWriter
	bytes *atomic.Int64
}

func (w *scaleWriteCounter) Write(p []byte) (int, error) {
	n, e := w.ResponseWriter.Write(p)
	w.bytes.Add(int64(n))
	return n, e
}
func (w *scaleWriteCounter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func scaleNginx(t *testing.T, ctx context.Context, root string, i int, upstream, location string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	scaleCheck(t, err)
	addr := listener.Addr().String()
	scaleCheck(t, listener.Close())
	config := fmt.Sprintf("worker_processes 1;\nerror_log /dev/stderr warn;\npid /tmp/nginx.pid;\nevents { worker_connections 128; }\nhttp { access_log off; upstream freefsm { server %s; } server { listen %s;\n%s\nlocation / { proxy_pass http://freefsm; } } }\n", strings.TrimPrefix(upstream, "http://"), addr, location)
	// The entire supported location is inserted byte-for-byte, without rewriting
	// any directive. Only the upstream and outer HTTP-only loopback listener vary.
	if !strings.Contains(config, location) {
		t.Fatal("nginx location changed")
	}
	path := filepath.Join(root, fmt.Sprintf("nginx-%d.conf", i))
	scaleCheck(t, os.WriteFile(path, []byte(config), 0644))
	name := fmt.Sprintf("freefsm-scale-%d-%d", os.Getpid(), i)
	image := os.Getenv("FREEFSM_BACKUP_NGINX_IMAGE")
	if image == "" {
		image = "docker.io/library/nginx:alpine"
	}
	cmd := exec.CommandContext(ctx, "podman", "run", "--rm", "--network=host", "--name", name, "-v", path+":/etc/nginx/nginx.conf:ro,Z", image, "nginx", "-g", "daemon off;")
	log, err := os.Create(filepath.Join(root, fmt.Sprintf("nginx-%d.log", i)))
	scaleCheck(t, err)
	cmd.Stdout, cmd.Stderr = log, log
	scaleCheck(t, cmd.Start())
	t.Cleanup(func() {
		_ = exec.Command("podman", "stop", "-t", "2", name).Run()
		_ = cmd.Wait()
		_ = log.Close()
		if t.Failed() {
			b, _ := os.ReadFile(log.Name())
			t.Logf("nginx: %s", b)
		}
	})
	origin := "http://" + addr
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		response, e := client.Get(origin + "/settings/backup")
		if e == nil {
			_ = response.Body.Close()
			return origin
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nginx failed to start")
	return ""
}

type scaleMetrics struct {
	ScaleTargetMet                                            bool
	Browser                                                   json.RawMessage `json:",omitempty"`
	Passed                                                    bool
	LogicalAttachmentBytes, DownloadedBytes, UploadedBytes    int64
	WorkflowSeconds                                           float64
	PeakAppRSSBytes, PeakAppAndPGChildrenRSSBytes             int64
	PeakScratchLogicalBytes, PeakScratchAllocatedBytes        int64
	AfterRestoreLogicalBytes, AfterRestartCleanupLogicalBytes int64
	NginxLocationSHA256                                       string
}

// RSS is sampled every 100ms from Linux procfs. Native pg_dump/pg_restore are
// direct children of the application; browser/node/proxy are deliberately not
// confused with application memory. Shared pages are conservatively double-counted.
func scaleRSS() (app, children int64) {
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, e := strconv.Atoi(entry.Name())
		if e != nil {
			continue
		}
		b, e := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if e != nil {
			continue
		}
		var parent int
		var rss int64
		var name string
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch fields[0] {
			case "Name:":
				name = fields[1]
			case "PPid:":
				parent, _ = strconv.Atoi(fields[1])
			case "VmRSS:":
				rss, _ = strconv.ParseInt(fields[1], 10, 64)
				rss *= 1024
			}
		}
		if pid == os.Getpid() {
			app = rss
		}
		if parent == os.Getpid() && (name == "pg_dump" || name == "pg_restore") {
			children += rss
		}
	}
	return
}

func scaleStorage(root string) (logical, allocated int64) {
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			logical += info.Size()
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				allocated += stat.Blocks * 512
			}
		}
		return nil
	})
	return
}

func scaleAssertClean(t *testing.T, state string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(state, "backup"))
	scaleCheck(t, err)
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "journal.json" {
			t.Fatalf("unsafe leftover backup material: %s", entry.Name())
		}
	}
}
