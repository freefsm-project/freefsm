package handlers

import (
	"errors"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// renderFieldSaveError is a schema-free field-correction continuation. Domain
// input is retained verbatim, not reinterpreted as a second editable schema.
// A resubmit returns to the owning action and its ordinary validation. Submitted
// custom-field identities are rebuilt only from the authorized definitions.
func renderFieldSaveError(w http.ResponseWriter, r *http.Request, svc *services.CustomFieldDefinitionService, typ string, err error) bool {
	if typ == "project" && errors.Is(err, authorization.ErrConflict) {
		http.Error(w, "Project state changed or dependencies need repair; reload and review", http.StatusConflict)
		return true
	}
	if typ == "job" && errors.Is(err, authorization.ErrConflict) {
		http.Error(w, "Job state changed or dependencies need repair; reload and review", http.StatusConflict)
		return true
	}
	var validation *services.ProjectValidationError
	if errors.As(err, &validation) {
		http.Error(w, validation.Message, http.StatusBadRequest)
		return true
	}
	if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrPending) {
		authorizationError(w, r, err)
		return true
	}
	var fieldErr *customfields.FieldError
	if !errors.As(err, &fieldErr) {
		return false
	}
	defs, loadErr := svc.ListForObjectType(r.Context(), typ)
	if loadErr != nil {
		http.Error(w, "Unable to load fields", 500)
		return true
	}
	var details []templates.FormInput
	keys := make([]string, 0, len(r.PostForm))
	for key := range r.PostForm {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if key == "csrf_token" || strings.HasPrefix(key, "custom_field_") {
			continue
		}
		for _, value := range r.PostForm[key] {
			details = append(details, templates.FormInput{Name: key, Value: value})
		}
	}
	old := "[]"
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	// Job-bound document creation carries the Job ID in the URL, not a document ID.
	if strings.Contains(r.URL.Path, "/create-") || strings.HasSuffix(r.URL.Path, "/invoices") {
		id = 0
	}
	if request, ok := authorization.FromContext(r.Context()); ok && id != 0 {
		var err error
		old, err = request.Service.ReadFields(r.Context(), request.Actor, typ, id, false)
		if err != nil {
			authorizationError(w, r, err)
			return true
		}
	}
	w.WriteHeader(422)
	templates.FieldSaveError(r.URL.RequestURI(), fieldErr.Error(), buildCustomFieldErrorDisplay(defs, old, parseCustomFieldValues(r), id == 0), details).Render(r.Context(), w)
	return true
}
