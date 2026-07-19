// Package v1 provides the version 1 JSON API for mobile clients.
// Its router uses paths relative to /api/v1 so callers can mount it at that prefix.
package v1

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const loginRateLimitWindow = time.Minute

type transportPeerContextKey struct{}

type SessionService interface {
	CreateMobile(context.Context, int64, string) (string, error)
	ValidateMobile(context.Context, string) (int64, error)
	RevokeMobile(context.Context, string) error
}

type userService interface {
	Authenticate(context.Context, string, string) error
	GetByEmail(context.Context, string) (*ent.User, error)
	GetByID(context.Context, int64) (*ent.User, error)
	CompleteOnboarding(context.Context, int64) error
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
	TransitionJob(context.Context, statusflow.Actor, int64, int64) error
}

type policyService interface {
	IsUserAssignedToJob(context.Context, int64, int64) bool
}

type Dependencies struct {
	Sessions    SessionService
	Users       userService
	Jobs        jobService
	Statuses    statusService
	TimeEntries timeEntryService
	Mutations   apiMutationService
	StatusFlow  statusFlowService
	Policy      policyService
}

// NewRouter constructs a router from existing FreeFSM services. Mount the returned
// handler with http.StripPrefix("/api/v1", ...) or chi.Mount("/api/v1", ...).
func NewRouter(db *pgxpool.Pool, client *ent.Client, sessions SessionService) http.Handler {
	objects := objectref.NewEntDirectory(client)
	return New(Dependencies{
		Sessions:    sessions,
		Users:       services.NewUserService(client),
		Jobs:        services.NewJobService(client),
		Statuses:    services.NewStatusService(client),
		TimeEntries: services.NewTimeEntryService(client),
		Mutations:   services.NewAPIMutationService(db),
		StatusFlow:  statusflow.New(db),
		Policy:      services.NewPolicyService(client, objects),
	})
}

// New constructs the API using injectable service seams.
func New(deps Dependencies) http.Handler {
	h := &handler{deps: deps}
	r := chi.NewRouter()
	r.Use(apiRecoverer)
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "API endpoint not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	loginLimiter := middleware.NewRateLimiter(5, loginRateLimitWindow)
	r.With(loginRateLimit(loginLimiter)).Post("/session", h.login)
	r.Group(func(r chi.Router) {
		r.Use(h.bearerAuth)
		r.Delete("/session", h.logout)
		r.Get("/me", h.me)
		r.Get("/jobs", h.listJobs)
		r.Get("/jobs/{id}", h.getJob)
		r.Post("/jobs/{id}/status", h.updateJobStatus)
		r.Get("/time-entries/active", h.activeTimeEntry)
		r.Post("/jobs/{id}/clock-in", h.clockIn)
		r.Post("/time-entries/clock-out", h.clockOut)
		r.Patch("/jobs/{id}/subtasks/{index}", h.updateSubtask)
	})
	return r
}

type handler struct{ deps Dependencies }

// CaptureTransportPeer preserves the network peer address before RealIP rewrites
// RemoteAddr for application-level proxy handling.
func CaptureTransportPeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), transportPeerContextKey{}, r.RemoteAddr)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func loginRateLimit(limiter *middleware.RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limitedRequest := r.Clone(r.Context())
			if peer, ok := r.Context().Value(transportPeerContextKey{}).(string); ok && peer != "" {
				if !isLoopbackPeer(peer) {
					limitedRequest.RemoteAddr = peer
				}
			}
			if !limiter.Allow(limitedRequest) {
				w.Header().Set("Retry-After", strconv.Itoa(int(loginRateLimitWindow/time.Second)))
				writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "too many login attempts")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isLoopbackPeer(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
