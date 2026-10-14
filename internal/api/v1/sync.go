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
	result, err := req.Service.SyncJobs(r.Context(), req.Actor, query)
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
	in := authorization.JobSyncRequest{Scope: "assigned", PageSize: 50}
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
	if in.Cursor != "" && in.Page != "" || in.Scope != "assigned" && in.Scope != "all" {
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
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: apiError{RequestID: id, Details: map[string]any{"purge": "all"}, Code: code, Message: "purge cached Job context before synchronizing current authority", RecoveryDisposition: disposition}})
}
