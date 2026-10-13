package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const (
	embeddedActivityLimit = 10
	objectActivityLimit   = 25
	activityCursorVersion = 1
	maxActivityCursorSize = 1024
)

var activityActionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type activityLister interface {
	List(context.Context, services.ActivityListRequest) (services.ActivityPage, error)
}

type activityResolver interface {
	Resolve(context.Context, int64, services.ActivityViewer, []services.ActivityEntry) (services.ActivityResolution, error)
}

type activityConversionService interface {
	ActivityEstimateID(context.Context, conversion.Actor, objectref.Ref) (int64, error)
}

type activityPolicy interface {
	CanAccessObject(context.Context, int64, string, objectref.Ref, services.PolicyAction) bool
}

type ActivityHandler struct {
	svc        activityLister
	resolver   activityResolver
	policySvc  activityPolicy
	conversion activityConversionService
}

func NewActivityHandler(svc activityLister, resolver activityResolver, policySvc activityPolicy, conversionSvc activityConversionService) *ActivityHandler {
	return &ActivityHandler{svc: svc, resolver: resolver, policySvc: policySvc, conversion: conversionSvc}
}

func (h *ActivityHandler) ListForObject(objectType objectref.Type) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rejectEmbeddedPage(w, r) {
			return
		}
		u, ok := activityUser(w, r)
		if !ok {
			return
		}
		objectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ref := objectref.New(objectType, objectID)
		if !ref.Valid() {
			http.NotFound(w, r)
			return
		}
		// Keep the route-level object's policy check even when conversion history broadens the query scope.
		if h.policySvc == nil || !h.policySvc.CanAccessObject(r.Context(), u.ID, u.Role, ref, policyRead) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		var scope services.ActivityScope = services.ObjectActivityScope{Refs: []objectref.Ref{ref}}
		conversionMode := objectType == objectref.TypeEstimate || objectType == objectref.TypeInvoice
		if req, current := authorization.FromContext(r.Context()); current {
			p, err := req.Service.Principal(r.Context(), req.Actor)
			if err != nil {
				authorizationError(w, r, err)
				return
			}
			r = r.WithContext(authorization.WithNavigationReads(r.Context(), p))
			conversionMode = conversionMode && req.Service.ParentFacet(r.Context(), req.Actor, string(objectType), objectID, string(objectType)+".provenance.view") == nil
		}
		if conversionMode {
			if h.conversion == nil {
				http.Error(w, "activity conversion service is required", http.StatusInternalServerError)
				return
			}
			estimateID, err := h.conversion.ActivityEstimateID(r.Context(), activityActor(u), ref)
			if err != nil {
				if objectType == objectref.TypeInvoice && errors.Is(err, conversion.ErrNotFound) {
					conversionMode = false
				} else {
					handleActivityConversionError(w, err)
					return
				}
			} else {
				scope = services.ConversionActivityScope{EstimateID: estimateID, ViewerID: u.ID, ViewerRole: u.Role}
				if _, current := authorization.NavigationPrincipal(r.Context()); current {
					r = r.WithContext(authorization.WithActivityProvenance(r.Context(), estimateID))
				}
			}
		}

		page, rows, err := h.listAndResolve(r.Context(), u, scope, objectActivityLimit, nil, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		filters := url.Values{"object_type": {string(ref.Type)}, "object_id": {strconv.FormatInt(ref.ID, 10)}}
		if conversionMode {
			filters.Set("conversion", "1")
		}
		_, currentRead := authorization.NavigationPrincipal(r.Context())
		render(w, r, templates.ActivityWidget(templates.ActivityWidgetData{
			DOMID:      fmt.Sprintf("activity-%s-%d", ref.Type, ref.ID),
			Entries:    rows,
			HasMore:    page.HasOlder,
			ViewAllURL: viewAllActivityURL(page.HasOlder, filters, currentRead || u.ProtectedAdministrator || u.Role == "admin" || u.Role == "dispatcher"),
		}))
	}
}

func (h *ActivityHandler) ListByType(objectType objectref.Type) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rejectEmbeddedPage(w, r) {
			return
		}
		u, ok := activityUser(w, r)
		if !ok {
			return
		}
		vocabularyView := false
		if objectType == objectref.TypeItem {
			if req, ok := authorization.FromContext(r.Context()); ok {
				p, err := req.Service.Principal(r.Context(), req.Actor)
				if err != nil || !p.Allows("item.view", authorization.All) {
					authorizationError(w, r, authorization.ErrForbidden)
					return
				}
				vocabularyView = true
			}
		}
		if objectType == objectref.TypeTag {
			if req, ok := authorization.FromContext(r.Context()); ok {
				p, err := req.Service.Principal(r.Context(), req.Actor)
				vocabularyView = err == nil && p.Allows("configuration.tags.view", authorization.Configuration)
			}
		}
		if objectType.AdminOnly() && !u.ProtectedAdministrator && !vocabularyView {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		page, rows, err := h.listAndResolve(r.Context(), u, services.TypeActivityScope{Types: []objectref.Type{objectType}}, embeddedActivityLimit, nil, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		filters := url.Values{"type": {string(objectType)}}
		templates.ActivityRecentList(templates.ActivityPageData{Entries: rows, HasMore: page.HasOlder, ViewAllURL: viewAllActivityURL(page.HasOlder, filters, true)}).Render(r.Context(), w)
	}
}

func (h *ActivityHandler) ListForAssetSettings(w http.ResponseWriter, r *http.Request) {
	if rejectEmbeddedPage(w, r) {
		return
	}
	u, ok := activityUser(w, r)
	if !ok {
		return
	}
	types := []objectref.Type{objectref.TypeAssetType, objectref.TypeAssetStatus}
	page, rows, err := h.listAndResolve(r.Context(), u, services.TypeActivityScope{Types: types}, embeddedActivityLimit, nil, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filters := url.Values{"type": {string(types[0]), string(types[1])}}
	templates.ActivityRecentList(templates.ActivityPageData{Entries: rows, HasMore: page.HasOlder, ViewAllURL: viewAllActivityURL(page.HasOlder, filters, true)}).Render(r.Context(), w)
}

func (h *ActivityHandler) ListSchedule(w http.ResponseWriter, r *http.Request) {
	if rejectEmbeddedPage(w, r) {
		return
	}
	u, ok := activityUser(w, r)
	if !ok {
		return
	}
	var scope services.ActivityScope = services.ScheduleActivityScope{ViewerID: u.ID, ViewerRole: u.Role}
	if _, current := authorization.NavigationPrincipal(r.Context()); current {
		scope = services.TypeActivityScope{Types: []objectref.Type{objectref.TypeJob}, Actions: []string{"scheduled", "rescheduled", "dispatched"}}
	}
	page, rows, err := h.listAndResolve(r.Context(), u, scope, embeddedActivityLimit, nil, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filters := url.Values{
		"type":   {string(objectref.TypeJob)},
		"action": {"scheduled", "rescheduled", "dispatched"},
	}
	viewAll := ""
	if _, current := authorization.NavigationPrincipal(r.Context()); current || u.ProtectedAdministrator || u.Role == "admin" || u.Role == "dispatcher" {
		viewAll = viewAllActivityURL(page.HasOlder, filters, true)
	}
	templates.ActivityRecentList(templates.ActivityPageData{Entries: rows, HasMore: page.HasOlder, ViewAllURL: viewAll}).Render(r.Context(), w)
}

func (h *ActivityHandler) ListAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u, ok := activityUser(w, r)
	if !ok {
		return
	}
	if _, current := authorization.NavigationPrincipal(r.Context()); !current && !u.ProtectedAdministrator && u.Role != "admin" && u.Role != "dispatcher" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	parsed, err := h.parseIndexRequest(r.Context(), r.URL.Query(), u)
	if err != nil {
		if errors.Is(err, conversion.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if parsed.forbidden {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/settings/activity/history" {
		if !u.ProtectedAdministrator {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		filtered := parsed.scope.(services.FilteredActivityScope)
		filtered.Operational = false
		parsed.scope = filtered
	}
	if parsed.page > 0 {
		http.Redirect(w, r, r.URL.Path+"?"+parsed.filters.Encode(), http.StatusSeeOther)
		return
	}

	var page services.ActivityPage
	var rows []templates.ActivityEntry
	load := func() error {
		var err error
		page, rows, err = h.listAndResolve(r.Context(), u, parsed.scope, objectActivityLimit, parsed.cursor, parsed.direction)
		return err
	}
	if r.URL.Path == "/settings/activity/history" {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		err = auth.Service.WithinPermission(r.Context(), auth.Actor, "audit.view", authorization.Sealed, func(tx pgx.Tx, p authorization.Principal) error {
			if err := load(); err != nil {
				return err
			}
			return protectedaudit.RecordProduct(r.Context(), tx, protectedaudit.ProductAction{ActorID: p.UserID, RoleID: p.RoleID, Action: "audit.view", Scope: string(authorization.Sealed), TargetType: "company", TargetID: p.CompanyID, Channel: "web", Detail: map[string]string{"surface": "administrative-history"}})
		})
	} else {
		err = load()
	}
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		templates.ActivityIndex(templates.ActivityPageData{Filters: parsed.filters, RefreshURL: r.URL.Path, Failed: true}).Render(r.Context(), w)
		return
	}
	data := templates.ActivityPageData{Entries: rows, HasMore: page.HasOlder, Filters: parsed.filters, RefreshURL: r.URL.Path, UpdatedAt: displayDateTime(r.Context(), time.Now())}
	if page.HasOlder && page.OlderCursor != nil {
		filters := cloneValues(parsed.filters)
		filters.Set("cursor", encodeActivityCursor(*page.OlderCursor, services.ActivityOlder, parsed.fingerprint))
		data.OlderURL = r.URL.Path + "?" + filters.Encode()
	}
	if page.HasNewer && page.NewerCursor != nil {
		filters := cloneValues(parsed.filters)
		filters.Set("cursor", encodeActivityCursor(*page.NewerCursor, services.ActivityNewer, parsed.fingerprint))
		data.NewerURL = r.URL.Path + "?" + filters.Encode()
	}
	templates.ActivityIndex(data).Render(r.Context(), w)
}

type activityIndexRequest struct {
	scope       services.ActivityScope
	filters     url.Values
	fingerprint string
	cursor      *services.ActivityCursor
	direction   services.ActivityDirection
	page        int
	forbidden   bool
}

func (h *ActivityHandler) parseIndexRequest(ctx context.Context, values url.Values, u *middleware.UserInfo) (activityIndexRequest, error) {
	var out activityIndexRequest
	values = cloneValues(values)
	for _, key := range []string{"type", "action", "actor", "from", "until", "object_type", "object_id"} {
		if entries := values[key]; len(entries) == 1 && entries[0] == "" {
			values.Del(key)
		}
	}
	types, err := normalizedActivityTypes(values["type"])
	if err != nil {
		return out, err
	}
	actions, err := normalizedActivityActions(values["action"])
	if err != nil {
		return out, err
	}
	for _, typ := range types {
		if typ.AdminOnly() && !u.ProtectedAdministrator {
			out.forbidden = true
			return out, nil
		}
	}

	objectTypeValue, hasObjectType := singleValue(values, "object_type")
	objectIDValue, hasObjectID := singleValue(values, "object_id")
	conversionValue, hasConversion := singleValue(values, "conversion")
	if hasObjectType != hasObjectID || (hasConversion && conversionValue != "1") {
		return out, errors.New("object_type and object_id must be supplied together; conversion must be 1")
	}
	if hasObjectType && len(types) > 0 {
		return out, errors.New("object filters cannot be combined with type filters")
	}
	if hasConversion && !hasObjectType {
		return out, errors.New("conversion requires an exact object filter")
	}

	out.filters = make(url.Values)
	if hasObjectType {
		id, parseErr := strconv.ParseInt(objectIDValue, 10, 64)
		ref, refErr := objectref.Parse(objectTypeValue, id)
		if parseErr != nil || refErr != nil || !ref.Type.Has(objectref.CapActivity) {
			return out, errors.New("invalid activity object")
		}
		if ref.Type.AdminOnly() && !u.ProtectedAdministrator {
			out.forbidden = true
			return out, nil
		}
		if h.policySvc == nil || !h.policySvc.CanAccessObject(ctx, u.ID, u.Role, ref, policyRead) {
			out.forbidden = true
			return out, nil
		}
		var scope services.ActivityScope = services.ObjectActivityScope{Refs: []objectref.Ref{ref}}
		if hasConversion {
			if ref.Type != objectref.TypeEstimate && ref.Type != objectref.TypeInvoice {
				return out, errors.New("conversion activity requires an estimate or invoice")
			}
			if h.conversion == nil {
				return out, errors.New("activity conversion service is required")
			}
			estimateID, err := h.conversion.ActivityEstimateID(ctx, activityActor(u), ref)
			if errors.Is(err, conversion.ErrForbidden) {
				out.forbidden = true
				return out, nil
			}
			if errors.Is(err, conversion.ErrNotFound) {
				return out, conversion.ErrNotFound
			}
			if err != nil {
				return out, fmt.Errorf("resolve conversion activity: %w", err)
			}
			scope = services.ConversionActivityScope{EstimateID: estimateID, ViewerID: u.ID, ViewerRole: u.Role}
			out.filters.Set("conversion", "1")
		}
		out.scope = scope
		out.filters.Set("object_type", string(ref.Type))
		out.filters.Set("object_id", strconv.FormatInt(ref.ID, 10))
	} else if len(types) > 0 {
		out.scope = services.TypeActivityScope{Types: types, Actions: actions}
		for _, typ := range types {
			out.filters.Add("type", string(typ))
		}
		for _, action := range actions {
			out.filters.Add("action", action)
		}
	} else {
		out.scope = services.TenantActivityScope{IncludeAdminOnly: u.ProtectedAdministrator, Actions: actions}
		for _, action := range actions {
			out.filters.Add("action", action)
		}
	}

	filtered := services.FilteredActivityScope{Scope: out.scope, Actions: actions, Operational: true}
	for _, action := range actions {
		if hasObjectType {
			out.filters.Add("action", action)
		}
	}
	if value, ok := singleValue(values, "actor"); ok {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return out, errors.New("actor must be a positive user identifier")
		}
		filtered.ActorID = id
		out.filters.Set("actor", value)
	}
	for key, dest := range map[string]*time.Time{"from": &filtered.From, "until": &filtered.Until} {
		if value, ok := singleValue(values, key); ok {
			date, err := time.ParseInLocation("2006-01-02", value, middleware.CompanyLocation(ctx))
			if err != nil {
				return out, errors.New("activity dates must use YYYY-MM-DD")
			}
			*dest = date
			out.filters.Set(key, value)
		}
	}
	if !filtered.From.IsZero() && !filtered.Until.IsZero() && !filtered.From.Before(filtered.Until) {
		return out, errors.New("until must be after from")
	}
	out.scope = filtered
	out.fingerprint = activityScopeFingerprint(out.filters)
	if pageValue, ok := singleValue(values, "page"); ok {
		page, pageErr := strconv.Atoi(pageValue)
		if pageErr != nil || page < 1 {
			return out, errors.New("invalid legacy activity page")
		}
		out.page = page
	}
	if cursorValue, ok := singleValue(values, "cursor"); ok && out.page == 0 {
		cursor, direction, cursorErr := decodeActivityCursor(cursorValue, out.fingerprint)
		if cursorErr != nil {
			return out, cursorErr
		}
		out.cursor = &cursor
		out.direction = direction
	}
	return out, nil
}

func (h *ActivityHandler) listAndResolve(ctx context.Context, u *middleware.UserInfo, scope services.ActivityScope, limit int, cursor *services.ActivityCursor, direction services.ActivityDirection) (services.ActivityPage, []templates.ActivityEntry, error) {
	page, err := h.svc.List(ctx, services.ActivityListRequest{
		CompanyID: u.CompanyID,
		Scope:     scope,
		Page:      services.ActivityPageRequest{Limit: limit, Cursor: cursor, Direction: direction},
	})
	if err != nil {
		return services.ActivityPage{}, nil, err
	}
	resolution, err := h.resolver.Resolve(ctx, u.CompanyID, services.ActivityViewer{ID: u.ID, Role: u.Role, ProtectedAdministrator: u.ProtectedAdministrator}, page.Entries)
	if err != nil {
		return services.ActivityPage{}, nil, err
	}
	return page, activityRows(ctx, page.Entries, resolution), nil
}

func activityRows(ctx context.Context, entries []services.ActivityEntry, resolution services.ActivityResolution) []templates.ActivityEntry {
	rows := make([]templates.ActivityEntry, 0, len(entries))
	for _, entry := range entries {
		var meta templates.ActivityMetadata
		if err := json.Unmarshal([]byte(entry.Metadata), &meta); err != nil {
			meta = templates.ActivityMetadata{}
		}
		// Legacy previews have no durable Comment identity with which to check
		// removal. Never resurrect their text through retained activity.
		meta.CommentPreview = ""
		actorName := meta.ActorName
		if actorName == "" {
			actorName = resolution.ActorNames[entry.ActorID]
		}
		if actorName == "" {
			actorName = "Unknown historical actor"
			if entry.ActorID > 0 {
				actorName = fmt.Sprintf("User #%d", entry.ActorID)
			}
		}
		target := resolution.Targets[entry.Target]
		entityName := meta.EntityName
		if entityName == "" {
			entityName = target.DisplayName
		}
		if entityName == "" {
			entityName = entry.HistoricalTarget
		}
		if entityName == "" && entry.Target.Valid() {
			entityName = fmt.Sprintf("%s #%d", entry.Target.Type, entry.Target.ID)
		}
		rows = append(rows, templates.ActivityEntry{
			ID: entry.ID, ActorName: actorName, Action: entry.Action, TargetType: string(entry.Target.Type),
			EntityName: entityName, EntityURL: target.URL, Icon: activityIcon(entry.Action), Metadata: meta,
			CreatedAt: displayDateTime(ctx, entry.CreatedAt),
		})
	}
	return rows
}

type encodedActivityCursor struct {
	Version   int    `json:"v"`
	Timestamp string `json:"t"`
	ID        int64  `json:"id"`
	Direction string `json:"d"`
	Scope     string `json:"s"`
}

func encodeActivityCursor(cursor services.ActivityCursor, direction services.ActivityDirection, fingerprint string) string {
	payload, _ := json.Marshal(encodedActivityCursor{
		Version: activityCursorVersion, Timestamp: cursor.CreatedAt.UTC().Format(time.RFC3339Nano),
		ID: cursor.ID, Direction: string(direction), Scope: fingerprint,
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeActivityCursor(value, fingerprint string) (services.ActivityCursor, services.ActivityDirection, error) {
	if value == "" || len(value) > maxActivityCursorSize {
		return services.ActivityCursor{}, "", errors.New("invalid activity cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(payload) > maxActivityCursorSize {
		return services.ActivityCursor{}, "", errors.New("invalid activity cursor")
	}
	var encoded encodedActivityCursor
	if err = json.Unmarshal(payload, &encoded); err != nil || encoded.Version != activityCursorVersion || encoded.ID <= 0 || encoded.Scope != fingerprint {
		return services.ActivityCursor{}, "", errors.New("invalid activity cursor")
	}
	direction := services.ActivityDirection(encoded.Direction)
	if direction != services.ActivityOlder && direction != services.ActivityNewer {
		return services.ActivityCursor{}, "", errors.New("invalid activity cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, encoded.Timestamp)
	if err != nil {
		return services.ActivityCursor{}, "", errors.New("invalid activity cursor")
	}
	return services.ActivityCursor{CreatedAt: createdAt, ID: encoded.ID}, direction, nil
}

func normalizedActivityTypes(values []string) ([]objectref.Type, error) {
	seen := make(map[objectref.Type]struct{}, len(values))
	for _, value := range values {
		typ := objectref.Type(strings.TrimSpace(value))
		if !objectref.Known(typ) || !typ.Has(objectref.CapActivity) {
			return nil, fmt.Errorf("invalid activity type: %q", value)
		}
		seen[typ] = struct{}{}
	}
	types := make([]objectref.Type, 0, len(seen))
	for typ := range seen {
		types = append(types, typ)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	return types, nil
}

func normalizedActivityActions(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		action := strings.TrimSpace(value)
		if !activityActionPattern.MatchString(action) {
			return nil, fmt.Errorf("invalid activity action: %q", value)
		}
		seen[action] = struct{}{}
	}
	actions := make([]string, 0, len(seen))
	for action := range seen {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	return actions, nil
}

func activityScopeFingerprint(filters url.Values) string {
	sum := sha256.Sum256([]byte(filters.Encode()))
	return hex.EncodeToString(sum[:16])
}

func activityURL(filters url.Values) string {
	path := "/activity"
	for _, typ := range filters["type"] {
		if objectref.Type(typ).AdminOnly() && typ != "item" {
			path = "/settings/activity/history"
		}
	}
	if encoded := filters.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func viewAllActivityURL(hasOlder bool, filters url.Values, allowed bool) string {
	if !hasOlder || !allowed {
		return ""
	}
	return activityURL(filters)
}

func cloneValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, entries := range values {
		clone[key] = append([]string(nil), entries...)
	}
	return clone
}

func singleValue(values url.Values, key string) (string, bool) {
	entries, ok := values[key]
	if !ok {
		return "", false
	}
	if len(entries) != 1 || entries[0] == "" {
		return "", true
	}
	return entries[0], true
}

func rejectEmbeddedPage(w http.ResponseWriter, r *http.Request) bool {
	if _, exists := r.URL.Query()["page"]; exists {
		http.Error(w, "page is not supported for embedded activity", http.StatusBadRequest)
		return true
	}
	return false
}

func activityUser(w http.ResponseWriter, r *http.Request) (*middleware.UserInfo, bool) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || u.CompanyID <= 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return u, true
}

func activityActor(u *middleware.UserInfo) conversion.Actor {
	return conversion.Actor{ID: u.ID, CompanyID: u.CompanyID, Role: u.Role}
}

func handleActivityConversionError(w http.ResponseWriter, err error) {
	if errors.Is(err, conversion.ErrForbidden) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if errors.Is(err, conversion.ErrNotFound) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func activityIcon(action string) string {
	switch action {
	case "created", "type_created", "status_created", "field_created", "tag_created", "user_created":
		return "+"
	case "updated", "type_updated", "status_updated", "field_updated", "tag_updated", "user_updated", "settings_updated":
		return "✎"
	case "deleted", "type_deleted", "status_deleted", "field_deleted", "tag_deleted", "contact_deleted":
		return "🗑"
	case "archived":
		return "📦"
	case "status_changed":
		return "↻"
	case "scheduled", "rescheduled", "dispatched":
		return "📅"
	case "tag_attached", "tag_detached":
		return "#"
	case "file_uploaded", "file_deleted":
		return "📎"
	case "comment_added", "comment_deleted":
		return "✍"
	case "clocked_in":
		return "▶"
	case "clocked_out":
		return "■"
	case "payment_recorded", "payment_deleted":
		return "$"
	case "user_disabled":
		return "🚫"
	case "user_enabled":
		return "✔"
	case "password_reset", "password_changed":
		return "🔒"
	case "welcome_resent":
		return "📧"
	case "subtask_completed":
		return "☑"
	case "subtask_uncompleted":
		return "☐"
	case "logged_in":
		return "🔑"
	case "logged_out":
		return "🚪"
	case "logo_uploaded":
		return "🖼"
	case "restored":
		return "↩"
	case "contact_created", "contact_updated":
		return "👤"
	case "location_created", "location_updated", "location_deleted":
		return "📍"
	default:
		return "●"
	}
}
