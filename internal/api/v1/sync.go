package v1

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func (h *handler) syncJobs(w http.ResponseWriter, r *http.Request) {
	h.syncProjection(w, r, false)
}

func (h *handler) syncClocks(w http.ResponseWriter, r *http.Request) {
	h.syncProjection(w, r, true)
}

func (h *handler) syncItems(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	query, err := urlSyncQuery(r)
	if r.URL.Query().Get("scope") == "" {
		query.Scope = "all"
	}
	if err != nil || query.Scope != "all" {
		writeError(w, 400, "invalid_request", "invalid synchronization query")
		return
	}
	result, err := req.Service.SyncItems(r.Context(), req.Actor, query)
	writeSyncResult(w, result, err)
}

func (h *handler) syncCommercial(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			unauthorized(w)
			return
		}
		query, err := urlSyncQuery(r)
		if err != nil {
			writeError(w, 400, "invalid_request", "invalid synchronization query")
			return
		}
		result, err := req.Service.SyncCommercial(r.Context(), req.Actor, kind, query)
		if err == nil {
			for _, state := range result.Records {
				if err = publicCommercialState(state); err != nil {
					break
				}
			}
		}
		writeSyncResult(w, result, err)
	}
}

func (h *handler) syncProjection(w http.ResponseWriter, r *http.Request, clocks bool) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	query, err := urlSyncQueryFor(r, clocks)
	if err != nil {
		writeError(w, 400, "invalid_request", "invalid synchronization query")
		return
	}
	var result authorization.SyncPage
	if clocks {
		result, err = req.Service.SyncClocks(r.Context(), req.Actor, query)
	} else {
		result, err = req.Service.SyncJobs(r.Context(), req.Actor, query)
	}
	writeSyncResult(w, result, err)
}

func writeSyncResult(w http.ResponseWriter, result authorization.SyncPage, err error) {
	switch {
	case errors.Is(err, authorization.ErrSyncResnapshot):
		writeSyncPurge(w, 409, "resnapshot_required", "purge_and_resnapshot")
	case errors.Is(err, authorization.ErrForbidden), errors.Is(err, authorization.ErrPending):
		writeSyncPurge(w, 403, "forbidden", "purge")
	case err != nil:
		w.Header().Set("Retry-After", "5")
		writeError(w, 503, "unavailable", "synchronization unavailable; retry without advancing the cursor")
	default:
		writeJSON(w, 200, result)
	}
}

func urlSyncQuery(r *http.Request) (authorization.JobSyncRequest, error) {
	return urlSyncQueryFor(r, false)
}

func urlSyncQueryFor(r *http.Request, clocks bool) (authorization.JobSyncRequest, error) {
	in := authorization.JobSyncRequest{Scope: "assigned", PageSize: 50}
	if clocks {
		in.Scope = "own"
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return in, err
	}
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return in, errors.New("repeated or empty parameter")
		}
		switch key {
		case "cursor":
			in.Cursor = values[0]
		case "page":
			in.Page = values[0]
		case "scope":
			in.Scope = values[0]
		case "page_size":
			n, err := strconv.Atoi(values[0])
			if err != nil || n < 1 || n > 200 {
				return in, errors.New("page size")
			}
			in.PageSize = n
		default:
			return in, errors.New("unknown parameter")
		}
	}
	validScope := !clocks && (in.Scope == "assigned" || in.Scope == "all") || clocks && in.Scope == "own"
	if in.Cursor != "" && in.Page != "" || !validScope {
		return in, errors.New("invalid scope or token combination")
	}
	return in, nil
}

func writeSyncPurge(w http.ResponseWriter, status int, code, disposition string) {
	id := w.Header().Get("X-Request-ID")
	if id == "" {
		id = uuid.NewString()
		w.Header().Set("X-Request-ID", id)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: apiError{Status: status, RequestID: id, Details: map[string]any{"purge": "all"}, Code: code, Message: "purge cached synchronized context before synchronizing current authority", RecoveryDisposition: disposition}})
}
