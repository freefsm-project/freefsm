package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

type actorContextKey struct{}
type tokenContextKey struct{}

type userDTO struct {
	ID        int64  `json:"id"`
	CompanyID int64  `json:"company_id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Role      string `json:"role"`
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "pairing_required", "approve this device in the hosted browser; API password login is retired")
}

func (h *handler) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			unauthorized(w)
			return
		}
		userID, err := h.deps.Sessions.ValidateMobile(r.Context(), token)
		if err != nil {
			if !errors.Is(err, services.ErrInvalidSession) && h.deps.Authorization != nil {
				writeError(w, 503, "unavailable", "session verification unavailable")
				return
			}
			unauthorized(w)
			return
		}
		user, err := h.deps.Users.GetByID(r.Context(), userID)
		if err != nil && !ent.IsNotFound(err) && h.deps.Authorization != nil {
			writeError(w, 503, "unavailable", "account verification unavailable")
			return
		}
		if err != nil || user.ID != userID || !validUser(user) {
			unauthorized(w)
			return
		}
		if user.ForcePasswordChange {
			_ = h.deps.Sessions.RevokeMobile(context.WithoutCancel(r.Context()), token)
			passwordChangeRequired(w)
			return
		}
		ctx := context.WithValue(r.Context(), actorContextKey{}, user)
		if epoch, ok := r.Context().Value(sessionEpochKey{}).(string); ok {
			w.Header().Set("X-FreeFSM-Session-Epoch", epoch)
		}
		ctx = context.WithValue(ctx, tokenContextKey{}, token)
		if h.deps.Authorization != nil {
			sum := sha256.Sum256([]byte(token))
			ctx = authorization.WithActor(ctx, h.deps.Authorization, authorization.Actor{UserID: user.ID, SessionHash: hex.EncodeToString(sum[:]), Channel: "api"})
		}
		response := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(response, r.WithContext(ctx))
		if response.Status() >= 200 && response.Status() < 300 && h.deps.Authorization != nil {
			sum := sha256.Sum256([]byte(token))
			if err := h.deps.Authorization.RenewMobile(context.WithoutCancel(ctx), authorization.Actor{UserID: user.ID, SessionHash: hex.EncodeToString(sum[:]), Channel: "api"}); err != nil && !errors.Is(err, authorization.ErrForbidden) {
				slog.Error("mobile authorization renewal failed", "user_id", user.ID)
			}
		}
	})
}

func (h *handler) customerContext(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 404, "not_found", "not found")
		return
	}
	id, err := resolveJobRoute(r.Context(), req.Service, chi.URLParam(r, "id"))
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	c, err := req.Service.JobCustomer(r.Context(), req.Actor, id)
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *handler) masterContext(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 404, "not_found", "not found")
		return
	}
	id, err := resolveJobRoute(r.Context(), req.Service, chi.URLParam(r, "id"))
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	c, err := req.Service.JobMasters(r.Context(), req.Actor, id)
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	operation, ok := securityOperationID(w, r)
	if !ok {
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 503, "unavailable", "session management unavailable")
		return
	}
	if _, err := req.Service.SubmitSessionRevocation(r.Context(), req.Actor, operation, "logout", 0); err != nil {
		sessionError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, makeUserDTO(actorFromContext(r.Context())))
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	returnToken := ""
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		returnToken = parts[1]
	}
	return returnToken, returnToken != ""
}

func validUser(user *ent.User) bool {
	return user != nil && user.ID > 0 && user.CompanyID != nil && *user.CompanyID > 0 && user.IsActive
}

func makeUserDTO(user *ent.User) userDTO {
	return userDTO{ID: user.ID, CompanyID: *user.CompanyID, Name: user.Name, Email: user.Email, Role: user.Role}
}

func actorFromContext(ctx context.Context) *ent.User {
	user, _ := ctx.Value(actorContextKey{}).(*ent.User)
	return user
}

func tokenFromContext(ctx context.Context) string {
	token, _ := ctx.Value(tokenContextKey{}).(string)
	return token
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="freefsm-api"`)
	writeError(w, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
}

func passwordChangeRequired(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "password_change_required", "password change is required before using the mobile API")
}
