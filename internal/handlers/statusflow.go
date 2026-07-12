package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/go-chi/chi/v5"
)

func statusflowHTTPError(w http.ResponseWriter, err error) {
	code := http.StatusUnprocessableEntity
	switch {
	case errors.Is(err, statusflow.ErrForbidden):
		code = http.StatusForbidden
	case errors.Is(err, statusflow.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, statusflow.ErrWrongType), errors.Is(err, statusflow.ErrPaymentDerived), errors.Is(err, statusflow.ErrInvalidInput):
		code = http.StatusBadRequest
	case errors.Is(err, statusflow.ErrActiveSettlement), errors.Is(err, statusflow.ErrReplacementRequired), errors.Is(err, statusflow.ErrConfirmationRequired):
		code = http.StatusConflict
	case errors.Is(err, statusflow.ErrInvalidTransition):
		code = http.StatusConflict
	}
	http.Error(w, err.Error(), code)
}

func transitionStatus(svc *statusflow.Service, typ statusflow.ObjectType) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		statusID, err := strconv.ParseInt(r.FormValue("status_id"), 10, 64)
		if err != nil || statusID <= 0 {
			http.Error(w, "invalid status", http.StatusBadRequest)
			return
		}
		u, ok := middleware.UserFromContext(r.Context())
		if !ok || u == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		a := statusflow.Actor{ID: u.ID, CompanyID: u.CompanyID, Role: u.Role}
		switch typ {
		case statusflow.Job:
			err = svc.TransitionJob(r.Context(), a, id, statusID)
		case statusflow.Project:
			err = svc.TransitionProject(r.Context(), a, id, statusID)
		case statusflow.Estimate:
			err = svc.TransitionEstimate(r.Context(), a, id, statusID)
		case statusflow.Invoice:
			err = svc.TransitionInvoice(r.Context(), a, id, statusID)
		}
		if err != nil {
			statusflowHTTPError(w, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/%ss/%d?flash=Status+updated", typ, id), http.StatusSeeOther)
	}
}
