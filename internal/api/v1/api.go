// Package v1 provides the version 1 JSON API for mobile clients.
// Its router uses paths relative to /api/v1 so callers can mount it at that prefix.
package v1

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/requestlimits"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SessionService interface {
	ValidateMobile(context.Context, string) (int64, error)
	RevokeMobile(context.Context, string) error
}

type userService interface {
	GetByID(context.Context, int64) (*ent.User, error)
}

type jobService interface {
	ListAllForCompany(context.Context, int64) ([]*ent.Job, error)
	ListAssignedAllForCompany(context.Context, int64, int64) ([]*ent.Job, error)
	GetByIDForCompany(context.Context, int64, int64) (*ent.Job, error)
}

type statusService interface {
	ByObjectTypeForCompany(context.Context, int64, string) ([]*ent.Status, error)
}

type timeEntryService interface {
	GetActiveByUserForCompany(context.Context, int64, int64) (*ent.TimeEntry, error)
}

type apiMutationService interface {
	SetSubtaskCompletion(context.Context, services.MutationActor, int64, int, bool) (services.APISubtaskResult, error)
	ClockIn(context.Context, services.MutationActor, int64, services.APIClockInParams) (services.APITimeEntryResult, error)
	ClockOut(context.Context, services.MutationActor) (services.APITimeEntryResult, error)
}

type statusFlowService interface {
	DiscoverIntents(context.Context, statusflow.Actor, statusflow.ObjectType, int64) ([]statusflow.IntentChoice, error)
	ApplyIntent(context.Context, statusflow.Actor, statusflow.ObjectType, int64, statusflow.Intent) error
}

type policyService interface {
	IsUserAssignedToJob(context.Context, int64, int64) bool
}

type Dependencies struct {
	Limits        *requestlimits.Limiter
	Config        config.APIConfig
	Files         *services.FileService
	Authorization *authorization.Service
	Sessions      SessionService
	Users         userService
	Jobs          jobService
	Statuses      statusService
	TimeEntries   timeEntryService
	Mutations     apiMutationService
	StatusFlow    statusFlowService
	Policy        policyService
}

// NewRouter constructs a router from existing FreeFSM services. Mount the returned
// handler with http.StripPrefix("/api/v1", ...) or chi.Mount("/api/v1", ...).
func NewRouter(db *pgxpool.Pool, client *ent.Client, sessions SessionService, files ...*services.FileService) http.Handler {
	return NewConfiguredRouter(db, client, sessions, config.APIConfig{}, files...)
}

func NewConfiguredRouter(db *pgxpool.Pool, client *ent.Client, sessions SessionService, policy config.APIConfig, files ...*services.FileService) http.Handler {
	return NewRouterWithLimits(db, client, sessions, policy, nil, files...)
}

func NewRouterWithLimits(db *pgxpool.Pool, client *ent.Client, sessions SessionService, policy config.APIConfig, limits *requestlimits.Limiter, files ...*services.FileService) http.Handler {
	authorization.InstallCustomerBoundary(client)
	objects := objectref.NewEntDirectory(client)
	var fileService *services.FileService
	if len(files) > 0 {
		fileService = files[0]
	}
	return New(Dependencies{
		Limits:        limits,
		Config:        policy,
		Files:         fileService,
		Authorization: authorization.New(db),
		Sessions:      sessions,
		Users:         services.NewUserService(client),
		Jobs:          services.NewJobService(client),
		Statuses:      services.NewStatusService(client),
		TimeEntries:   services.NewTimeEntryService(client),
		Mutations:     services.NewAPIMutationService(db),
		StatusFlow:    statusflow.New(db),
		Policy:        services.NewPolicyService(client, objects),
	})
}

// New constructs the API using injectable service seams.
func New(deps Dependencies) http.Handler {
	deps.Config = deps.Config.Defaults()
	if err := deps.Config.Validate(); err != nil {
		panic(err)
	}
	if deps.Limits == nil {
		deps.Limits = requestlimits.New()
	}
	h := &handler{deps: deps, limits: deps.Limits}
	r := chi.NewRouter()
	r.Use(apiRecoverer)
	r.Use(h.transportPolicy)
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	r.With(h.authenticationBudget).Post("/session", h.login)
	r.With(h.authenticationBudget).Post("/pairings", h.startPairing)
	r.With(h.authenticationBudget).Post("/pairings/redeem", h.redeemPairing)
	r.With(h.authenticationBudget).Post("/pairings/cancel", h.cancelPairing)
	r.Get("/openapi.json", referenceFile("openapi.json", "application/json"))
	r.Get("/docs", referenceFile("docs.html", "text/html; charset=utf-8"))
	r.Get("/reference", referenceFile("reference.html", "text/html; charset=utf-8"))
	r.Get("/reference.js", referenceFile("reference.js", "text/javascript; charset=utf-8"))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"api_major": 1, "server_time": time.Now().UTC(), "pairing": "/api/v1/pairings", "recovery": "/forgot-password", "documentation": "/api/v1/docs"})
	})
	r.Group(func(r chi.Router) {
		r.Use(h.sessionEpoch)
		r.Use(h.bearerAuth)
		r.Use(h.authorizedBudget)
		r.Get("/capabilities", h.capabilities)
		// Synchronization routes remain within this authenticated budget boundary.
		r.HandleFunc("/sync", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, 404, "not_found", "synchronization endpoint not available")
		})
		r.HandleFunc("/sync/*", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, 404, "not_found", "synchronization endpoint not available")
		})
		r.Delete("/session", h.logout)
		r.Get("/me", h.me)
		r.Get("/sessions", h.sessions)
		r.Delete("/sessions", h.sessions)
		r.Delete("/sessions/{sessionID}", h.sessions)
		r.Get("/items", h.itemReferences)
		r.Get("/items/{id}", h.itemReferences)
		r.Get("/items/{id}/selection", h.itemReferences)
		for path, typ := range map[string]objectref.Type{"/jobs": objectref.TypeJob, "/estimates": objectref.TypeEstimate, "/invoices": objectref.TypeInvoice} {
			r.Get(path+"/{id}/activity", h.activity(string(typ)))
			r.Get(path+"/{id}/comments", h.comments(typ, false))
			r.Post(path+"/{id}/comments", h.comments(typ, false))
			r.Post(path+"/{id}/comments/{cid}/remove", h.comments(typ, true))
		}
		r.Get("/comment-operations/{operation}", h.commentReceipt)
		if deps.Files != nil {
			r.Get("/files/limits", h.fileLimits)
			r.Post("/file-transfers", h.startFileTransfer)
			r.Get("/file-transfers/{transfer}", h.getFileTransfer)
			r.Put("/file-transfers/{transfer}/chunks", h.putFileChunk)
			r.Post("/file-transfers/{transfer}/publish", h.publishFileTransfer)
			r.Get("/files/{id}", h.downloadFile)
			r.Get("/files/{id}/metadata", h.fileMetadata)
			r.Post("/files/{id}/rename", h.renameFile)
			r.Post("/files/{id}/remove", h.removeFile)
		}
		r.Get("/jobs", h.listJobs)
		r.Get("/jobs/{id}", h.getJob)
		r.Get("/jobs/{id}/custom-fields", h.jobFields)
		r.Patch("/jobs/{id}/custom-fields", h.jobFields)
		r.Get("/jobs/{id}/customer-context", h.customerContext)
		r.Get("/jobs/{id}/master-context", h.masterContext)
		r.Post("/jobs/{id}/status", h.updateJobStatus)
		r.Get("/jobs/{id}/status-intents", h.jobStatusIntents)
		r.Get("/time-entries/active", h.activeTimeEntry)
		r.Post("/jobs/{id}/clock-in", h.clockIn)
		r.Post("/time-entries/clock-out", h.clockOut)
		r.Patch("/jobs/{id}/subtasks/{index}", h.updateSubtask)
	})
	return r
}

type handler struct {
	deps   Dependencies
	limits *requestlimits.Limiter
}

// CaptureTransportPeer preserves the network peer address before RealIP rewrites
// RemoteAddr for application-level proxy handling.
func CaptureTransportPeer(next http.Handler) http.Handler {
	return requestlimits.CaptureTransportPeer(next)
}

func apiRecoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				slog.Error("API request panic", "panic", recovered, "path", r.URL.Path, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
