package templates

import (
	"context"
	"net/url"
	"strconv"
)

type ListPresentation struct {
	Key          string
	Values       url.Values
	Revision     int64
	CanonicalURL string
	Sorts        []string
}
type listPresentationKey struct{}

func listSortLabel(value string) string {
	switch value {
	case "name":
		return "Name A–Z"
	case "name_desc":
		return "Name Z–A"
	case "updated":
		return "Recently updated"
	case "created_desc":
		return "Newest first"
	case "created_asc":
		return "Oldest first"
	case "title_asc":
		return "Title A–Z"
	case "number", "number_asc":
		return "Number"
	case "schedule_desc":
		return "Scheduled (latest first)"
	default:
		return value
	}
}

// These are filters, not the user's independently authorized view/scope.
// Clear recovery retains scope, archive/facet and relationship context.
var listRemovableFilters = []string{"q", "search", "status_id", "asset_type_id", "asset_status_id", "valid", "lifecycle", "queue", "placement", "billing", "ending_from", "ending_through"}

func listHasFilters(ctx context.Context, fallback bool) bool {
	state, ok := currentListPresentation(ctx)
	if !ok {
		return fallback
	}
	for _, key := range listRemovableFilters {
		value := state.Values.Get(key)
		if value == "" {
			continue
		}
		switch key {
		case "status_id", "asset_type_id", "asset_status_id":
			if value == "0" {
				continue
			}
		}
		return true
	}
	return false
}

func listRecoveryURL(ctx context.Context, clear bool) string {
	state, ok := currentListPresentation(ctx)
	if !ok {
		return ""
	}
	values := cloneQuery(state.Values)
	if clear {
		for _, key := range listRemovableFilters {
			values.Del(key)
		}
		values.Set("q", "")
	}
	values.Set("page", "1")
	return "/" + state.Key + "?" + values.Encode()
}

func listEmptyCreation(ctx context.Context) bool {
	state, ok := currentListPresentation(ctx)
	return !ok || state.Values.Get("archived") != "1" && state.Values.Get("state") != "archived"
}

func WithListPresentation(ctx context.Context, state ListPresentation) context.Context {
	return context.WithValue(ctx, listPresentationKey{}, state)
}
func WithoutListPresentation(ctx context.Context) context.Context {
	return context.WithValue(ctx, listPresentationKey{}, (*ListPresentation)(nil))
}
func currentListPresentation(ctx context.Context) (ListPresentation, bool) {
	state, ok := ctx.Value(listPresentationKey{}).(ListPresentation)
	return state, ok
}

// ListRevision is the revision originally loaded by this GET.
func ListRevision(ctx context.Context) int64 {
	state, ok := currentListPresentation(ctx)
	if !ok || state.Revision == 0 {
		return 0
	}
	return state.Revision
}
func (p ListPresentation) RevisionString() string { return strconv.FormatInt(p.Revision, 10) }
func listPaginationQuery(ctx context.Context, fallback url.Values) url.Values {
	if state, ok := currentListPresentation(ctx); ok {
		return state.Values
	}
	return fallback
}
func listPageURL(ctx context.Context, fallback string, page int) string {
	if state, ok := currentListPresentation(ctx); ok {
		return state.PageURL(page)
	}
	return fallback
}

// Carry only controls not edited by this form. The complete explicit URL is
// submitted, without duplicate singleton fields or a stale search alias.
func listFormCarry(ctx context.Context, edited ...string) []ListQueryField {
	state, ok := currentListPresentation(ctx)
	if !ok {
		return nil
	}
	query := cloneQuery(state.Values)
	for _, key := range append([]string{"q", "search", "page"}, edited...) {
		query.Del(key)
	}
	return queryFields(query)
}
func listSearchFields(ctx context.Context, p ListToolbarData) []ListQueryField {
	query := listQuery("", p.Filters, p.Scope)
	if state, ok := currentListPresentation(ctx); ok {
		query = cloneQuery(state.Values)
	}
	for _, key := range []string{"q", "search", "page"} {
		query.Del(key)
	}
	return queryFields(query)
}
func listFilterScope(ctx context.Context, p ListToolbarData) []ListQueryField {
	query := cloneQuery(p.Scope)
	if state, ok := currentListPresentation(ctx); ok {
		query = cloneQuery(state.Values)
		for _, key := range append([]string{"q", "search", "page"}, p.FilterFields...) {
			query.Del(key)
		}
		// The legacy archive checkbox is the complete archive-state control.
		for _, key := range p.FilterFields {
			if key == "archived" {
				query.Del("state")
			}
		}
	}
	return queryFields(query)
}
func listClearURL(ctx context.Context, p ListToolbarData) string {
	if state, ok := currentListPresentation(ctx); ok {
		query := cloneQuery(state.Values)
		query.Del("page")
		for _, key := range p.FilterFields {
			if key != "sort" && key != "per_page" {
				query.Del(key)
			}
			if key == "archived" {
				query.Del("state")
			}
		}
		return listURL(p.Action, query, 1)
	}
	return listURL(p.Action, listQuery(p.Search, nil, p.Scope), 1)
}
func listClearInclude(ctx context.Context, p ListToolbarData) string {
	if _, ok := currentListPresentation(ctx); ok {
		return "this"
	}
	return "#" + listFilterSearchID(p.Target)
}
func listClearRequestURL(ctx context.Context, p ListToolbarData) string {
	if _, ok := currentListPresentation(ctx); ok {
		return listClearURL(ctx, p)
	}
	return listURL(p.Action, p.Scope, 1)
}
func (p ListPresentation) SizeSortFields() []ListQueryField {
	query := cloneQuery(p.Values)
	for _, key := range []string{"page", "per_page", "sort"} {
		query.Del(key)
	}
	return queryFields(query)
}
func (p ListPresentation) PageURL(page int) string {
	values := make(url.Values)
	for key, entries := range p.Values {
		values[key] = append([]string(nil), entries...)
	}
	values.Set("page", strconv.Itoa(page))
	return "/" + p.Key + "?" + values.Encode()
}
