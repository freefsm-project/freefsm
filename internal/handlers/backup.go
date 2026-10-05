package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/justinas/nosurf"
	"golang.org/x/crypto/bcrypt"
)

type backupEngine interface {
	StartBackup(int64, string, string) (backup.Operation, error)
	StageUpload(context.Context, int64, io.Reader, string) (backup.Operation, error)
	StartRestore(context.Context, int64, string, string, string) (backup.Operation, error)
	Status(int64, string) (backup.Operation, error)
	StatusCapability(string) (backup.Operation, error)
	OpenDownload(int64, string) (io.ReadCloser, string, int64, error)
}
type backupUsers interface {
	GetByID(context.Context, int64) (*ent.User, error)
}
type backupSettings interface {
	Get(context.Context) (*ent.CompanySettings, error)
}

type BackupHandler struct {
	manager        backupEngine
	control        *instancecontrol.Control
	refresh        func() error
	users          backupUsers
	settings       backupSettings
	disabledReason string
}

func NewBackupRouter(manager *backup.Manager, control *instancecontrol.Control, users *services.UserService, settings *services.CompanySettingsService, sessions *services.SessionService, disabledReason string) http.Handler {
	h := &BackupHandler{manager: manager, control: control, refresh: manager.RefreshConnections, users: users, settings: settings, disabledReason: disabledReason}
	auth := middleware.Auth(sessions, func(ctx context.Context, id int64) (*middleware.UserInfo, error) {
		u, err := users.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if !u.IsActive || u.ForcePasswordChange {
			return nil, errors.New("account unavailable")
		}
		return &middleware.UserInfo{ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role}, nil
	})
	csrf := nosurf.New(h.controlRoutes(auth))
	csrf.SetIsTLSFunc(middleware.IsHTTPS)
	// Require the header for uploads: nosurf must never parse a large form.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.RawQuery != "" {
			http.Error(w, "Use request bodies or headers, not query parameters", 400)
			return
		}
		if r.URL.Path == "/settings/backup/upload" || r.URL.Path == "/settings/backup/download" {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(6 * time.Hour))
			_ = rc.SetWriteDeadline(time.Now().Add(6 * time.Hour))
		}
		if r.URL.Path == "/settings/backup/upload" {
			if r.Header.Get("X-CSRF-Token") == "" || r.Header.Get("Content-Type") != "application/octet-stream" {
				http.Error(w, "Raw upload and CSRF header required", 400)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, (16<<30)+(32<<20))
		} else {
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		}
		csrf.ServeHTTP(w, r)
	})
}

// Only status and its static shell bypass admission and sessions. Other boundaries hold admission
// through authorization, reauthentication and the synchronous engine call. The
// engine starts exclusive work on an independent goroutine, never inline.
func (h *BackupHandler) controlRoutes(auth func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.CSRFToken)
	r.Use(middleware.Theme)
	r.Use(middleware.CurrentPath)
	r.Get("/settings/backup", h.page)
	r.Post("/settings/backup/create", h.create)
	r.Post("/settings/backup/upload", h.upload)
	r.Post("/settings/backup/restore", h.restore)
	r.Post("/settings/backup/download", h.download)
	return h.routeDispatch(r, auth)
}

func (h *BackupHandler) routeDispatch(routes http.Handler, auth func(http.Handler) http.Handler) http.Handler {
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, release, err := h.control.Enter(r.Context())
		if err != nil {
			if r.Method == http.MethodGet && r.URL.Path == "/settings/backup" {
				h.statusPage(w, r)
				return
			}
			w.Header().Set("Retry-After", "5")
			http.Error(w, instancecontrol.ErrMaintenance.Error(), http.StatusServiceUnavailable)
			return
		}
		defer release()
		r = r.WithContext(ctx)
		if err := h.refresh(); err != nil {
			http.Error(w, "Instance connections unavailable", 503)
			return
		}
		auth(middleware.AdminOnly(routes)).ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/settings/backup/status-page" {
			if r.Method != http.MethodGet {
				http.Error(w, "GET required", 405)
				return
			}
			h.statusPage(w, r)
			return
		}
		if r.URL.Path == "/settings/backup/status" {
			if r.Method != http.MethodPost {
				http.Error(w, "POST required", 405)
				return
			}
			id := r.Header.Get("X-Backup-Operation")
			if id == "" || r.URL.RawQuery != "" {
				http.Error(w, "Operation header required", 400)
				return
			}
			op, err := h.manager.StatusCapability(id)
			h.result(w, op, err)
			return
		}
		protected.ServeHTTP(w, r)
	})
}

func (h *BackupHandler) statusPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_ = templates.BackupStatusPage(nosurf.Token(r)).Render(r.Context(), w)
}

func (h *BackupHandler) page(w http.ResponseWriter, r *http.Request) {
	cs, err := h.settings.Get(r.Context())
	if err != nil {
		http.Error(w, "Settings unavailable", 503)
		return
	}
	render(w, r, templates.BackupPage(companyName(cs), h.disabledReason, h.control.EmailDisabled()))
}

func (h *BackupHandler) actor(w http.ResponseWriter, r *http.Request, reauthenticate bool) (*ent.User, bool) {
	if h.disabledReason != "" {
		http.Error(w, h.disabledReason, 503)
		return nil, false
	}
	info, ok := middleware.UserFromContext(r.Context())
	if !ok || info == nil {
		http.Error(w, "Forbidden", 403)
		return nil, false
	}
	u, err := h.users.GetByID(r.Context(), info.ID)
	if err != nil || !u.IsActive || u.Role != "admin" || u.ForcePasswordChange {
		http.Error(w, "Forbidden", 403)
		return nil, false
	}
	if reauthenticate && (r.PostFormValue("current_password") == "" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(r.PostFormValue("current_password"))) != nil) {
		http.Error(w, "Current account password is incorrect", 403)
		return nil, false
	}
	return u, true
}
func (h *BackupHandler) destination(w http.ResponseWriter, r *http.Request) (string, bool) {
	cs, err := h.settings.Get(r.Context())
	if err != nil {
		http.Error(w, "Settings unavailable", 503)
		return "", false
	}
	return companyName(cs), true
}
func (h *BackupHandler) create(w http.ResponseWriter, r *http.Request) {
	u, ok := h.actor(w, r, false)
	if !ok {
		return
	}
	name, ok := h.destination(w, r)
	if !ok {
		return
	}
	op, err := h.manager.StartBackup(u.ID, name, r.PostFormValue("archive_password"))
	h.result(w, op, err)
}
func (h *BackupHandler) upload(w http.ResponseWriter, r *http.Request) {
	u, ok := h.actor(w, r, false)
	if !ok {
		return
	}
	// JSON string encoding in the header preserves Unicode and whitespace passwords.
	var password string
	if err := json.Unmarshal([]byte(r.Header.Get("X-Archive-Password")), &password); err != nil || len(password) > 1024 {
		http.Error(w, "Archive password required", 400)
		return
	}
	op, err := h.manager.StageUpload(r.Context(), u.ID, r.Body, password)
	h.result(w, op, err)
}
func (h *BackupHandler) restore(w http.ResponseWriter, r *http.Request) {
	u, ok := h.actor(w, r, true)
	if !ok {
		return
	}
	name, ok := h.destination(w, r)
	if !ok {
		return
	}
	id := r.PostFormValue("operation")
	op, err := h.manager.Status(u.ID, id)
	if err != nil || op.Kind != "upload" || op.Phase != "ready" || op.ArchiveDigest == "" || op.ArchiveDigest != r.PostFormValue("archive_digest") {
		http.Error(w, "Archive review expired or does not match", 409)
		return
	}
	op, err = h.manager.StartRestore(r.Context(), u.ID, id, name, r.PostFormValue("confirmation"))
	h.result(w, op, err)
}
func (h *BackupHandler) download(w http.ResponseWriter, r *http.Request) {
	u, ok := h.actor(w, r, true)
	if !ok {
		return
	}
	f, _, size, err := h.manager.OpenDownload(u.ID, r.PostFormValue("operation"))
	if err != nil {
		h.result(w, backup.Operation{}, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="freefsm-backup.age"`)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	_, _ = io.Copy(w, f)
}
func (h *BackupHandler) result(w http.ResponseWriter, op backup.Operation, err error) {
	if err != nil {
		code, message := 400, "Backup operation unavailable; check input, storage and PostgreSQL prerequisites"
		if errors.Is(err, backup.ErrNotFound) {
			code, message = 404, "Operation not found or expired"
		}
		if errors.Is(err, instancecontrol.ErrOperationBusy) {
			code, message = 409, "Another instance operation is in progress"
		}
		http.Error(w, message, code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(op)
}
