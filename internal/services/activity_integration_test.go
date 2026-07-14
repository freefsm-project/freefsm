package services

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/activitylog"
	"github.com/freefsm-project/freefsm/internal/ent/enttest"
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
	if _, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: TypeActivityScope{Types: []objectref.Type{"legacy"}}, Page: ActivityPageRequest{Limit: 10}}); err == nil {
		t.Fatal("List unknown type error = nil")
	}
	if _, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: TypeActivityScope{}, Page: ActivityPageRequest{Limit: 10}}); err == nil {
		t.Fatal("List empty types error = nil")
	}
	if _, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: TypeActivityScope{Types: []objectref.Type{objectref.TypeJob}, Actions: []string{""}}, Page: ActivityPageRequest{Limit: 10}}); err == nil {
		t.Fatal("List blank action error = nil")
	}

	svc = NewActivityService(nil, nil)
	if err := svc.Record(ctx, 1, 1, "created", objectref.New(objectref.TypeCustomer, 1), nil); err == nil {
		t.Fatal("Record without directory error = nil")
	}

	unsupported := &objectref.FakeDirectory{Descriptors: map[objectref.Type]objectref.Descriptor{
		objectref.TypeCustomer: {Type: objectref.TypeCustomer},
	}}
	svc = NewActivityService(nil, unsupported)
	if _, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: ObjectActivityScope{Refs: []objectref.Ref{objectref.New(objectref.TypeCustomer, 1)}}, Page: ActivityPageRequest{Limit: 10}}); err == nil {
		t.Fatal("List unsupported type error = nil")
	}
}

func TestActivityServiceListRejectsInvalidOptions(t *testing.T) {
	ctx := context.Background()
	svc := NewActivityService(nil, &objectref.FakeDirectory{})
	validCursor := &ActivityCursor{CreatedAt: time.Now(), ID: 1}
	valid := ActivityListRequest{CompanyID: 1, Scope: TenantActivityScope{}, Page: ActivityPageRequest{Limit: 10}}

	tests := []struct {
		name   string
		mutate func(*ActivityListRequest)
	}{
		{"missing company", func(r *ActivityListRequest) { r.CompanyID = 0 }},
		{"missing scope", func(r *ActivityListRequest) { r.Scope = nil }},
		{"negative limit", func(r *ActivityListRequest) { r.Page.Limit = -1 }},
		{"limit over maximum", func(r *ActivityListRequest) { r.Page.Limit = MaxActivityPageLimit + 1 }},
		{"unknown direction", func(r *ActivityListRequest) { r.Page.Direction = "sideways" }},
		{"newer without cursor", func(r *ActivityListRequest) { r.Page.Direction = ActivityNewer }},
		{"cursor without time", func(r *ActivityListRequest) { r.Page.Cursor = &ActivityCursor{ID: 1} }},
		{"cursor without id", func(r *ActivityListRequest) { r.Page.Cursor = &ActivityCursor{CreatedAt: time.Now()} }},
		{"empty types", func(r *ActivityListRequest) { r.Scope = TypeActivityScope{} }},
		{"unknown type", func(r *ActivityListRequest) { r.Scope = TypeActivityScope{Types: []objectref.Type{"unknown"}} }},
		{"blank action", func(r *ActivityListRequest) {
			r.Scope = TypeActivityScope{Types: []objectref.Type{objectref.TypeJob}, Actions: []string{" "}}
		}},
		{"empty refs", func(r *ActivityListRequest) { r.Scope = ObjectActivityScope{} }},
		{"invalid ref", func(r *ActivityListRequest) {
			r.Scope = ObjectActivityScope{Refs: []objectref.Ref{objectref.New(objectref.TypeJob, 0)}}
		}},
		{"schedule viewer id", func(r *ActivityListRequest) { r.Scope = ScheduleActivityScope{ViewerRole: "tech"} }},
		{"schedule role", func(r *ActivityListRequest) { r.Scope = ScheduleActivityScope{ViewerID: 1, ViewerRole: "owner"} }},
		{"conversion estimate id", func(r *ActivityListRequest) {
			r.Scope = ConversionActivityScope{ViewerID: 1, ViewerRole: "admin"}
		}},
		{"conversion viewer id", func(r *ActivityListRequest) {
			r.Scope = ConversionActivityScope{EstimateID: 1, ViewerRole: "admin"}
		}},
		{"conversion role", func(r *ActivityListRequest) {
			r.Scope = ConversionActivityScope{EstimateID: 1, ViewerID: 1, ViewerRole: "owner"}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := valid
			request.Page.Cursor = nil
			tc.mutate(&request)
			if _, err := svc.List(ctx, request); err == nil {
				t.Fatal("List error = nil")
			}
		})
	}

	valid.Page.Direction = ActivityNewer
	valid.Page.Cursor = validCursor
	if _, err := svc.List(ctx, valid); err == nil || err.Error() != "activity client is required" {
		t.Fatalf("valid request with nil client error = %v", err)
	}
}

func TestConversionActivityPredicateUsesBoundedExistsSQL(t *testing.T) {
	table := entsql.Table(activitylog.Table)
	selector := entsql.Select(table.C("*")).From(table)
	conversionActivityPredicate(ConversionActivityScope{EstimateID: 7, ViewerID: 9, ViewerRole: "tech"})(selector)
	query, args := selector.Query()
	lowerQuery := strings.ToLower(query)
	if !strings.Contains(lowerQuery, "exists") || !strings.Contains(lowerQuery, "estimate_invoice_conversion_cycles") {
		t.Fatalf("conversion predicate query = %s", query)
	}
	if strings.Count(lowerQuery, "conversion_hidden_at") != 2 {
		t.Fatalf("conversion predicate does not require visible estimate and invoice targets: %s", query)
	}
	if strings.Contains(lowerQuery, "invoice_id in (") || strings.Contains(lowerQuery, "object_id in (") {
		t.Fatalf("conversion predicate materializes IDs: %s", query)
	}
	if len(args) > 12 {
		t.Fatalf("conversion predicate has %d arguments, want fixed bounded shape: %v", len(args), args)
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

	forObjectPage, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: ObjectActivityScope{Refs: []objectref.Ref{customer}}, Page: ActivityPageRequest{Limit: 10}})
	if err != nil {
		t.Fatalf("List object: %v", err)
	}
	forObject := forObjectPage.Entries
	if len(forObject) != 2 {
		t.Fatalf("ListForObject count = %d, want 2", len(forObject))
	}
	otherCompanyPage, err := svc.List(ctx, ActivityListRequest{CompanyID: 2, Scope: ObjectActivityScope{Refs: []objectref.Ref{customer}}, Page: ActivityPageRequest{Limit: 10}})
	if err != nil || len(otherCompanyPage.Entries) != 1 || otherCompanyPage.Entries[0].ActorID != 77 {
		t.Fatalf("second company activity = %#v, err=%v", otherCompanyPage.Entries, err)
	}
	for _, entry := range forObject {
		if entry.Target != customer || entry.HistoricalTarget != "" {
			t.Fatalf("customer entry target = %#v, historical = %q", entry.Target, entry.HistoricalTarget)
		}
	}

	created, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: TypeActivityScope{Types: []objectref.Type{objectref.TypeCustomer}, Actions: []string{"created"}}, Page: ActivityPageRequest{Limit: 10}})
	if err != nil {
		t.Fatalf("List type and actions: %v", err)
	}
	if len(created.Entries) != 1 || created.Entries[0].Action != "created" || created.Entries[0].Metadata != `{"name":"Missing customer"}` {
		t.Fatalf("created entries = %#v", created.Entries)
	}

	byTypes, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: TypeActivityScope{Types: []objectref.Type{objectref.TypeCustomer, objectref.TypeJob}}, Page: ActivityPageRequest{Limit: 10}})
	if err != nil {
		t.Fatalf("List types: %v", err)
	}
	if len(byTypes.Entries) != 3 {
		t.Fatalf("List types count = %d, want 3", len(byTypes.Entries))
	}
	all, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: TenantActivityScope{IncludeAdminOnly: true}, Page: ActivityPageRequest{Limit: 10}})
	if err != nil || len(all.Entries) != 3 {
		t.Fatalf("company-scoped List = %#v err=%v", all.Entries, err)
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

	page, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: TenantActivityScope{IncludeAdminOnly: true}, Page: ActivityPageRequest{Limit: 10}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("List count = %d, want 1", len(page.Entries))
	}
	if page.Entries[0].Target != (objectref.Ref{}) || page.Entries[0].HistoricalTarget != "removed_type #23" {
		t.Fatalf("legacy target = %#v, historical = %q", page.Entries[0].Target, page.Entries[0].HistoricalTarget)
	}
}

func TestActivityServiceKeysetListingIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewActivityService(client, objectref.NewEntDirectory(client))
	actor1 := client.User.Create().SetCompanyID(1).SetEmail("activity-keyset-a@example.test").SetPasswordHash("hash").SetName("A").SetRole("admin").SaveX(ctx)
	actor2 := client.User.Create().SetCompanyID(2).SetEmail("activity-keyset-b@example.test").SetPasswordHash("hash").SetName("B").SetRole("admin").SaveX(ctx)
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	var tenantIDs []int64
	for i := 0; i < 7; i++ {
		createdAt := base.Add(time.Duration(i/3) * time.Minute)
		row := client.ActivityLog.Create().
			SetCompanyID(1).
			SetActorID(actor1.ID).
			SetAction("created").
			SetObjectType(string(objectref.TypeCustomer)).
			SetObjectID(int64(i + 1)).
			SetCreatedAt(createdAt).
			SaveX(ctx)
		tenantIDs = append(tenantIDs, row.ID)
	}
	client.ActivityLog.Create().SetCompanyID(2).SetActorID(actor2.ID).SetAction("created").SetObjectType("customer").SetObjectID(1).SetCreatedAt(base.Add(time.Hour)).SaveX(ctx)

	request := ActivityListRequest{CompanyID: 1, Scope: TenantActivityScope{}, Page: ActivityPageRequest{Limit: 3}}
	first, err := svc.List(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	assertActivityIDs(t, first.Entries, tenantIDs[6], tenantIDs[5], tenantIDs[4])
	if !first.HasOlder || first.HasNewer || first.OlderCursor == nil || first.NewerCursor == nil {
		t.Fatalf("first page flags/cursors = %#v", first)
	}
	assertStrictActivityOrder(t, first.Entries)

	request.Page.Cursor = first.OlderCursor
	second, err := svc.List(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	assertActivityIDs(t, second.Entries, tenantIDs[3], tenantIDs[2], tenantIDs[1])
	if !second.HasOlder || !second.HasNewer {
		t.Fatalf("second page flags older=%v newer=%v", second.HasOlder, second.HasNewer)
	}
	seen := map[int64]bool{}
	for _, entry := range append(first.Entries, second.Entries...) {
		if seen[entry.ID] {
			t.Fatalf("duplicate activity id %d", entry.ID)
		}
		seen[entry.ID] = true
	}

	request.Page.Direction = ActivityNewer
	request.Page.Cursor = second.NewerCursor
	roundTrip, err := svc.List(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	assertActivityIDs(t, roundTrip.Entries, tenantIDs[6], tenantIDs[5], tenantIDs[4])
	assertStrictActivityOrder(t, roundTrip.Entries)
}

func TestActivityServiceScopedListingIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewActivityService(client, objectref.NewEntDirectory(client))
	actor := client.User.Create().SetCompanyID(1).SetEmail("activity-scopes@example.test").SetPasswordHash("hash").SetName("Actor").SetRole("admin").SaveX(ctx)
	other := client.User.Create().SetCompanyID(2).SetEmail("activity-scopes-other@example.test").SetPasswordHash("hash").SetName("Other").SetRole("admin").SaveX(ctx)
	createdAt := time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC)
	seed := func(companyID, actorID int64, action string, typ objectref.Type, objectID int64) int64 {
		t.Helper()
		return client.ActivityLog.Create().SetCompanyID(companyID).SetActorID(actorID).SetAction(action).SetObjectType(string(typ)).SetObjectID(objectID).SetCreatedAt(createdAt).SaveX(ctx).ID
	}
	customerCreated := seed(1, actor.ID, "created", objectref.TypeCustomer, 11)
	seed(1, actor.ID, "updated", objectref.TypeCustomer, 11)
	jobCreated := seed(1, actor.ID, "created", objectref.TypeJob, 22)
	estimate := seed(1, actor.ID, "converted", objectref.TypeEstimate, 31)
	invoice := seed(1, actor.ID, "created_from_estimate", objectref.TypeInvoice, 32)
	seed(1, actor.ID, "updated", objectref.TypeInvoice, 99)
	seed(1, actor.ID, "updated", objectref.TypeUser, actor.ID)
	seed(2, other.ID, "created", objectref.TypeCustomer, 11)

	typePage, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: TypeActivityScope{
		Types: []objectref.Type{objectref.TypeCustomer, objectref.TypeJob}, Actions: []string{"created"},
	}, Page: ActivityPageRequest{Limit: 20}})
	if err != nil {
		t.Fatal(err)
	}
	assertActivityIDSet(t, typePage.Entries, customerCreated, jobCreated)

	objectPage, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: ObjectActivityScope{Refs: []objectref.Ref{
		objectref.New(objectref.TypeEstimate, 31), objectref.New(objectref.TypeInvoice, 32),
	}}, Page: ActivityPageRequest{Limit: 20}})
	if err != nil {
		t.Fatal(err)
	}
	assertActivityIDSet(t, objectPage.Entries, estimate, invoice)

	nonAdmin, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: TenantActivityScope{}, Page: ActivityPageRequest{Limit: 20}})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range nonAdmin.Entries {
		if entry.Target.Type == objectref.TypeUser {
			t.Fatal("tenant scope exposed admin-only activity")
		}
	}
	admin, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: TenantActivityScope{IncludeAdminOnly: true}, Page: ActivityPageRequest{Limit: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if len(admin.Entries) != 7 {
		t.Fatalf("admin tenant entries = %d, want 7", len(admin.Entries))
	}
}

func TestActivityServiceScheduleAssignmentScopeIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewActivityService(client, objectref.NewEntDirectory(client))
	actor := client.User.Create().SetCompanyID(1).SetEmail("activity-schedule-actor@example.test").SetPasswordHash("hash").SetName("Actor").SetRole("admin").SaveX(ctx)
	tech := client.User.Create().SetCompanyID(1).SetEmail("activity-schedule-tech@example.test").SetPasswordHash("hash").SetName("Tech").SetRole("tech").SaveX(ctx)
	active := client.Job.Create().SetCompanyID(1).SetCustomerID(1).SetJobType("Active").SaveX(ctx)
	unassigned := client.Job.Create().SetCompanyID(1).SetCustomerID(1).SetJobType("Unassigned").SaveX(ctx)
	deletedAt := time.Now()
	archived := client.Job.Create().SetCompanyID(1).SetCustomerID(1).SetJobType("Archived").SetDeletedAt(deletedAt).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(active.ID).SetUserID(tech.ID).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(archived.ID).SetUserID(tech.ID).SaveX(ctx)

	seedSchedule := func(jobID int64, action string) int64 {
		return client.ActivityLog.Create().SetCompanyID(1).SetActorID(actor.ID).SetAction(action).SetObjectType("job").SetObjectID(jobID).SaveX(ctx).ID
	}
	activeID := seedSchedule(active.ID, "scheduled")
	seedSchedule(unassigned.ID, "rescheduled")
	seedSchedule(archived.ID, "dispatched")
	seedSchedule(active.ID, "updated")

	for _, role := range []string{"tech", "technician"} {
		page, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: ScheduleActivityScope{ViewerID: tech.ID, ViewerRole: role}, Page: ActivityPageRequest{Limit: 20}})
		if err != nil {
			t.Fatalf("role %s: %v", role, err)
		}
		assertActivityIDs(t, page.Entries, activeID)
	}
	admin, err := svc.List(ctx, ActivityListRequest{CompanyID: 1, Scope: ScheduleActivityScope{ViewerID: actor.ID, ViewerRole: "admin"}, Page: ActivityPageRequest{Limit: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if len(admin.Entries) != 3 {
		t.Fatalf("admin schedule entries = %d, want 3", len(admin.Entries))
	}
}

func TestActivityServiceConversionScopeIntegration(t *testing.T) {
	fixture := openActivityConversionTestClient(t)
	defer fixture.client.Close()

	ctx := context.Background()
	client := fixture.client
	svc := NewActivityService(client, objectref.NewEntDirectory(client))
	actor := client.User.Create().SetCompanyID(1).SetEmail("activity-conversion-actor@example.test").SetPasswordHash("hash").SetName("Actor").SetRole("admin").SaveX(ctx)
	tech := client.User.Create().SetCompanyID(1).SetEmail("activity-conversion-tech@example.test").SetPasswordHash("hash").SetName("Tech").SetRole("tech").SaveX(ctx)
	otherActor := client.User.Create().SetCompanyID(2).SetEmail("activity-conversion-other@example.test").SetPasswordHash("hash").SetName("Other").SetRole("admin").SaveX(ctx)
	assignedJob := client.Job.Create().SetCompanyID(1).SetCustomerID(1).SetJobType("Assigned").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(assignedJob.ID).SetUserID(tech.ID).SaveX(ctx)

	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	// The root estimate is hidden while its second conversion is active.
	estimate := client.Estimate.Create().SetCompanyID(1).SetCustomerID(1).SetJobID(assignedJob.ID).SetTitle("Root estimate").SetConversionHiddenAt(now).SaveX(ctx)
	firstInvoice := client.Invoice.Create().SetCompanyID(1).SetCustomerID(1).SetJobID(assignedJob.ID).SetInvoiceNumber(101).SetTitle("First conversion").SetInvoiceDate(now).SetDueDate(now).SaveX(ctx)
	// The reverted invoice remains on the technician's job, but is hidden by conversion policy.
	client.Invoice.UpdateOneID(firstInvoice.ID).SetConversionHiddenAt(now).SaveX(ctx)
	secondInvoice := client.Invoice.Create().SetCompanyID(1).SetCustomerID(1).SetJobID(assignedJob.ID).SetInvoiceNumber(102).SetTitle("Second conversion").SetInvoiceDate(now).SetDueDate(now).SaveX(ctx)
	if _, err := fixture.db.ExecContext(ctx, `INSERT INTO estimate_invoice_conversion_cycles(id,company_id,estimate_id,invoice_id,converted_at,reverted_at) VALUES
		('00000000-0000-0000-0000-000000000001',1,$1,$2,$4,$4),
		('00000000-0000-0000-0000-000000000002',1,$1,$3,$4,NULL)`, estimate.ID, firstInvoice.ID, secondInvoice.ID, now); err != nil {
		t.Fatal(err)
	}

	seed := func(companyID, actorID int64, typ objectref.Type, objectID int64, action string) int64 {
		t.Helper()
		return client.ActivityLog.Create().SetCompanyID(companyID).SetActorID(actorID).SetObjectType(string(typ)).SetObjectID(objectID).SetAction(action).SetCreatedAt(now).SaveX(ctx).ID
	}
	seed(1, actor.ID, objectref.TypeEstimate, estimate.ID, "created")
	secondOlder := seed(1, actor.ID, objectref.TypeInvoice, secondInvoice.ID, "created_from_estimate")
	secondMiddle := seed(1, actor.ID, objectref.TypeInvoice, secondInvoice.ID, "updated")
	secondNewer := seed(1, actor.ID, objectref.TypeInvoice, secondInvoice.ID, "sent")
	seed(1, actor.ID, objectref.TypeInvoice, firstInvoice.ID, "updated")
	seed(1, actor.ID, objectref.TypeInvoice, firstInvoice.ID, "reverted")
	seed(2, otherActor.ID, objectref.TypeEstimate, estimate.ID, "cross_tenant")

	officeRequest := ActivityListRequest{CompanyID: 1, Scope: ConversionActivityScope{
		EstimateID: estimate.ID, ViewerID: actor.ID, ViewerRole: "dispatcher",
	}, Page: ActivityPageRequest{Limit: 20}}
	office, err := svc.List(ctx, officeRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(office.Entries) != 6 {
		t.Fatalf("office conversion chain entries = %d, want 6", len(office.Entries))
	}
	wantTargets := map[objectref.Ref]bool{
		objectref.New(objectref.TypeEstimate, estimate.ID):     false,
		objectref.New(objectref.TypeInvoice, firstInvoice.ID):  false,
		objectref.New(objectref.TypeInvoice, secondInvoice.ID): false,
	}
	for _, entry := range office.Entries {
		if _, ok := wantTargets[entry.Target]; !ok {
			t.Fatalf("office received out-of-chain target %#v", entry.Target)
		}
		wantTargets[entry.Target] = true
		if entry.ActorID == otherActor.ID {
			t.Fatal("office conversion chain crossed tenant boundary")
		}
	}
	for target, seen := range wantTargets {
		if !seen {
			t.Fatalf("office did not receive chain target %#v", target)
		}
	}

	*fixture.logs = nil
	techRequest := ActivityListRequest{CompanyID: 1, Scope: ConversionActivityScope{
		EstimateID: estimate.ID, ViewerID: tech.ID, ViewerRole: "technician",
	}, Page: ActivityPageRequest{Limit: 2}}
	techPage, err := svc.List(ctx, techRequest)
	if err != nil {
		t.Fatal(err)
	}
	// Two newer unauthorized rows exist, so a post-query filter would underfill this page.
	assertActivityIDs(t, techPage.Entries, secondNewer, secondMiddle)
	if !techPage.HasOlder {
		t.Fatal("technician conversion page HasOlder = false, want true")
	}
	assertStrictActivityOrder(t, techPage.Entries)

	techRequest.Page.Cursor = techPage.OlderCursor
	older, err := svc.List(ctx, techRequest)
	if err != nil {
		t.Fatal(err)
	}
	assertActivityIDs(t, older.Entries, secondOlder)
	for _, page := range []ActivityPage{techPage, older} {
		for _, entry := range page.Entries {
			if entry.Target == objectref.New(objectref.TypeEstimate, estimate.ID) || entry.Target == objectref.New(objectref.TypeInvoice, firstInvoice.ID) {
				t.Fatal("technician received activity for conversion-hidden document")
			}
		}
	}
	techAliasRequest := techRequest
	techAliasRequest.Scope = ConversionActivityScope{EstimateID: estimate.ID, ViewerID: tech.ID, ViewerRole: "tech"}
	techAliasRequest.Page = ActivityPageRequest{Limit: 2}
	techAlias, err := svc.List(ctx, techAliasRequest)
	if err != nil {
		t.Fatal(err)
	}
	assertActivityIDs(t, techAlias.Entries, secondNewer, secondMiddle)

	loggedSQL := strings.ToLower(strings.Join(*fixture.logs, "\n"))
	if !strings.Contains(loggedSQL, "exists") || !strings.Contains(loggedSQL, "estimate_invoice_conversion_cycles") || !strings.Contains(loggedSQL, "limit") {
		t.Fatalf("conversion request did not use bounded EXISTS/LIMIT SQL: %s", loggedSQL)
	}
	if strings.Contains(loggedSQL, "invoice_id in (") || strings.Contains(loggedSQL, "object_id in (") {
		t.Fatalf("conversion request materialized an ID list: %s", loggedSQL)
	}
}

type activityConversionTestClient struct {
	client *ent.Client
	db     *sql.DB
	logs   *[]string
}

func openActivityConversionTestClient(t *testing.T) activityConversionTestClient {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL activity integration tests")
	}
	schemaName := fmt.Sprintf("freefsm_activity_conversion_%d", time.Now().UnixNano())
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adminDB.Exec(`CREATE SCHEMA ` + schemaName); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.Exec(`DROP SCHEMA ` + schemaName + ` CASCADE`)
		_ = adminDB.Close()
	})
	schemaDSN, err := dsnWithSearchPath(dsn, schemaName)
	if err != nil {
		t.Fatal(err)
	}
	schemaDB, err := sql.Open("pgx", schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = schemaDB.Close() })
	logs := []string{}
	client := enttest.NewClient(t, enttest.WithOptions(
		ent.Driver(entsql.OpenDB(dialect.Postgres, schemaDB)),
		ent.Debug(),
		ent.Log(func(args ...any) { logs = append(logs, fmt.Sprint(args...)) }),
	))
	if _, err = schemaDB.Exec(`CREATE TABLE estimate_invoice_conversion_cycles (
		id UUID PRIMARY KEY,
		company_id BIGINT NOT NULL,
		estimate_id BIGINT NOT NULL,
		invoice_id BIGINT NOT NULL,
		converted_at TIMESTAMPTZ NOT NULL,
		reverted_at TIMESTAMPTZ,
		UNIQUE(company_id, invoice_id)
	);
	CREATE INDEX conversion_cycles_estimate_timeline
		ON estimate_invoice_conversion_cycles(company_id, estimate_id, converted_at, id)`); err != nil {
		t.Fatal(err)
	}
	return activityConversionTestClient{client: client, db: schemaDB, logs: &logs}
}

func assertActivityIDs(t *testing.T, entries []ActivityEntry, ids ...int64) {
	t.Helper()
	if len(entries) != len(ids) {
		t.Fatalf("activity ids count = %d, want %d: %#v", len(entries), len(ids), entries)
	}
	for i, id := range ids {
		if entries[i].ID != id {
			t.Fatalf("activity id[%d] = %d, want %d", i, entries[i].ID, id)
		}
	}
}

func assertActivityIDSet(t *testing.T, entries []ActivityEntry, ids ...int64) {
	t.Helper()
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	for _, entry := range entries {
		if !want[entry.ID] {
			t.Fatalf("unexpected activity id %d; want %v", entry.ID, ids)
		}
		delete(want, entry.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing activity ids: %v", want)
	}
}

func assertStrictActivityOrder(t *testing.T, entries []ActivityEntry) {
	t.Helper()
	for i := 1; i < len(entries); i++ {
		newer, older := entries[i-1], entries[i]
		if newer.CreatedAt.Before(older.CreatedAt) || (newer.CreatedAt.Equal(older.CreatedAt) && newer.ID <= older.ID) {
			t.Fatalf("activity order is not strictly descending at %d: %s/%d then %s/%d", i, newer.CreatedAt, newer.ID, older.CreatedAt, older.ID)
		}
	}
}
