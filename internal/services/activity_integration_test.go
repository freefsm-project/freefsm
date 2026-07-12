package services

import (
	"context"
	"testing"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

func TestActivityServiceRejectsInvalidInputs(t *testing.T) {
	ctx := context.Background()
	svc := NewActivityService(nil, &objectref.FakeDirectory{})

	if err := svc.Record(ctx, 1, 1, "created", objectref.Ref{Type: objectref.TypeCustomer}, nil); err == nil {
		t.Fatal("Record invalid target error = nil")
	}
	if err := svc.Record(ctx, 1, 1, " \t", objectref.New(objectref.TypeCustomer, 1), nil); err == nil {
		t.Fatal("Record empty action error = nil")
	}
	if _, _, err := svc.ListByType(ctx, 1, objectref.Type("legacy"), 0, 10); err == nil {
		t.Fatal("ListByType unknown type error = nil")
	}
	if _, _, err := svc.ListByTypes(ctx, 1, nil, 0, 10); err == nil {
		t.Fatal("ListByTypes empty types error = nil")
	}
	if _, _, err := svc.ListByTypeAndActions(ctx, 1, objectref.TypeJob, nil, 0, 10); err == nil {
		t.Fatal("ListByTypeAndActions empty actions error = nil")
	}
	if _, _, err := svc.ListByTypeAndActions(ctx, 1, objectref.TypeJob, []string{""}, 0, 10); err == nil {
		t.Fatal("ListByTypeAndActions blank action error = nil")
	}

	svc = NewActivityService(nil, nil)
	if err := svc.Record(ctx, 1, 1, "created", objectref.New(objectref.TypeCustomer, 1), nil); err == nil {
		t.Fatal("Record without directory error = nil")
	}

	unsupported := &objectref.FakeDirectory{Descriptors: map[objectref.Type]objectref.Descriptor{
		objectref.TypeCustomer: {Type: objectref.TypeCustomer},
	}}
	svc = NewActivityService(nil, unsupported)
	if _, err := svc.ListForObject(ctx, 1, objectref.New(objectref.TypeCustomer, 1), 10); err == nil {
		t.Fatal("ListForObject unsupported type error = nil")
	}
}

func TestMapActivityEntriesPreservesMalformedHistoricalTargets(t *testing.T) {
	rows := []*ent.ActivityLog{
		{ID: 1, ActorID: 2, Action: "created", ObjectType: "customer", ObjectID: 3, Metadata: "{}"},
		{ID: 4, ActorID: 5, Action: "legacy", ObjectType: "removed_type", ObjectID: 6, Metadata: "{}"},
		{ID: 7, ActorID: 8, Action: "invalid_id", ObjectType: "job", ObjectID: 0, Metadata: "{}"},
	}

	entries := mapActivityEntries(rows)
	if entries[0].Target != objectref.New(objectref.TypeCustomer, 3) || entries[0].HistoricalTarget != "" {
		t.Fatalf("valid entry target = %#v, historical = %q", entries[0].Target, entries[0].HistoricalTarget)
	}
	if entries[1].Target != (objectref.Ref{}) || entries[1].HistoricalTarget != "removed_type #6" {
		t.Fatalf("unknown entry target = %#v, historical = %q", entries[1].Target, entries[1].HistoricalTarget)
	}
	if entries[2].Target != (objectref.Ref{}) || entries[2].HistoricalTarget != "job #0" {
		t.Fatalf("invalid ID target = %#v, historical = %q", entries[2].Target, entries[2].HistoricalTarget)
	}
}

func TestActivityServiceTypedPersistenceAndFilteringIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewActivityService(client, objectref.NewEntDirectory(client))
	client.User.Create().SetCompanyID(1).SetEmail("activity-a@example.test").SetPasswordHash("hash").SetName("A").SetRole("admin").SetID(42).SaveX(ctx)
	client.User.Create().SetCompanyID(2).SetEmail("activity-b@example.test").SetPasswordHash("hash").SetName("B").SetRole("admin").SetID(77).SaveX(ctx)
	customer := objectref.New(objectref.TypeCustomer, 999999)
	job := objectref.New(objectref.TypeJob, 888888)

	if err := svc.Record(ctx, 1, 42, "  created  ", customer, map[string]interface{}{"name": "Missing customer"}); err != nil {
		t.Fatalf("Record missing target: %v", err)
	}
	if err := svc.Record(ctx, 1, 42, "updated", customer, nil); err != nil {
		t.Fatalf("Record customer update: %v", err)
	}
	if err := svc.Record(ctx, 1, 42, "created", job, nil); err != nil {
		t.Fatalf("Record job: %v", err)
	}
	if err := svc.Record(ctx, 2, 77, "created", customer, nil); err != nil {
		t.Fatalf("Record second company: %v", err)
	}
	if err := svc.Record(ctx, 1, 77, "cross-company", customer, nil); err == nil {
		t.Fatal("Record accepted actor from another company")
	}

	forObject, err := svc.ListForObject(ctx, 1, customer, 10)
	if err != nil {
		t.Fatalf("ListForObject: %v", err)
	}
	if len(forObject) != 2 {
		t.Fatalf("ListForObject count = %d, want 2", len(forObject))
	}
	otherCompany, err := svc.ListForObject(ctx, 2, customer, 10)
	if err != nil || len(otherCompany) != 1 || otherCompany[0].ActorID != 77 {
		t.Fatalf("second company activity = %#v, err=%v", otherCompany, err)
	}
	for _, entry := range forObject {
		if entry.Target != customer || entry.HistoricalTarget != "" {
			t.Fatalf("customer entry target = %#v, historical = %q", entry.Target, entry.HistoricalTarget)
		}
	}

	created, total, err := svc.ListByTypeAndActions(ctx, 1, objectref.TypeCustomer, []string{"created"}, 0, 10)
	if err != nil {
		t.Fatalf("ListByTypeAndActions: %v", err)
	}
	if total != 1 || len(created) != 1 || created[0].Action != "created" || created[0].Metadata != `{"name":"Missing customer"}` {
		t.Fatalf("created entries = %#v, total = %d", created, total)
	}

	byTypes, total, err := svc.ListByTypes(ctx, 1, []objectref.Type{objectref.TypeCustomer, objectref.TypeJob}, 0, 10)
	if err != nil {
		t.Fatalf("ListByTypes: %v", err)
	}
	if total != 3 || len(byTypes) != 3 {
		t.Fatalf("ListByTypes count = %d, total = %d, want 3", len(byTypes), total)
	}
	all, total, err := svc.ListAll(ctx, 1, 0, 10, true)
	if err != nil || total != 3 || len(all) != 3 {
		t.Fatalf("company-scoped ListAll = %#v total=%d err=%v", all, total, err)
	}
}

func TestActivityServiceMapsMalformedLegacyRowsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewActivityService(client, objectref.NewEntDirectory(client))
	client.ActivityLog.Create().
		SetCompanyID(1).
		SetActorID(1).
		SetAction("legacy").
		SetObjectType("removed_type").
		SetObjectID(23).
		SaveX(ctx)

	entries, total, err := svc.ListAll(ctx, 1, 0, 10, true)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if total != 1 || len(entries) != 1 {
		t.Fatalf("ListAll count = %d, total = %d, want 1", len(entries), total)
	}
	if entries[0].Target != (objectref.Ref{}) || entries[0].HistoricalTarget != "removed_type #23" {
		t.Fatalf("legacy target = %#v, historical = %q", entries[0].Target, entries[0].HistoricalTarget)
	}
}
