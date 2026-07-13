package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
)

type activityTestDirectory struct {
	names map[objectref.Ref]string
}

func (d activityTestDirectory) Parse(objectType string, objectID int64) (objectref.Ref, error) {
	return objectref.Parse(objectType, objectID)
}

func (d activityTestDirectory) Describe(typ objectref.Type) (objectref.Descriptor, bool) {
	return objectref.Describe(typ)
}

func (d activityTestDirectory) Supports(typ objectref.Type, capability objectref.Capability) bool {
	return typ.Has(capability)
}

func (d activityTestDirectory) Exists(context.Context, objectref.Ref, objectref.ExistenceMode) (bool, error) {
	return true, nil
}

func (d activityTestDirectory) TargetCompanyID(context.Context, objectref.Ref) (int64, error) {
	return 1, nil
}

func (d activityTestDirectory) DisplayName(_ context.Context, ref objectref.Ref) (string, error) {
	name, ok := d.names[ref]
	if !ok {
		return "", errors.New("not found")
	}
	return name, nil
}

func (d activityTestDirectory) URL(ref objectref.Ref) (string, bool) {
	return objectref.URL(ref)
}

func TestActivityPageNormalizesInvalidValues(t *testing.T) {
	for _, query := range []string{"", "?page=0", "?page=-2", "?page=nope"} {
		r := httptest.NewRequest("GET", "/activity"+query, nil)
		if got := activityPage(r); got != 1 {
			t.Fatalf("activityPage(%q) = %d, want 1", query, got)
		}
	}

	r := httptest.NewRequest("GET", "/activity?page=3", nil)
	if got := activityPage(r); got != 3 {
		t.Fatalf("activityPage() = %d, want 3", got)
	}
}

func TestEntriesToRowsResolvesTargetsWithoutDroppingMalformedEntries(t *testing.T) {
	customer := objectref.New(objectref.TypeCustomer, 7)
	missingJob := objectref.New(objectref.TypeJob, 9)
	h := &ActivityHandler{objects: activityTestDirectory{names: map[objectref.Ref]string{customer: "Directory Customer"}}}
	createdAt := time.Date(2026, time.June, 29, 14, 0, 0, 0, time.UTC)

	rows := h.entriesToRows(context.Background(), []services.ActivityEntry{
		{ID: 1, ActorID: 1, Action: "updated", Target: customer, HistoricalTarget: "Old Customer", Metadata: `{"actor_name":"Alex","entity_name":"Metadata Customer"}`, CreatedAt: createdAt},
		{ID: 2, ActorID: 1, Action: "updated", Target: customer, Metadata: `{"actor_name":"Alex"}`, CreatedAt: createdAt},
		{ID: 3, ActorID: 1, Action: "deleted", Target: missingJob, HistoricalTarget: "Deleted Job", Metadata: `{bad`, CreatedAt: createdAt},
		{ID: 4, ActorID: 1, Action: "deleted", Target: objectref.Ref{}, HistoricalTarget: "Legacy Target", Metadata: `{"actor_name":"Alex"}`, CreatedAt: createdAt},
		{ID: 5, ActorID: 1, Action: "deleted", Target: missingJob, Metadata: `{"actor_name":"Alex"}`, CreatedAt: createdAt},
	})

	if len(rows) != 5 {
		t.Fatalf("len(rows) = %d, want 5", len(rows))
	}
	if rows[0].EntityName != "Metadata Customer" {
		t.Fatalf("metadata name = %q", rows[0].EntityName)
	}
	if rows[1].EntityName != "Directory Customer" || rows[1].EntityURL != "/customers/7" {
		t.Fatalf("directory row = %#v", rows[1])
	}
	if rows[2].EntityName != "Deleted Job" {
		t.Fatalf("historical fallback = %q", rows[2].EntityName)
	}
	if rows[3].EntityName != "Legacy Target" || rows[3].EntityURL != "" {
		t.Fatalf("invalid target row = %#v", rows[3])
	}
	if rows[4].EntityName != "job #9" {
		t.Fatalf("known target fallback = %q", rows[4].EntityName)
	}
	if rows[0].CreatedAt != "Jun 29, 2026 2:00 PM" {
		t.Fatalf("created timestamp = %q, want %q", rows[0].CreatedAt, "Jun 29, 2026 2:00 PM")
	}
}
