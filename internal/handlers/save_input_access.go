package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/go-chi/chi/v5"
	"golang.org/x/net/html"
)

func saveInputAccessHandler(s *authorization.Service, current http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		a := saveaction.Action{UserID: authorizationActor(r).UserID, ID: chi.URLParam(r, "operation")}
		receipt, err := (saveaction.Store{Pool: s.Pool}).Receipt(r.Context(), a)
		if err != nil && !errors.Is(err, saveaction.ErrIdentity) {
			middleware.ServiceUnavailable(w, r)
			return
		}
		if err != nil || receipt.Disposition == "not_received" {
			http.NotFound(w, r)
			return
		}
		path := saveFormPath(receipt.RequestPath)
		if path == "" {
			path = receipt.ReviewPath
		}
		if strings.HasPrefix(path, "/save-actions/") || strings.HasPrefix(path, "/web-actions/") {
			http.NotFound(w, r)
			return
		}
		get := r.Clone(context.WithValue(saveaction.WithoutAction(r.Context()), chi.RouteCtxKey, chi.NewRouteContext()))
		u := *r.URL
		u.Path = path
		u.RawPath = ""
		u.RawQuery = ""
		get.URL = &u
		get.RequestURI = path
		get.Header = r.Header.Clone()
		get.Header.Set("Accept", "text/html")
		for _, key := range []string{"HX-Request", "HX-Boosted", "X-FreeFSM-Action", "X-FreeFSM-Revision"} {
			get.Header.Del(key)
		}
		buffer := &formReviewResponse{header: make(http.Header)}
		current.ServeHTTP(buffer, get)
		if buffer.status == 401 || buffer.status == 403 || buffer.status == 404 {
			http.Error(w, "Retained input is no longer accessible", buffer.status)
			return
		}
		if buffer.status != 0 && buffer.status != 200 {
			http.Error(w, "Current input access could not be checked", 503)
			return
		}
		doc, err := html.Parse(bytes.NewReader(buffer.body.Bytes()))
		if err != nil {
			http.Error(w, "Current form unavailable", 503)
			return
		}
		var form *html.Node
		walkSaveNodes(doc, func(n *html.Node) {
			if n.Data != "form" {
				return
			}
			action, err := url.Parse(saveAttr(n, "action"))
			if err == nil && get.URL.ResolveReference(action).Path == receipt.RequestPath {
				form = n
			}
		})
		if form == nil {
			http.NotFound(w, r)
			return
		}
		fields, widgets := map[string]bool{}, map[string]bool{}
		walkSaveNodes(form, func(n *html.Node) {
			if n.Type != html.ElementNode || saveAttr(n, "name") == "" {
				return
			}
			if n.Data != "input" && n.Data != "textarea" && n.Data != "select" && n.Data != "button" {
				return
			}
			for parent := n; parent != nil && parent != form; parent = parent.Parent {
				if hasSaveAttr(parent, "disabled") || hasSaveAttr(parent, "readonly") {
					return
				}
			}
			if saveAttr(n, "type") == "password" {
				return
			}
			name := saveAttr(n, "name")
			fields[name] = true
			switch name {
			case "line_items", "visits", "assignments", "subtasks":
				widgets[name] = true
			}
		})
		fieldNames, widgetNames := []string{}, []string{}
		for key := range fields {
			fieldNames = append(fieldNames, key)
		}
		for key := range widgets {
			widgetNames = append(widgetNames, key)
		}
		sort.Strings(fieldNames)
		sort.Strings(widgetNames)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			EditableFields   []string `json:"editable_fields"`
			StructuredFields []string `json:"structured_fields"`
			FormURL          string   `json:"form_url"`
		}{fieldNames, widgetNames, path})
	}
}
