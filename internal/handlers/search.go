package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/liststate"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
)

type SearchHandler struct{ svc *services.SearchService }

func NewSearchHandler(svc *services.SearchService) *SearchHandler { return &SearchHandler{svc: svc} }

func searchStateSpec() liststate.Spec {
	fields := map[string]func([]string) error{
		"q":        liststate.Text(500),
		"page":     liststate.Integer(1, 1000000),
		"per_page": listEnum("25", "50", "100"),
		"sort":     listEnum("relevance", "name", "name_desc"),
		"state":    listEnum("active", "archived", "all"),
		"type": func(values []string) error {
			seen := map[string]bool{}
			validate := listEnum("asset", "contact", "customer", "estimate", "invoice", "item", "job", "lease", "location", "project")
			for _, value := range values {
				if seen[value] {
					return fmt.Errorf("duplicate type")
				}
				if err := validate([]string{value}); err != nil {
					return err
				}
				seen[value] = true
			}
			return nil
		},
		"lifecycle": listEnum("", "draft", "scheduled", "active", "month_to_month", "terminated"),
		"queue":     listEnum("", "starts", "renewals", "termination", "placement", "monthly", "failed", "review"),
		"placement": listEnum("", "unplaced", "wrong_premises"),
		"billing":   listEnum("", "failed", "review"),
		"customer":  listID, "asset": listID, "premises": listID,
		"ending_from": listDate, "ending_through": listDate,
	}
	return liststate.Spec{Fields: fields, Defaults: url.Values{"q": {""}, "sort": {"relevance"}, "state": {"active"}, "page": {"1"}, "per_page": {"25"}}}
}

func discoveryQueryFromValues(values url.Values) authorization.DiscoveryQuery {
	page, _ := strconv.Atoi(values.Get("page"))
	size, _ := strconv.Atoi(values.Get("per_page"))
	id := func(key string) int64 { n, _ := strconv.ParseInt(values.Get(key), 10, 64); return n }
	return authorization.DiscoveryQuery{
		Search: values.Get("q"), Types: values["type"], State: values.Get("state"), Sort: values.Get("sort"), Page: page, PerPage: size,
		Lease: authorization.LeaseWorkspaceQuery{Lifecycle: values.Get("lifecycle"), Queue: values.Get("queue"), Placement: values.Get("placement"), Billing: values.Get("billing"), Customer: id("customer"), Asset: id("asset"), Premises: id("premises"), EndingFrom: values.Get("ending_from"), EndingThrough: values.Get("ending_through")},
	}
}

func (h *SearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	query := discoveryQueryFromValues(values)
	result, err := h.svc.Discover(r.Context(), authorizationActor(r), query)
	if err != nil {
		status := http.StatusServiceUnavailable
		message := "Search is unavailable. Previous results are unavailable. Retry with these filters."
		if errors.Is(err, authorization.ErrForbidden) {
			status = http.StatusForbidden
			message = "These filters are no longer authorized. Review them or Reset."
		}
		if errors.Is(err, authorization.ErrConflict) {
			status = http.StatusBadRequest
			message = "Review the search filters."
		}
		// Never render any part of a failed engine projection.
		listStateFailure(w, r, "search", values, templates.ListRevision(r.Context()), status, message)
		return
	}
	_ = templates.SearchResults(templates.DiscoveryPageData{Page: result, Values: values}).Render(r.Context(), w)
}
