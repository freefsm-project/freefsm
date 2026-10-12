package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func vocabularyGuard(domain string, manage bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req, ok := authorization.FromContext(r.Context())
			if !ok {
				http.Error(w, "Forbidden", 403)
				return
			}
			p, err := req.Service.Principal(r.Context(), req.Actor)
			action := "view"
			if manage {
				action = "manage"
			}
			if err == nil && !p.Allows("configuration."+domain+"."+action, authorization.Configuration) {
				err = authorization.ErrForbidden
			}
			if err != nil {
				authorizationError(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeAssetVocabularyError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(authorization.VocabularyError(err), authorization.ErrConflict) {
		authorizationError(w, r, authorization.ErrConflict)
		return
	}
	internalServerError(w, r, "save asset", err)
}

func canAttachTag(r *http.Request, kind string, id int64) bool {
	req, ok := authorization.FromContext(r.Context())
	return ok && req.Service.TagActionAllowed(r.Context(), req.Actor, kind, id, "attach")
}

func vocabularyHistory(domain string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		p, err := req.Service.Principal(r.Context(), req.Actor)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		rows, err := req.Service.Pool.Query(r.Context(), `SELECT id,recorded_at::text,detail FROM authorization_audit WHERE action=$1 AND ($2::bigint=0 OR id<$2) AND ($3='assets' OR coalesce(detail->'after'->>'company_id',detail->'before'->>'company_id')=$4) ORDER BY id DESC LIMIT 26`, "configuration."+domain+".manage", before, domain, fmt.Sprint(p.CompanyID))
		if err != nil {
			internalServerError(w, r, "load vocabulary history", err)
			return
		}
		defer rows.Close()
		entries := []templates.VocabularyHistoryRow{}
		next := int64(0)
		for rows.Next() {
			var id int64
			var when string
			var raw []byte
			if err := rows.Scan(&id, &when, &raw); err != nil {
				internalServerError(w, r, "load vocabulary history", err)
				return
			}
			if len(entries) == 25 {
				next = entries[24].ID
				break
			}
			var detail struct {
				Operation     string
				Before, After *struct {
					Name, Color string
					Retired     bool
				}
			}
			if err := json.Unmarshal(raw, &detail); err != nil {
				internalServerError(w, r, "decode vocabulary history", err)
				return
			}
			row := templates.VocabularyHistoryRow{ID: id, When: when, Operation: detail.Operation}
			if detail.Before != nil {
				row.Before = fmt.Sprintf("%s %s (retired: %t)", detail.Before.Name, detail.Before.Color, detail.Before.Retired)
			}
			if detail.After != nil {
				row.After = fmt.Sprintf("%s %s (retired: %t)", detail.After.Name, detail.After.Color, detail.After.Retired)
			}
			entries = append(entries, row)
		}
		if err := rows.Err(); err != nil {
			internalServerError(w, r, "load vocabulary history", err)
			return
		}
		templates.VocabularyHistory(entries, r.URL.Path, next).Render(r.Context(), w)
	}
}

func vocabularyMutation(kind, operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		id := int64(0)
		if raw := chi.URLParam(r, "id"); raw != "" {
			var err error
			id, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				http.Error(w, "Invalid id", 400)
				return
			}
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		order, err := strconv.Atoi(r.FormValue("sort_order"))
		if r.FormValue("sort_order") != "" && err != nil {
			http.Error(w, "Invalid order", 400)
			return
		}
		_, err = req.Service.ChangeVocabulary(r.Context(), req.Actor, kind, id, operation, authorization.VocabularyInput{Name: r.FormValue("name"), Color: r.FormValue("color"), SortOrder: order})
		if err != nil {
			if errors.Is(err, authorization.ErrVocabularyInvalid) {
				http.Error(w, "Invalid name or color", 400)
			} else if errors.Is(err, authorization.ErrConflict) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrPending) {
				authorizationError(w, r, err)
			} else {
				internalServerError(w, r, "change vocabulary", err)
			}
			return
		}
		target := "/settings/assets"
		if kind == "tag" {
			target = "/tags"
		}
		http.Redirect(w, r, target, 303)
	}
}

func tagLinkMutation(kind string, attach bool, tags *services.TagService, links *services.TagLinkService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, tagID, ok := tagRouteIDs(w, r)
		if !ok {
			return
		}
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", 403)
			return
		}
		if err := req.Service.ChangeTagLink(r.Context(), req.Actor, kind, id, tagID, attach); err != nil {
			if errors.Is(err, authorization.ErrConflict) || errors.Is(err, authorization.ErrNotFound) || errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrPending) {
				authorizationError(w, r, err)
			} else {
				internalServerError(w, r, "change tag reference", err)
			}
			return
		}
		p, err := req.Service.Principal(r.Context(), req.Actor)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		current, err := links.ListForObject(r.Context(), p.CompanyID, objectref.New(objectref.Type(kind), id))
		if err != nil {
			internalServerError(w, r, "load tags", err)
			return
		}
		all, err := tags.ListAll(r.Context(), p.CompanyID)
		if err != nil {
			internalServerError(w, r, "load tags", err)
			return
		}
		templates.TagWidget(templates.TagWidgetData{BaseURL: fmt.Sprintf("/%ss/%d", kind, id), Tags: tagsToRows(current), AllTags: tagsToRows(all)}).Render(r.Context(), w)
	}
}
