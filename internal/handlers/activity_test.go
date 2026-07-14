package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
)

type activityListFake struct {
	page     services.ActivityPage
	err      error
	requests []services.ActivityListRequest
}

func (f *activityListFake) List(_ context.Context, request services.ActivityListRequest) (services.ActivityPage, error) {
	f.requests = append(f.requests, request)
	return f.page, f.err
}

type activityResolverFake struct {
	resolution services.ActivityResolution
	err        error
	calls      int
	entries    []services.ActivityEntry
}

type activityPolicyFake struct{ allow bool }

func (f activityPolicyFake) CanAccessObject(context.Context, int64, string, objectref.Ref, services.PolicyAction) bool {
	return f.allow
}

type activityConversionFake struct {
	estimateID int64
	err        error
	calls      []objectref.Ref
}

func (f *activityConversionFake) ActivityEstimateID(_ context.Context, _ conversion.Actor, ref objectref.Ref) (int64, error) {
	f.calls = append(f.calls, ref)
	return f.estimateID, f.err
}

func (f *activityResolverFake) Resolve(_ context.Context, _ int64, _ services.ActivityViewer, entries []services.ActivityEntry) (services.ActivityResolution, error) {
	f.calls++
	f.entries = append([]services.ActivityEntry(nil), entries...)
	return f.resolution, f.err
}

func TestActivityCursorRoundTripAndValidation(t *testing.T) {
	fingerprint := activityScopeFingerprint(url.Values{"type": {"customer"}})
	want := services.ActivityCursor{CreatedAt: time.Date(2026, 7, 14, 12, 13, 14, 123, time.FixedZone("test", -4*60*60)), ID: 42}
	encoded := encodeActivityCursor(want, services.ActivityOlder, fingerprint)
	got, direction, err := decodeActivityCursor(encoded, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || got.ID != want.ID || direction != services.ActivityOlder {
		t.Fatalf("decoded cursor = %#v/%q, want %#v/older", got, direction, want)
	}

	for _, invalid := range []string{"", "not-base64", strings.Repeat("a", maxActivityCursorSize+1)} {
		if _, _, err = decodeActivityCursor(invalid, fingerprint); err == nil {
			t.Fatalf("decodeActivityCursor(%q) error = nil", invalid)
		}
	}
	if _, _, err = decodeActivityCursor(encoded, activityScopeFingerprint(url.Values{"type": {"job"}})); err == nil {
		t.Fatal("cursor was accepted for a different filter scope")
	}
}

func TestActivityCursorDirectionIsBoundIntoNavigationURL(t *testing.T) {
	fingerprint := activityScopeFingerprint(nil)
	cursor := services.ActivityCursor{CreatedAt: time.Now(), ID: 7}
	for _, direction := range []services.ActivityDirection{services.ActivityOlder, services.ActivityNewer} {
		value := encodeActivityCursor(cursor, direction, fingerprint)
		_, got, err := decodeActivityCursor(value, fingerprint)
		if err != nil || got != direction {
			t.Fatalf("direction %q decoded as %q, err=%v", direction, got, err)
		}
	}
}

func TestActivityRowsUsesMetadataThenResolverThenFallback(t *testing.T) {
	customer := objectref.New(objectref.TypeCustomer, 7)
	job := objectref.New(objectref.TypeJob, 9)
	createdAt := time.Date(2026, time.June, 29, 14, 0, 0, 0, time.UTC)
	resolution := services.ActivityResolution{
		ActorNames: map[int64]string{1: "Current Actor"},
		Targets: map[objectref.Ref]services.ActivityTargetResolution{
			customer: {DisplayName: "Current Customer", URL: "/customers/7"},
			job:      {Err: context.Canceled},
		},
	}
	rows := activityRows(context.Background(), []services.ActivityEntry{
		{ID: 1, ActorID: 1, Action: "updated", Target: customer, Metadata: `{"actor_name":"Metadata Actor","entity_name":"Metadata Customer"}`, CreatedAt: createdAt},
		{ID: 2, ActorID: 1, Action: "updated", Target: customer, CreatedAt: createdAt},
		{ID: 3, ActorID: 2, Action: "deleted", Target: job, HistoricalTarget: "Deleted Job", CreatedAt: createdAt},
		{ID: 4, ActorID: 3, Action: "deleted", Target: job, CreatedAt: createdAt},
	}, resolution)

	if rows[0].ActorName != "Metadata Actor" || rows[0].EntityName != "Metadata Customer" || rows[0].EntityURL != "/customers/7" {
		t.Fatalf("metadata precedence row = %#v", rows[0])
	}
	if rows[1].ActorName != "Current Actor" || rows[1].EntityName != "Current Customer" {
		t.Fatalf("resolver precedence row = %#v", rows[1])
	}
	if rows[2].ActorName != "User #2" || rows[2].EntityName != "Deleted Job" || rows[2].EntityURL != "" {
		t.Fatalf("historical fallback row = %#v", rows[2])
	}
	if rows[3].EntityName != "job #9" || rows[3].EntityURL != "" {
		t.Fatalf("typed fallback row = %#v", rows[3])
	}
}

func TestEmbeddedTypeActivityIsBoundedResolvedOnceAndAlwaysFragment(t *testing.T) {
	entry := services.ActivityEntry{ID: 1, ActorID: 2, Action: "created", Target: objectref.New(objectref.TypeCustomer, 3), CreatedAt: time.Now()}
	lister := &activityListFake{page: services.ActivityPage{Entries: []services.ActivityEntry{entry}, HasOlder: true}}
	resolver := &activityResolverFake{resolution: services.ActivityResolution{
		ActorNames: map[int64]string{2: "Actor"},
		Targets:    map[objectref.Ref]services.ActivityTargetResolution{entry.Target: {DisplayName: "Customer", URL: "/customers/3"}},
	}}
	h := NewActivityHandler(lister, resolver, nil, nil)
	request := requestWithActivityUser("/customers/activity", "dispatcher")
	request.Header.Set("HX-Request", "true")
	recorder := httptest.NewRecorder()

	h.ListByType(objectref.TypeCustomer).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(lister.requests) != 1 || lister.requests[0].Page.Limit != embeddedActivityLimit {
		t.Fatalf("list requests = %#v", lister.requests)
	}
	if resolver.calls != 1 || len(resolver.entries) != 1 {
		t.Fatalf("resolver calls=%d entries=%d", resolver.calls, len(resolver.entries))
	}
	body := recorder.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, "Page ") || strings.Contains(body, "entries)") {
		t.Fatalf("embedded endpoint rendered a page or totals: %s", body)
	}
	if !strings.Contains(body, `href="/activity?type=customer"`) || !strings.Contains(body, "View all activity") {
		t.Fatalf("safe view-all filter missing: %s", body)
	}
}

func TestEmbeddedActivityRejectsLegacyPage(t *testing.T) {
	h := NewActivityHandler(&activityListFake{}, &activityResolverFake{}, nil, nil)
	recorder := httptest.NewRecorder()
	h.ListByType(objectref.TypeCustomer).ServeHTTP(recorder, requestWithActivityUser("/customers/activity?page=2", "dispatcher"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestDispatcherCannotFilterAdminOnlyActivity(t *testing.T) {
	h := NewActivityHandler(&activityListFake{}, &activityResolverFake{}, nil, nil)
	request := requestWithActivityUser("/activity?type=user", "dispatcher")
	recorder := httptest.NewRecorder()
	h.ListAll(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestLegacyPageOneRedirectsToCanonicalFilters(t *testing.T) {
	lister := &activityListFake{}
	resolver := &activityResolverFake{}
	h := NewActivityHandler(lister, resolver, nil, nil)
	request := requestWithActivityUser("/activity?action=updated&type=job&page=1", "dispatcher")
	recorder := httptest.NewRecorder()
	h.ListAll(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/activity?action=updated&type=job" {
		t.Fatalf("redirect = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	if len(lister.requests) != 0 || resolver.calls != 0 {
		t.Fatalf("legacy redirect queried activity: requests=%#v resolver calls=%d", lister.requests, resolver.calls)
	}
}

func TestLegacyPageGreaterThanOneRedirectsToCanonicalFirstPageWithoutQuery(t *testing.T) {
	lister := &activityListFake{}
	resolver := &activityResolverFake{}
	h := NewActivityHandler(lister, resolver, nil, nil)
	request := requestWithActivityUser("/activity?action=updated&type=job&page=9&cursor=not-a-cursor", "dispatcher")
	recorder := httptest.NewRecorder()
	h.ListAll(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/activity?action=updated&type=job" {
		t.Fatalf("redirect = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	if len(lister.requests) != 0 || resolver.calls != 0 {
		t.Fatalf("legacy redirect queried activity: requests=%#v resolver calls=%d", lister.requests, resolver.calls)
	}
}

func TestTechnicianScheduleUsesScheduleScopeAndHasNoViewAllLink(t *testing.T) {
	lister := &activityListFake{page: services.ActivityPage{HasOlder: true}}
	h := NewActivityHandler(lister, &activityResolverFake{resolution: services.ActivityResolution{}}, nil, nil)
	recorder := httptest.NewRecorder()
	h.ListSchedule(recorder, requestWithActivityUser("/schedule/activity", "tech"))
	if recorder.Code != http.StatusOK || len(lister.requests) != 1 {
		t.Fatalf("status=%d requests=%#v", recorder.Code, lister.requests)
	}
	scope, ok := lister.requests[0].Scope.(services.ScheduleActivityScope)
	if !ok || scope.ViewerID != 1 || scope.ViewerRole != "tech" {
		t.Fatalf("schedule scope = %#v", lister.requests[0].Scope)
	}
	if strings.Contains(recorder.Body.String(), "View all activity") {
		t.Fatalf("technician schedule exposed view-all link: %s", recorder.Body.String())
	}
}

func TestConversionObjectActivityUsesResolvedRootScopeAndBoundedListForBothDocuments(t *testing.T) {
	documents := []objectref.Ref{objectref.New(objectref.TypeEstimate, 10), objectref.New(objectref.TypeInvoice, 20)}
	for _, document := range documents {
		t.Run(string(document.Type), func(t *testing.T) {
			lister := &activityListFake{page: services.ActivityPage{Entries: []services.ActivityEntry{{ID: 1, ActorID: 1, Target: document}}, HasOlder: true}}
			conversionSvc := &activityConversionFake{estimateID: 10}
			h := NewActivityHandler(lister, &activityResolverFake{resolution: services.ActivityResolution{}}, activityPolicyFake{allow: true}, conversionSvc)
			request := requestWithActivityUser("/ignored", "dispatcher")
			routeContext := chi.NewRouteContext()
			routeContext.URLParams.Add("id", "10")
			if document.Type == objectref.TypeInvoice {
				routeContext.URLParams = chi.RouteParams{}
				routeContext.URLParams.Add("id", "20")
			}
			request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
			recorder := httptest.NewRecorder()
			h.ListForObject(document.Type).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK || len(conversionSvc.calls) != 1 || conversionSvc.calls[0] != document {
				t.Fatalf("status=%d conversion calls=%#v", recorder.Code, conversionSvc.calls)
			}
			if len(lister.requests) != 1 || lister.requests[0].Page.Limit != objectActivityLimit {
				t.Fatalf("list requests = %#v", lister.requests)
			}
			scope, ok := lister.requests[0].Scope.(services.ConversionActivityScope)
			if !ok || scope.EstimateID != 10 || scope.ViewerID != 1 || scope.ViewerRole != "dispatcher" {
				t.Fatalf("object scope = %#v", lister.requests[0].Scope)
			}
			wantFilter := "conversion=1&amp;object_id=" + strconv.FormatInt(document.ID, 10) + "&amp;object_type=" + string(document.Type)
			if !strings.Contains(recorder.Body.String(), wantFilter) {
				t.Fatalf("conversion view-all filter changed: %s", recorder.Body.String())
			}
		})
	}
}

func TestStandaloneInvoiceWidgetUsesBoundedObjectActivityAndNonConversionViewAll(t *testing.T) {
	invoice := objectref.New(objectref.TypeInvoice, 20)
	lister := &activityListFake{page: services.ActivityPage{
		Entries:  []services.ActivityEntry{{ID: 1, ActorID: 1, Target: invoice}},
		HasOlder: true,
	}}
	conversionSvc := &activityConversionFake{err: conversion.ErrNotFound}
	h := NewActivityHandler(lister, &activityResolverFake{resolution: services.ActivityResolution{}}, activityPolicyFake{allow: true}, conversionSvc)
	recorder := httptest.NewRecorder()
	h.ListForObject(objectref.TypeInvoice).ServeHTTP(recorder, requestForActivityObject("/invoices/20/activity", "dispatcher", 20))

	if recorder.Code != http.StatusOK || len(conversionSvc.calls) != 1 || conversionSvc.calls[0] != invoice {
		t.Fatalf("status=%d conversion calls=%#v", recorder.Code, conversionSvc.calls)
	}
	if len(lister.requests) != 1 || lister.requests[0].Page.Limit != objectActivityLimit {
		t.Fatalf("list requests = %#v", lister.requests)
	}
	scope, ok := lister.requests[0].Scope.(services.ObjectActivityScope)
	if !ok || len(scope.Refs) != 1 || scope.Refs[0] != invoice {
		t.Fatalf("standalone invoice scope = %#v", lister.requests[0].Scope)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `href="/activity?object_id=20&amp;object_type=invoice"`) || strings.Contains(body, "conversion=1") {
		t.Fatalf("standalone invoice view-all URL is not exact and non-conversion: %s", body)
	}
}

func TestInvoiceAndEstimateConversionErrorsRetainStatusMapping(t *testing.T) {
	tests := []struct {
		name       string
		objectType objectref.Type
		err        error
		wantStatus int
	}{
		{name: "invoice forbidden", objectType: objectref.TypeInvoice, err: conversion.ErrForbidden, wantStatus: http.StatusForbidden},
		{name: "invoice internal", objectType: objectref.TypeInvoice, err: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError},
		{name: "estimate not found", objectType: objectref.TypeEstimate, err: conversion.ErrNotFound, wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lister := &activityListFake{}
			h := NewActivityHandler(lister, &activityResolverFake{}, activityPolicyFake{allow: true}, &activityConversionFake{err: tt.err})
			recorder := httptest.NewRecorder()
			h.ListForObject(tt.objectType).ServeHTTP(recorder, requestForActivityObject("/ignored", "dispatcher", 20))
			if recorder.Code != tt.wantStatus || len(lister.requests) != 0 {
				t.Fatalf("status=%d, want %d; list requests=%#v", recorder.Code, tt.wantStatus, lister.requests)
			}
		})
	}
}

func TestExplicitStandaloneInvoiceConversionFilterReturnsNotFoundWithoutBroadening(t *testing.T) {
	lister := &activityListFake{}
	conversionSvc := &activityConversionFake{err: conversion.ErrNotFound}
	h := NewActivityHandler(lister, &activityResolverFake{}, activityPolicyFake{allow: true}, conversionSvc)
	recorder := httptest.NewRecorder()
	h.ListAll(recorder, requestWithActivityUser("/activity?conversion=1&object_id=20&object_type=invoice", "dispatcher"))
	if recorder.Code != http.StatusNotFound || len(lister.requests) != 0 {
		t.Fatalf("status=%d, want 404; list requests=%#v", recorder.Code, lister.requests)
	}
}

func TestDedicatedConversionScopeKeepsOriginalFilterFingerprint(t *testing.T) {
	conversionSvc := &activityConversionFake{estimateID: 10}
	h := NewActivityHandler(&activityListFake{}, &activityResolverFake{}, activityPolicyFake{allow: true}, conversionSvc)
	values := url.Values{"conversion": {"1"}, "object_type": {"invoice"}, "object_id": {"20"}}
	user := &middleware.UserInfo{ID: 4, CompanyID: 2, Role: "dispatcher"}
	parsed, err := h.parseIndexRequest(context.Background(), values, user)
	if err != nil {
		t.Fatal(err)
	}
	scope, ok := parsed.scope.(services.ConversionActivityScope)
	if !ok || scope.EstimateID != 10 || scope.ViewerID != 4 || scope.ViewerRole != "dispatcher" {
		t.Fatalf("conversion scope = %#v", parsed.scope)
	}
	if got := parsed.filters.Encode(); got != values.Encode() {
		t.Fatalf("canonical conversion filters = %q, want %q", got, values.Encode())
	}
	if parsed.fingerprint != activityScopeFingerprint(values) {
		t.Fatalf("conversion fingerprint changed: %q", parsed.fingerprint)
	}
}

func TestObjectActivityPolicyDenialHappensBeforeList(t *testing.T) {
	lister := &activityListFake{}
	h := NewActivityHandler(lister, &activityResolverFake{}, activityPolicyFake{}, nil)
	request := requestWithActivityUser("/ignored", "dispatcher")
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", "7")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	recorder := httptest.NewRecorder()
	h.ListForObject(objectref.TypeCustomer).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || len(lister.requests) != 0 {
		t.Fatalf("status=%d requests=%#v", recorder.Code, lister.requests)
	}
}

func requestWithActivityUser(target, role string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	user := &middleware.UserInfo{ID: 1, CompanyID: 2, Role: role}
	return request.WithContext(context.WithValue(request.Context(), middleware.UserKey, user))
}

func requestForActivityObject(target, role string, id int64) *http.Request {
	request := requestWithActivityUser(target, role)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", strconv.FormatInt(id, 10))
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}
