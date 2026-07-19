package v1

import (
	"context"
	"net/http"
	"strings"

	"github.com/freefsm-project/freefsm/internal/ent"
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

type loginRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	DeviceName string `json:"device_name"`
}

type sessionDTO struct {
	Token string  `json:"token"`
	User  userDTO `json:"user"`
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Email = strings.TrimSpace(request.Email)
	if request.Email == "" || request.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "email and password are required")
		return
	}
	if err := h.deps.Users.Authenticate(r.Context(), request.Email, request.Password); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	user, err := h.deps.Users.GetByEmail(r.Context(), request.Email)
	if err != nil || !validUser(user) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	if user.ForcePasswordChange {
		passwordChangeRequired(w)
		return
	}
	token, err := h.deps.Sessions.CreateMobile(r.Context(), user.ID, strings.TrimSpace(request.DeviceName))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create session")
		return
	}
	if err := h.deps.Users.CompleteOnboarding(r.Context(), user.ID); err != nil {
		_ = h.deps.Sessions.RevokeMobile(context.WithoutCancel(r.Context()), token)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not complete login")
		return
	}
	writeJSON(w, http.StatusCreated, sessionDTO{Token: token, User: makeUserDTO(user)})
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
			unauthorized(w)
			return
		}
		user, err := h.deps.Users.GetByID(r.Context(), userID)
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
		ctx = context.WithValue(ctx, tokenContextKey{}, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.Sessions.RevokeMobile(r.Context(), tokenFromContext(r.Context())); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not delete session")
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
