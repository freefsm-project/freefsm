package handlers

import (
	"errors"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
)

func (h *CustomFieldHandler) Values(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h.values(w, r, typ) }
}

func (h *CustomFieldHandler) values(w http.ResponseWriter, r *http.Request, typ string) {
	request, ok := authorization.FromContext(r.Context())
	if !ok || request.Service == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	values, err := request.Service.ReadFields(r.Context(), request.Actor, typ, id, true)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	message := ""
	old := values
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		values = parseCustomFieldValues(r)
		_, err = request.Service.SaveFields(r.Context(), request.Actor, typ, id, values)
		if err == nil {
			http.Redirect(w, r, fmt.Sprintf("/%ss/%d/custom-fields", typ, id), 303)
			return
		}
		var fieldErr *customfields.FieldError
		if !errors.As(err, &fieldErr) {
			authorizationError(w, r, err)
			return
		}
		message = fieldErr.Error()
	}
	defs, err := h.svc.ListForObjectType(r.Context(), typ)
	if err != nil {
		http.Error(w, "Unable to load fields", 500)
		return
	}
	if message != "" {
		w.WriteHeader(422)
	}
	templates.ParentFieldsForm(typ, id, buildCustomFieldErrorDisplay(defs, old, values, false), message).Render(r.Context(), w)
}
