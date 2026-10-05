package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"golang.org/x/crypto/bcrypt"
)

type statusBackup struct {
	backupEngine
	calls int
}

type backupUserFixture struct{ user ent.User }

func (u *backupUserFixture) GetByID(_ context.Context, id int64) (*ent.User, error) {
	return &u.user, nil
}

type backupSettingsFixture struct{}

func (backupSettingsFixture) Get(context.Context) (*ent.CompanySettings, error) {
	return &ent.CompanySettings{BusinessName: "Trusted destination"}, nil
}

type workflowBackup struct {
	backupEngine
	downloads, restores int
}

func (m *workflowBackup) Status(actor int64, id string) (backup.Operation, error) {
	if actor != 7 || id != "reviewed" {
		return backup.Operation{}, backup.ErrNotFound
	}
	return backup.Operation{ID: id, Kind: "upload", Phase: "ready", ArchiveDigest: "immutable-digest"}, nil
}
func (m *workflowBackup) StartRestore(_ context.Context, actor int64, id, destination, confirmation string) (backup.Operation, error) {
	if actor != 7 || id != "reviewed" || destination != "Trusted destination" || confirmation != destination {
		return backup.Operation{}, backup.ErrInvalid
	}
	m.restores++
	return backup.Operation{ID: id, Phase: "queued"}, nil
}
func (m *workflowBackup) OpenDownload(actor int64, id string) (io.ReadCloser, string, int64, error) {
	if actor != 7 || id != "reviewed" {
		return nil, "", 0, backup.ErrNotFound
	}
	m.downloads++
	return io.NopCloser(strings.NewReader("encrypted")), "archive.age", 9, nil
}

func TestBackupAuthorizationAndFreshConfirmation(t *testing.T) {
	c, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("current-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := &backupUserFixture{ent.User{ID: 7, Role: "admin", IsActive: true, PasswordHash: string(hash)}}
	m := &workflowBackup{}
	refreshes := 0
	h := &BackupHandler{manager: m, control: c, users: u, settings: backupSettingsFixture{}, refresh: func() error { refreshes++; return nil }}
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if refreshes == 0 {
				t.Fatal("auth before refresh")
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.UserKey, &middleware.UserInfo{ID: 7, Role: u.user.Role})))
		})
	}
	routes := h.controlRoutes(auth)
	request := func(path, password, id, digest, confirmation string) *httptest.ResponseRecorder {
		values := url.Values{"current_password": {password}, "operation": {id}, "archive_digest": {digest}, "confirmation": {confirmation}, "destination": {"untrusted"}}
		r := httptest.NewRequest(http.MethodPost, "/settings/backup/"+path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Backup-Operation", "reviewed")
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w
	}
	for _, role := range []string{"dispatcher", "technician", "customer"} {
		u.user.Role = role
		if w := request("download", "current-password", "reviewed", "", ""); w.Code != 403 {
			t.Fatalf("%s got %d", role, w.Code)
		}
	}
	u.user.Role = "admin"
	for _, path := range []string{"download", "restore"} {
		if w := request(path, "wrong", "reviewed", "immutable-digest", "Trusted destination"); w.Code != 403 {
			t.Fatalf("wrong password: %d", w.Code)
		}
	}
	u.user.IsActive = false
	if w := request("download", "current-password", "reviewed", "", ""); w.Code != 403 {
		t.Fatal("inactive administrator authorized")
	}
	u.user.IsActive = true
	for _, input := range [][3]string{{"other", "immutable-digest", "Trusted destination"}, {"reviewed", "stale", "Trusted destination"}, {"reviewed", "immutable-digest", "trusted destination"}} {
		if w := request("restore", "current-password", input[0], input[1], input[2]); w.Code == 200 {
			t.Fatal("mismatched confirmation accepted")
		}
	}
	if m.restores != 0 || m.downloads != 0 {
		t.Fatal("unauthorized engine boundary reached")
	}
	if w := request("restore", "current-password", "reviewed", "immutable-digest", "Trusted destination"); w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if w := request("download", "current-password", "reviewed", "", ""); w.Code != 200 || w.Body.String() != "encrypted" || !strings.Contains(w.Header().Get("Content-Disposition"), ".age") {
		t.Fatalf("download: %d %s", w.Code, w.Body)
	}
	if m.restores != 1 || m.downloads != 1 {
		t.Fatal("expected authorized engine boundaries")
	}
}

func (s *statusBackup) StatusCapability(id string) (backup.Operation, error) {
	s.calls++
	return backup.Operation{ID: id, Phase: "complete"}, nil
}

func TestBackupMaintenanceStatusDoesNotUseAuthentication(t *testing.T) {
	c, err := instancecontrol.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.KeepClosed(); err != nil {
		t.Fatal(err)
	}
	m := &statusBackup{}
	h := &BackupHandler{manager: m, control: c, refresh: func() error { t.Fatal("refresh during maintenance"); return nil }}
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("authentication during maintenance") })
	}
	routes := h.controlRoutes(auth)
	for _, path := range []string{"/settings/backup", "/settings/backup/create", "/settings/backup/restore", "/settings/backup/download"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set("X-Backup-Operation", "capability")
		routes.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/settings/backup", "/settings/backup/status-page"} {
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "backup-progress") {
			t.Fatalf("maintenance status shell %s: %d", path, w.Code)
		}
		// The shared script references authenticated controls, but those references
		// are not rendered controls. Check markup, not bare IDs anywhere in JS.
		// TestBackupBrowserWorkflow additionally checks the live Chromium DOM.
		for _, forbiddenMarkup := range []string{"<form", "<input", "Destination:"} {
			if strings.Contains(w.Body.String(), forbiddenMarkup) {
				t.Fatalf("status shell exposes control or account markup %s", forbiddenMarkup)
			}
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/settings/backup/status", nil)
	r.Header.Set("X-Backup-Operation", "capability")
	routes.ServeHTTP(w, r)
	if w.Code != http.StatusOK || m.calls != 1 {
		t.Fatalf("status: %d calls %d", w.Code, m.calls)
	}
	for _, target := range []string{"/settings/backup/status?operation=capability", "/settings/backup/status"} {
		w = httptest.NewRecorder()
		routes.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code == http.StatusOK {
			t.Fatal("GET capability accepted")
		}
	}
}
