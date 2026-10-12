package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justinas/nosurf"
	"golang.org/x/crypto/bcrypt"
)

type backupEngine interface {
	StartBackupFor(backup.ActivityActor, string, string) (backup.Operation, error)
	StageUploadFor(context.Context, backup.ActivityActor, io.Reader, string) (backup.Operation, error)
	StartRestoreFor(context.Context, backup.ActivityActor, string, string, string) (backup.Operation, error)
	RecordDownload(context.Context, backup.ActivityActor, string) error
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

type backupInitiations interface {
	StartBackupAction(backup.ActivityActor, string, string, string) (backup.Operation, error)
	StageUploadAction(context.Context, backup.ActivityActor, string, io.Reader, string) (backup.Operation, error)
	InitiationStatus(string) (backup.InitiationReceipt, error)
}

type BackupHandler struct {
	manager        backupEngine
	control        *instancecontrol.Control
	refresh        func() error
	users          backupUsers
	settings       backupSettings
	activity       *ActivityHandler
	disabledReason string
}

func NewBackupRouter(db *pgxpool.Pool, manager *backup.Manager, control *instancecontrol.Control, users *services.UserService, settings *services.CompanySettingsService, sessions *services.SessionService, disabledReason string, activity *ActivityHandler) http.Handler {
	h := &BackupHandler{manager: manager, control: control, refresh: manager.RefreshConnections, users: users, settings: settings, disabledReason: disabledReason, activity: activity}
	authority := authorization.New(db)
	authenticate := middleware.Auth(sessions, func(ctx context.Context, id int64) (*middleware.UserInfo, error) {
		u, err := users.GetByID(ctx, id)
		if err != nil {
			if ent.IsNotFound(err) {
				return nil, services.ErrInvalidSession
			}
			return nil, err
		}
		if u == nil || !u.IsActive || u.ForcePasswordChange || u.CompanyID == nil || *u.CompanyID <= 0 {
			return nil, services.ErrInvalidSession
		}
		p, authErr := authority.Administrator(ctx, authorization.Actor{UserID: id})
		if authErr != nil && !errors.Is(authErr, authorization.ErrForbidden) && !errors.Is(authErr, authorization.ErrPending) {
			return nil, authErr
		}
		info := &middleware.UserInfo{ID: u.ID, CompanyID: *u.CompanyID, Name: u.Name, Email: u.Email, Role: u.Role, FontSize: u.FontSize, AuthorizationLoaded: true, AuthorizationReady: p.Ready, ProtectedAdministrator: authErr == nil && p.Protected && p.Ready, CanReviewAuthorization: authErr == nil && p.Protected, NavigationGrants: map[string]bool{}}
		info.DirectoryView = p.Allows("directory.view", authorization.All)
		info.ConfigurationView = p.Allows("configuration.company.view", authorization.Configuration) || p.Allows("configuration.documents.view", authorization.Configuration)
		info.MailSettingsView = p.Allows("configuration.mail.view", authorization.Configuration)
		info.MapSettingsView = p.Allows("configuration.maps.view", authorization.Configuration)
		if p.Ready {
			for _, grant := range p.Grants {
				info.NavigationGrants[grant.Permission+"/"+string(grant.Scope)] = true
			}
		}
		return info, nil
	})
	// Only authenticated controls load presentation. The capability/status shell
	// bypasses this provider entirely, including when the database is unavailable.
	auth := func(next http.Handler) http.Handler { return authenticate(presentationContext(authority)(next)) }
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
	r.Get("/settings/backup/activity", h.listActivity)
	r.Post("/settings/backup/create", h.create)
	r.Post("/settings/backup/upload", h.upload)
	r.Post("/settings/backup/restore", h.restore)
	r.Post("/settings/backup/download", h.download)
	return h.routeDispatch(r, auth)
}

func (h *BackupHandler) listActivity(w http.ResponseWriter, r *http.Request) {
	if h.activity == nil {
		http.Error(w, "Activity unavailable", http.StatusServiceUnavailable)
		return
	}
	h.activity.ListByType(objectref.TypeInstance)(w, r)
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
		if r.URL.Path == "/settings/backup/action-status" {
			if r.Method != http.MethodPost {
				http.Error(w, "POST required", 405)
				return
			}
			m, ok := h.manager.(backupInitiations)
			if !ok {
				http.Error(w, "Initiation status unavailable", 503)
				return
			}
			receipt, err := m.InitiationStatus(r.Header.Get("X-Backup-Action"))
			if err != nil {
				code := http.StatusServiceUnavailable
				if errors.Is(err, backup.ErrInvalid) {
					code = http.StatusBadRequest
				}
				http.Error(w, "Initiation status could not be checked. Retain this capability and check again; do not initiate a replacement.", code)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(receipt)
			return
		}
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
	if err != nil || u == nil || !u.IsActive || u.AuthorizationRoleID == nil || *u.AuthorizationRoleID != 1 || u.ForcePasswordChange || u.CompanyID == nil || *u.CompanyID <= 0 {
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
	var op backup.Operation
	var err error
	if id := r.Header.Get("X-Backup-Action"); id != "" {
		m, ok := h.manager.(backupInitiations)
		if !ok {
			http.Error(w, "Initiation boundary unavailable", 503)
			return
		}
		op, err = m.StartBackupAction(backupActorSnapshot(u, name), id, name, r.PostFormValue("archive_password"))
		if h.initiationDuplicate(w, m, id, err) {
			return
		}
	} else {
		op, err = h.manager.StartBackupFor(backupActorSnapshot(u, name), name, r.PostFormValue("archive_password"))
	}
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
	name, ok := h.destination(w, r)
	if !ok {
		return
	}
	var op backup.Operation
	var err error
	if id := r.Header.Get("X-Backup-Action"); id != "" {
		m, ok := h.manager.(backupInitiations)
		if !ok {
			http.Error(w, "Initiation boundary unavailable", 503)
			return
		}
		op, err = m.StageUploadAction(r.Context(), backupActorSnapshot(u, name), id, r.Body, password)
		if h.initiationDuplicate(w, m, id, err) {
			return
		}
	} else {
		op, err = h.manager.StageUploadFor(r.Context(), backupActorSnapshot(u, name), r.Body, password)
	}
	h.result(w, op, err)
}

func (h *BackupHandler) initiationDuplicate(w http.ResponseWriter, m backupInitiations, id string, err error) bool {
	if !errors.Is(err, backup.ErrInitiated) {
		return false
	}
	receipt, lookupErr := m.InitiationStatus(id)
	if lookupErr != nil {
		http.Error(w, "The action was already admitted. Check its same capability again; do not initiate a replacement.", 503)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(receipt)
	return true
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
	op, err = h.manager.StartRestoreFor(r.Context(), backupActorSnapshot(u, name), id, name, r.PostFormValue("confirmation"))
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
	name, ok := h.destination(w, r)
	if !ok {
		return
	}
	if err := h.manager.RecordDownload(r.Context(), backupActorSnapshot(u, name), r.PostFormValue("operation")); err != nil {
		http.Error(w, "Could not record backup download activity", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="freefsm-backup.age"`)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	_, _ = io.Copy(w, f)
}

func backupActorSnapshot(u *ent.User, companyName string) backup.ActivityActor {
	return backup.ActivityActor{ID: u.ID, CompanyID: *u.CompanyID, Name: u.Name, CompanyName: companyName}
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
