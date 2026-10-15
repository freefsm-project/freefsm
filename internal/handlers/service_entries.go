package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func writeServiceEntryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrConflict) {
		authorizationError(w, r, err)
		return
	}
	// A dependency's SQLSTATE is not a domain disposition. In particular an
	// audit constraint failure must remain retryable under the original ID.
	http.Error(w, "Service Entry temporarily unavailable. Retry the same operation.", 503)
}

func serviceEntries(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	jobID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	assetID, err := strconv.ParseInt(chi.URLParam(r, "assetID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if err = r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		in := authorization.ServiceEntryInput{ServiceDate: r.FormValue("service_date"), Summary: r.FormValue("summary")}
		in.LinkID, _ = strconv.ParseInt(r.FormValue("link_id"), 10, 64)
		for _, value := range r.PostForm["responsible_user_id"] {
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				http.Error(w, "Invalid responsible user", 400)
				return
			}
			in.ResponsibleUsers = append(in.ResponsibleUsers, id)
		}
		command := authorization.ServiceEntryCommand{OperationID: r.FormValue("operation_id"), JobID: jobID, AssetID: assetID, Content: in}
		if target := chi.URLParam(r, "entryID"); target != "" {
			id, parseErr := strconv.ParseInt(target, 10, 64)
			if parseErr != nil {
				http.NotFound(w, r)
				return
			}
			command.EntryID = id
			command.Revision = r.FormValue("revision")
		}
		_, err = req.Service.SubmitServiceEntry(r.Context(), req.Actor, command)
		if err != nil {
			writeServiceEntryError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", jobID, assetID), 303)
		return
	}
	page, err := req.Service.ServiceEntryPage(r.Context(), req.Actor, jobID, assetID)
	if err != nil {
		writeServiceEntryError(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Service-Relationship-ID", strconv.FormatInt(page.LinkID, 10))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page.Entries)
		return
	}
	render(w, r, templates.ServiceEntries(jobID, assetID, page))
}

func serviceEntryReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	receipt, err := req.Service.ServiceEntryReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		writeServiceEntryError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(receipt)
}

func serviceEntryRevisions(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	job, asset := int64(0), id
	if value := chi.URLParam(r, "assetID"); value != "" {
		job = id
		asset, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	entry, err := strconv.ParseInt(chi.URLParam(r, "entryID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rows, err := req.Service.ServiceEntryRevisions(r.Context(), req.Actor, job, asset, entry)
	if err != nil {
		writeServiceEntryError(w, r, err)
		return
	}
	render(w, r, templates.ServiceEntryRevisions(rows))
}

func serviceEntryHistory(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	asset, customer := id, int64(0)
	if value := chi.URLParam(r, "assetID"); value != "" {
		customer = id
		asset, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	entries, err := req.Service.AssetServiceEntries(r.Context(), req.Actor, asset, customer)
	if err != nil {
		writeServiceEntryError(w, r, err)
		return
	}
	render(w, r, templates.OfficeServiceEntryHistory(entries))
}
