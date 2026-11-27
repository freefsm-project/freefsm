package handlers

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/liststate"
)

// collectionListSpec preserves each dedicated collection's public filter and
// sort vocabulary. In particular, archived=1 retains the owning list's existing
// meaning; unsupported archive modes are never accepted and ignored.
func collectionListSpec(key string) liststate.Spec {
	sorts := []string{"name"}
	switch key {
	case "customers":
		sorts = []string{"name", "name_desc", "updated"}
	case "contacts", "locations":
		sorts = []string{"name", "name_desc"}
	case "assets", "projects":
		sorts = []string{"updated"}
	case "jobs":
		sorts = []string{"schedule_desc"}
	case "estimates", "invoices":
		sorts = []string{"created_desc", "created_asc", "title_asc", "number_asc"}
	case "leases":
		sorts = []string{"number"}
	case "items":
	default:
		return liststate.Spec{}
	}
	fields := liststate.CommonFields(sorts...)
	delete(fields, "state")
	fields["search"] = liststate.Text(500)
	defaults := url.Values{"q": {""}, "sort": {sorts[0]}, "page": {"1"}, "per_page": {"25"}}
	id := liststate.Optional(liststate.Integer(0, 9223372036854775807))
	switch key {
	case "customers", "contacts", "locations", "assets", "items":
		fields["archived"] = liststate.Enum("", "0", "1")
	}
	switch key {
	case "assets":
		fields["customer_id"], fields["asset_type_id"], fields["asset_status_id"] = id, id, id
	case "projects":
		fields["status_id"] = id
	case "jobs", "estimates", "invoices":
		fields["status_id"], fields["customer_id"] = id, id
		fields["scope"] = liststate.Enum("", "A", "J")
		if key == "estimates" {
			fields["valid"] = liststate.Enum("", "current")
		}
	case "leases":
		fields["state"] = liststate.Enum("active", "archived", "all")
		defaults.Set("state", "active")
		fields["facet"] = liststate.Enum("", "base", "billing")
		fields["lifecycle"] = liststate.Enum("", "draft", "scheduled", "active", "month_to_month", "terminated")
		fields["queue"] = liststate.Enum("", "starts", "renewals", "termination", "placement", "monthly", "failed", "review")
		fields["placement"] = liststate.Enum("", "unplaced", "wrong_premises")
		fields["billing"] = liststate.Enum("", "failed", "review")
		for _, key := range []string{"customer", "asset", "premises"} {
			fields[key] = liststate.Optional(liststate.Integer(1, 9223372036854775807))
		}
		fields["ending_from"], fields["ending_through"] = liststate.Optional(liststate.Date), liststate.Optional(liststate.Date)
	}
	return liststate.Spec{Fields: fields, Defaults: defaults}
}

// Validate here as well as in the router adapter: direct GET callbacks used for
// preference reauthorization must have precisely the same strict contract.
func collectionQuery(w http.ResponseWriter, r *http.Request, key string) (url.Values, bool) {
	raw, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "Invalid list filters", http.StatusBadRequest)
		return nil, false
	}
	query, err := liststate.Parse(raw, collectionListSpec(key))
	if err != nil || query.Reset {
		http.Error(w, "Invalid list filters", http.StatusBadRequest)
		return nil, false
	}
	values := query.Values
	// Existing dedicated forms use search; q is also accepted for shared URLs.
	// Conflicting aliases must not silently discard either submitted filter.
	if values.Get("q") != "" {
		if values.Has("search") && values.Get("search") != values.Get("q") {
			http.Error(w, "Conflicting search filters", http.StatusBadRequest)
			return nil, false
		}
		values.Set("search", values.Get("q"))
	}
	if values.Get("ending_from") != "" && values.Get("ending_through") != "" && values.Get("ending_from") > values.Get("ending_through") {
		http.Error(w, "Invalid ending range", http.StatusBadRequest)
		return nil, false
	}
	request, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil, false
	}
	if err := request.Service.CheckCollectionListQuery(r.Context(), request.Actor, key, values); err != nil {
		authorizationError(w, r, err)
		return nil, false
	}
	return values, true
}

func collectionPagination(values url.Values) (page, size int) {
	page, _ = strconv.Atoi(values.Get("page"))
	size, _ = strconv.Atoi(values.Get("per_page"))
	return
}

// Reports keep their historical byte limit through parseDocumentListFilter;
// only main browser collections use the 500-character query contract.
func parseDocumentCollectionFilter(kind string, values url.Values) (authorization.DocumentListFilter, error) {
	filter := authorization.DocumentListFilter{Search: values.Get("search"), Sort: values.Get("sort"), Valid: values.Get("valid")}
	var err error
	if filter.StatusID, err = selectedReportID(values, "status_id"); err != nil {
		return filter, err
	}
	if filter.CustomerID, err = selectedReportID(values, "customer_id"); err != nil {
		return filter, err
	}
	return authorization.NormalizeDocumentCollectionFilter(kind, filter)
}

// Slice only after the domain selector has authorized, filtered and sorted.
// Out-of-range pages remain explicit empty pages, not silently clamped URLs.
func collectionPageBounds(page, size, total int) (start, end int) {
	start = min((page-1)*size, total)
	return start, min(start+size, total)
}
