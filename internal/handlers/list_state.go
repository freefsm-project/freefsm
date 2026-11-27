package handlers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/liststate"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// The same read handler (including its collection gate) serves GET and the
// protected preference revalidation. Only the ten main collections use this.
func registerCollectionList(r chi.Router, authority *authorization.Service, path, key, gate string, read http.HandlerFunc) {
	spec := collectionListSpec(key)
	if gate != "" {
		read = collectionRead(gate)(read).ServeHTTP
	}
	r.With(listState(authority, key, spec)).Get(path, read)
	base := strings.TrimSuffix(path, "/")
	r.Post(base+"/state", saveListState(authority, key, spec, read))
	r.Post(base+"/reset", resetListState(authority, key, spec))
}

// Presentation writes have their own CSRF/CAS protocol, not business receipts.
func listPresentationWritePath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || (parts[1] != "state" && parts[1] != "reset") {
		return false
	}
	switch parts[0] {
	case "search", "customers", "contacts", "locations", "assets", "jobs", "projects", "estimates", "invoices", "leases", "items":
		return true
	}
	return false
}

// listState resolves presentation only. GET never changes durable preferences.
func listState(authority *authorization.Service, key string, spec liststate.Spec) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			// Outer middleware may already have captured unvalidated transport.
			// Only the successfully parsed request may supply a notification.
			r = r.WithContext(context.WithValue(r.Context(), middleware.FlashKey, ""))
			raw, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil {
				listStateFailure(w, r, key, raw, 0, http.StatusBadRequest, "Review the URL filters.")
				return
			}
			query, err := liststate.Parse(raw, spec)
			if err != nil {
				listStateFailure(w, r, key, raw, 0, http.StatusBadRequest, "Review the URL filters.")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), middleware.FlashKey, raw.Get("flash")))
			saved, err := authority.LoadListState(r.Context(), authorizationActor(r), key)
			if err != nil {
				listStateFailure(w, r, key, raw, 0, listStateStatus(err), "Saved filters are unavailable. Retry this request.")
				return
			}
			if query.Reset {
				listStateFailure(w, r, key, raw, saved.Revision, http.StatusOK, "Use Reset below to clear your saved filters.")
				return
			}
			if !query.Explicit && len(saved.Values) != 0 {
				query, err = liststate.Parse(saved.Values, spec)
				if err != nil {
					listStateFailure(w, r, key, saved.Values, saved.Revision, http.StatusBadRequest, "Saved filters are no longer valid. Review them or Reset.")
					return
				}
			}
			values := query.Values
			canonical := "/" + key + "?" + liststate.Canonical(values)
			presentation := templates.ListPresentation{Key: key, Values: values, Revision: saved.Revision, CanonicalURL: canonical}
			if validate := spec.Fields["sort"]; validate != nil {
				candidates := []string{spec.Defaults.Get("sort"), "name", "name_desc", "updated", "number", "number_desc", "created", "created_desc", "created_asc", "title_asc", "number_asc", "schedule_desc", "date", "date_desc", "due", "scheduled", "title", "newest"}
				seen := make(map[string]bool)
				for _, candidate := range candidates {
					if candidate != "" && !seen[candidate] && validate([]string{candidate}) == nil {
						presentation.Sorts = append(presentation.Sorts, candidate)
						seen[candidate] = true
					}
				}
			}
			ctx := templates.WithListPresentation(r.Context(), presentation)
			r = r.Clone(ctx)
			r.URL.RawQuery = liststate.Canonical(values)
			response := &listResponse{header: make(http.Header)}
			next.ServeHTTP(response, r)
			if response.status == 0 {
				response.status = http.StatusOK
			}
			if response.status >= 400 {
				message := "Results are unavailable. Review these filters, Retry or Reset."
				if response.status == http.StatusForbidden {
					message = "These filters are not authorized. Review them or Reset."
				}
				listStateFailure(w, r, key, values, saved.Revision, response.status, message)
				return
			}
			if response.status >= 200 && response.status < 300 {
				w.Header().Set("Content-Location", canonical)
				w.Header().Set("HX-Push-Url", canonical)
			}
			for name, values := range response.header {
				w.Header()[name] = values
			}
			w.WriteHeader(response.status)
			_, _ = w.Write(response.body.Bytes())
		})
	}
}

// saveListState reruns the read boundary under current authority before saving.
// The revision is the one rendered by GET, never a freshly substituted revision.
func saveListState(authority *authorization.Service, key string, spec liststate.Spec, read http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		if r.ParseForm() != nil {
			http.Error(w, "Invalid filters", 400)
			return
		}
		for key, values := range r.PostForm {
			if (key != "query" && key != "revision" && key != "csrf_token") || len(values) != 1 {
				http.Error(w, "Invalid filters", 400)
				return
			}
		}
		revision, err := strconv.ParseInt(r.PostForm.Get("revision"), 10, 64)
		if err != nil || revision < 0 {
			http.Error(w, "Invalid revision", 400)
			return
		}
		values, err := url.ParseQuery(r.PostForm.Get("query"))
		if err != nil {
			http.Error(w, "Invalid filters", 400)
			return
		}
		query, err := liststate.Parse(values, spec)
		if err != nil || query.Reset || !query.Explicit {
			http.Error(w, "Invalid filters", 400)
			return
		}
		request := r.Clone(r.Context())
		request.Method = http.MethodGet
		request.URL.Path = "/" + key
		request.URL.RawQuery = liststate.Canonical(query.Values)
		// Discard POST transport fields so existing list reads see only the query.
		request.Form, request.PostForm = nil, nil
		response := &listResponse{header: make(http.Header)}
		read(response, request)
		if response.status >= 300 {
			status := response.status
			if status < 400 {
				status = http.StatusForbidden
			}
			http.Error(w, "Filters could not be reauthorized", status)
			return
		}
		// The successful read above proves availability, not authority to commit.
		// Recheck domain/query grants under the SAME fence as the CAS write.
		state, err := authority.SaveAuthorizedListState(r.Context(), authorizationActor(r), key, liststate.Persistent(query.Values), revision, func(tx pgx.Tx, principal authorization.Principal) error {
			if key == "search" {
				return authorization.ValidateDiscoveryQuery(r.Context(), tx, principal, discoveryQueryFromValues(query.Values))
			}
			return authorization.ValidateCollectionListQuery(r.Context(), tx, principal, key, query.Values)
		})
		if err != nil {
			http.Error(w, "Filters could not be saved. Reload and retry.", listStateStatus(err))
			return
		}
		w.Header().Set("X-List-State-Revision", strconv.FormatInt(state.Revision, 10))
		if r.Header.Get("X-List-State") != "save" {
			http.Redirect(w, r, "/"+key+"?"+liststate.Canonical(query.Values), http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type listResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *listResponse) Header() http.Header { return w.header }
func (w *listResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *listResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func listStateStatus(err error) int {
	if errors.Is(err, authorization.ErrNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, authorization.ErrForbidden) {
		return http.StatusForbidden
	}
	if errors.Is(err, authorization.ErrConflict) {
		return http.StatusConflict
	}
	return http.StatusServiceUnavailable
}

func listStateFailure(w http.ResponseWriter, r *http.Request, key string, values url.Values, revision int64, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = templates.ListStateFailure(templates.ListPresentation{Key: key, Values: values, Revision: revision}, message).Render(templates.WithoutListPresentation(r.Context()), w)
}

// Reset is mounted only in the authenticated, CSRF-protected router.
func resetListState(authority *authorization.Service, key string, spec liststate.Spec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid Reset request", 400)
			return
		}
		for key, values := range r.PostForm {
			if (key != "revision" && key != "csrf_token") || len(values) != 1 {
				http.Error(w, "Invalid Reset request", http.StatusBadRequest)
				return
			}
		}
		revision, err := strconv.ParseInt(r.PostForm.Get("revision"), 10, 64)
		if err != nil || revision < 0 {
			http.Error(w, "Invalid Reset revision", 400)
			return
		}
		_, err = authority.ResetListState(r.Context(), authorizationActor(r), key, revision)
		if err != nil {
			listStateFailure(w, r, key, nil, revision, listStateStatus(err), "Reset could not be confirmed. Reload and retry.")
			return
		}
		defaults, err := liststate.Parse(url.Values{}, spec)
		if err != nil {
			http.Error(w, "Defaults unavailable", 503)
			return
		}
		http.Redirect(w, r, "/"+key+"?"+liststate.Canonical(defaults.Values), http.StatusSeeOther)
	}
}

func listEnum(allowed ...string) func([]string) error {
	return liststate.Enum(allowed...)
}

func listID(values []string) error {
	return liststate.Optional(liststate.Integer(1, 9223372036854775807))(values)
}

func listDate(values []string) error {
	return liststate.Optional(liststate.Date)(values)
}
