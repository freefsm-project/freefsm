package v1

import (
	"errors"
	"net/http"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/go-chi/chi/v5"
)

func serviceEntryAPIError(w http.ResponseWriter, err error) {
	status, code := 503, "retryable_failure"
	switch {
	case errors.Is(err, authorization.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, authorization.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, authorization.ErrConflict):
		status, code = 409, "service_entry_conflict"
	}
	writeError(w, status, code, code)
}

func (h *handler) serviceEntries(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "Service Entry requests have no query parameters")
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 503, "unavailable", "authorization unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	job, err := resolveJobRoute(r.Context(), req.Service, chi.URLParam(r, "id"))
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	asset, err := req.Service.ResolveServiceEntryReference(r.Context(), "asset", chi.URLParam(r, "assetID"))
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		page, err := req.Service.FieldServiceEntryPage(r.Context(), req.Actor, job, asset)
		if err != nil {
			serviceEntryAPIError(w, err)
			return
		}
		writeJSON(w, 200, struct {
			Entries []authorization.ServiceEntryFieldRecord `json:"entries"`
			LinkID  int64                                   `json:"link_id"`
		}{page.FieldEntries(), page.LinkID})
		return
	}
	var body struct {
		LinkID           int64      `json:"link_id"`
		OccurredAt       *time.Time `json:"occurred_at,omitempty"`
		OperationID      string     `json:"operation_id"`
		Revision         string     `json:"revision,omitempty"`
		ServiceDate      string     `json:"service_date"`
		Summary          string     `json:"summary"`
		ResponsibleUsers []int64    `json:"responsible_user_ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	in := authorization.ServiceEntryCommand{OperationID: body.OperationID, JobID: job, AssetID: asset, Revision: body.Revision, Content: authorization.ServiceEntryInput{ServiceDate: body.ServiceDate, Summary: body.Summary, ResponsibleUsers: body.ResponsibleUsers}}
	in.OccurredAt = body.OccurredAt
	in.Content.LinkID = body.LinkID
	if target := chi.URLParam(r, "entryID"); target != "" {
		in.EntryID, err = req.Service.ResolveServiceEntryReference(r.Context(), "service_entries", target)
		if err != nil {
			serviceEntryAPIError(w, err)
			return
		}
	}
	receipt, err := req.Service.SubmitServiceEntry(r.Context(), req.Actor, in)
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	writeJSON(w, 200, receipt)
}

func (h *handler) serviceEntryReceipt(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "Service Entry receipts have no query parameters")
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 503, "unavailable", "authorization unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	receipt, err := req.Service.ServiceEntryReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	writeJSON(w, 200, receipt)
}

func (h *handler) serviceEntryRevisions(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "Service Entry revisions have no query parameters")
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 503, "unavailable", "authorization unavailable")
		return
	}
	job, err := resolveJobRoute(r.Context(), req.Service, chi.URLParam(r, "id"))
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	asset, err := req.Service.ResolveServiceEntryReference(r.Context(), "asset", chi.URLParam(r, "assetID"))
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	entry, err := req.Service.ResolveServiceEntryReference(r.Context(), "service_entries", chi.URLParam(r, "entryID"))
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	rows, err := req.Service.ServiceEntryRevisions(r.Context(), req.Actor, job, asset, entry)
	if err != nil {
		serviceEntryAPIError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, rows)
}
