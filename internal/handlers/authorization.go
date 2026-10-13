package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/credentials"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func authorizationActor(r *http.Request) authorization.Actor {
	u, _ := middleware.UserFromContext(r.Context())
	a := authorization.Actor{Channel: "web"}
	if u != nil {
		a.UserID = u.ID
	}
	if c, e := r.Cookie("session"); e == nil {
		sum := sha256.Sum256([]byte(c.Value))
		a.SessionHash = hex.EncodeToString(sum[:])
	}
	return a
}
func authorizationError(w http.ResponseWriter, r *http.Request, err error) {
	code := http.StatusConflict
	message := "Request rejected; review the current state and supplied grants"
	switch {
	case errors.Is(err, saveaction.ErrRevisionRequired):
		code = http.StatusPreconditionRequired
		message = err.Error()
	case errors.Is(err, authorization.ErrUnavailable):
		middleware.ServiceUnavailable(w, r)
		return
	case errors.Is(err, saveaction.ErrStale):
		message = err.Error()
	case errors.Is(err, authorization.ErrGeocoder):
		code = 503
		message = err.Error()
	case errors.Is(err, credentials.ErrKey), errors.Is(err, credentials.ErrReview):
		code = 503
		message = err.Error()
	case errors.Is(err, authorization.ErrNotFound):
		code = 404
		message = "Not found"
	case errors.Is(err, authorization.ErrForbidden):
		code = 403
		message = "Forbidden"
	case errors.Is(err, authorization.ErrPending):
		code = 503
		message = "Customer authorization is pending Administrator review"
	case errors.Is(err, authorization.ErrConfigurationUnavailable):
		code = 503
		message = "Company configuration is unavailable"
	default:
		if r.Method == http.MethodGet && !errors.Is(err, authorization.ErrConflict) {
			slog.Error("authorized read unavailable", "path", r.URL.Path, "error", err)
			middleware.ServiceUnavailable(w, r)
			return
		}
	}
	slog.Warn("authorization request rejected", "actor_id", authorizationActor(r).UserID, "path", r.URL.Path, "channel", "web", "status", code)
	http.Error(w, message, code)
}

func bindAuthorization(s *authorization.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a := authorizationActor(r)
			r = r.WithContext(authorization.WithActor(r.Context(), s, a))
			if strings.HasPrefix(r.URL.Path, "/settings/authorization") {
				if _, err := s.Administrator(r.Context(), a); err != nil {
					authorizationError(w, r, err)
					return
				}
			}
			// All Customer routes, including legacy child editors and activity endpoints,
			// pass through the migrated View boundary before any resource details load.
			if r.URL.Path == "/customers" || strings.HasPrefix(r.URL.Path, "/customers/") {
				p, err := s.Principal(r.Context(), a)
				if err != nil {
					authorizationError(w, r, err)
					return
				}
				parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
				// Contacts and Locations are independently authorized resources.
				// Their handlers authorize child visibility and selected inputs;
				// a nested URL does not require or grant Customer master access.
				if len(parts) >= 3 && (parts[2] == "contacts" || parts[2] == "locations") {
					next.ServeHTTP(w, r)
					return
				}
				permission := "customer.view"
				if len(parts) == 1 && r.Method == http.MethodPost || len(parts) == 2 && parts[1] == "new" {
					permission = "customer.create"
				}
				if len(parts) > 1 {
					if id, e := strconv.ParseInt(parts[1], 10, 64); e == nil {
						if len(parts) == 2 && r.Method == http.MethodPost || len(parts) == 3 && parts[2] == "edit" {
							permission = "customer.edit"
						}
						if len(parts) > 2 {
							switch parts[2] {
							case "delete":
								permission = "customer.archive"
							case "restore":
								permission = "customer.restore"
							case "credit":
								permission = "customer.financial.view"
							}
						}
						if err := s.CustomerAccess(r.Context(), a, id, permission); err != nil {
							authorizationError(w, r, err)
							return
						}
					} else if !p.Allows(permission, authorization.All) {
						authorizationError(w, r, authorization.ErrForbidden)
						return
					}
				} else if !p.Allows(permission, authorization.All) {
					authorizationError(w, r, authorization.ErrForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

type authorizationHandler struct{ svc *authorization.Service }

func (h authorizationHandler) AccountSecurity(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	action := "user.sessions.revoke"
	if strings.HasSuffix(r.URL.Path, "/require-change") {
		action = "user.password.require_change"
	}
	if err := h.svc.AccountSecurity(r.Context(), authorizationActor(r), id, action); err != nil {
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, "/users/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (h authorizationHandler) Directory(w http.ResponseWriter, r *http.Request) {
	people, err := h.svc.Directory(r.Context(), authorizationActor(r))
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(people)
		return
	}
	templates.Directory(people).Render(r.Context(), w)

}

func (h authorizationHandler) WebReceipt(w http.ResponseWriter, r *http.Request) {
	receipt, err := h.svc.WebReceipt(r.Context(), authorizationActor(r), chi.URLParam(r, "operation"))
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(receipt)
}

func (h authorizationHandler) Show(w http.ResponseWriter, r *http.Request) {
	review, err := h.svc.Review(r.Context(), authorizationActor(r))
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(review)
		return
	}
	templates.AuthorizationReview(review).Render(r.Context(), w)
}
func (h authorizationHandler) SaveRole(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	var grants []authorization.Grant
	if r.FormValue("catalog_form") == "true" {
		for _, value := range r.Form["grant"] {
			permission, scope, ok := strings.Cut(value, ":")
			if !ok {
				http.Error(w, "Invalid grant", 400)
				return
			}
			grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.Scope(scope)})
		}
	} else {
		if !strings.HasPrefix(strings.TrimSpace(r.FormValue("grants")), "[") {
			http.Error(w, "Grants must be an explicit JSON array", 400)
			return
		}
		decoder := json.NewDecoder(strings.NewReader(r.FormValue("grants")))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&grants); err != nil {
			http.Error(w, "Grants must be an explicit JSON array", 400)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			http.Error(w, "Unexpected trailing JSON", 400)
			return
		}
	}
	var id int64
	if raw := r.FormValue("role_id"); raw != "" {
		var err error
		id, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "Invalid Role identifier", http.StatusBadRequest)
			return
		}
	}
	revision, _ := strconv.ParseInt(r.FormValue("role_revision"), 10, 64)
	_, err := h.svc.SaveRole(r.Context(), authorizationActor(r), id, r.FormValue("name"), grants, revision)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings/authorization", 303)
}
func (h authorizationHandler) Assign(w http.ResponseWriter, r *http.Request) {
	userID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	roleID, _ := strconv.ParseInt(r.FormValue("role_id"), 10, 64)
	var err error
	if r.FormValue("review") == "true" {
		err = h.svc.ApproveUser(r.Context(), authorizationActor(r), userID, roleID, r.FormValue("snapshot"))
	} else {
		err = h.svc.AssignUser(r.Context(), authorizationActor(r), userID, roleID)
	}
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings/authorization", 303)
}
func (h authorizationHandler) Cutover(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("confirmation") != "APPROVE CUTOVER" {
		http.Error(w, "Explicit confirmation required", 400)
		return
	}
	if err := h.svc.Cutover(r.Context(), authorizationActor(r)); err != nil {
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings/authorization", 303)
}

func (h authorizationHandler) RemoveRole(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.FormValue("role_id"), 10, 64)
	replacement, _ := strconv.ParseInt(r.FormValue("replacement_id"), 10, 64)
	if err := h.svc.RemoveRole(r.Context(), authorizationActor(r), id, replacement); err != nil {
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings/authorization", 303)
}
func (h authorizationHandler) Context(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	c, err := h.svc.JobCustomer(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(c)
		return
	}
	masters, err := h.svc.JobMasters(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	templates.CustomerJobContext(c, masters).Render(r.Context(), w)
}

func (h authorizationHandler) MasterContext(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	c, err := h.svc.JobMasters(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(c)
		return
	}
	templates.JobMasterContext(c).Render(r.Context(), w)
}

func customerPermission(r *http.Request, permission string) bool {
	return allRecordsPermission(r, permission)
}

func allRecordsPermission(r *http.Request, permission string) bool {
	request, ok := authorization.FromContext(r.Context())
	if !ok {
		return false
	}
	p, err := request.Service.Principal(r.Context(), request.Actor)
	return err == nil && p.Allows(permission, authorization.All)
}

func customerInput(r *http.Request) authorization.CustomerInput {
	field := func(name string) *string {
		if !r.Form.Has(name) {
			return nil
		}
		s := r.Form.Get(name)
		return &s
	}
	in := authorization.CustomerInput{FirstName: field("first_name"), LastName: field("last_name"), DisplayName: field("display_name"), Email: field("email"), Phone: field("phone"), CompanyName: field("company_name"), Notes: field("notes"), ServiceNotes: field("service_notes"), AccountType: field("account_type"), BillingAddress1: field("billing_address_1"), BillingAddress2: field("billing_address_2"), BillingCity: field("billing_city"), BillingState: field("billing_state"), BillingZipCode: field("billing_zip_code")}
	in.Status = field("status")
	if r.Form.Has("tax_exempt") || r.Form.Has("tax_exempt_present") {
		v := r.Form.Get("tax_exempt") == "true"
		in.TaxExempt = &v
	}
	for key := range r.Form {
		if strings.HasPrefix(key, "custom_field_") {
			v := parseCustomFieldValues(r)
			in.CustomFields = &v
			break
		}
	}
	return in
}
