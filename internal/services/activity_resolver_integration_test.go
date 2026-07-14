package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

func TestActivityResolverTenantScopeAndQueryBatchingIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	const localCompanyID int64 = 101
	const foreignCompanyID int64 = 202
	localActor := client.User.Create().
		SetCompanyID(localCompanyID).
		SetEmail("activity-local@example.test").
		SetPasswordHash("hash").
		SetName("Local Actor").
		SetRole("admin").
		SaveX(ctx)
	foreignActor := client.User.Create().
		SetCompanyID(foreignCompanyID).
		SetEmail("activity-foreign@example.test").
		SetPasswordHash("hash").
		SetName("Foreign Actor").
		SetRole("admin").
		SaveX(ctx)
	localCustomer := client.Customer.Create().SetCompanyID(localCompanyID).SetDisplayName("Current Local Name").SaveX(ctx)
	archivedCustomer := client.Customer.Create().SetCompanyID(localCompanyID).SetDisplayName("Archived Local Name").SetDeletedAt(time.Now()).SaveX(ctx)
	foreignCustomer := client.Customer.Create().SetCompanyID(foreignCompanyID).SetDisplayName("Foreign Name").SaveX(ctx)
	deletedCustomer := client.Customer.Create().SetCompanyID(localCompanyID).SetDisplayName("Deleted Name").SaveX(ctx)
	client.Customer.DeleteOne(deletedCustomer).ExecX(ctx)

	queryCount := 0
	client.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			queryCount++
			return next.Query(ctx, query)
		})
	}))
	resolver := NewActivityResolver(client)
	entries := []ActivityEntry{
		{ActorID: localActor.ID, Target: objectref.New(objectref.TypeCustomer, localCustomer.ID), Metadata: `{"actor_name":"Historical Actor","entity_name":"Historical Customer"}`},
		{ActorID: localActor.ID, Target: objectref.New(objectref.TypeCustomer, localCustomer.ID)},
		{ActorID: foreignActor.ID, Target: objectref.New(objectref.TypeCustomer, foreignCustomer.ID)},
		{ActorID: localActor.ID, Target: objectref.New(objectref.TypeCustomer, archivedCustomer.ID)},
		{ActorID: localActor.ID, Target: objectref.New(objectref.TypeCustomer, deletedCustomer.ID)},
	}

	got, err := resolver.Resolve(ctx, localCompanyID, ActivityViewer{ID: localActor.ID, Role: "admin"}, entries)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if queryCount != 2 {
		t.Fatalf("query count = %d, want 2 (one actors, one customer targets)", queryCount)
	}
	if got.ActorNames[localActor.ID] != "Local Actor" {
		t.Fatalf("local actor = %q", got.ActorNames[localActor.ID])
	}
	if _, ok := got.ActorNames[foreignActor.ID]; ok {
		t.Fatal("cross-tenant actor was resolved")
	}
	local := got.Targets[objectref.New(objectref.TypeCustomer, localCustomer.ID)]
	if local.DisplayName != "Current Local Name" || !local.Exists || !local.Readable || local.URL == "" {
		t.Fatalf("local target = %#v", local)
	}
	archived := got.Targets[objectref.New(objectref.TypeCustomer, archivedCustomer.ID)]
	if archived.DisplayName != "Archived Local Name" || !archived.Exists || !archived.Readable || archived.URL == "" {
		t.Fatalf("archived admin-readable target = %#v", archived)
	}
	for _, ref := range []objectref.Ref{
		objectref.New(objectref.TypeCustomer, foreignCustomer.ID),
		objectref.New(objectref.TypeCustomer, deletedCustomer.ID),
	} {
		resolution := got.Targets[ref]
		if resolution.Exists || resolution.Readable || resolution.URL != "" || resolution.DisplayName != "" {
			t.Fatalf("foreign/deleted target %v leaked data: %#v", ref, resolution)
		}
	}
}

func TestActivityResolverTimeEntryFormattingIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	const companyID int64 = 303
	actor := client.User.Create().
		SetCompanyID(companyID).
		SetEmail("activity-time@example.test").
		SetPasswordHash("hash").
		SetName("Time Actor").
		SetRole("admin").
		SaveX(ctx)
	client.CompanySettings.Create().
		SetCompanyID(companyID).
		SetTimezone("America/New_York").
		SetDateFormat("2006-01-02").
		SaveX(ctx)
	clockIn := time.Date(2026, time.July, 14, 13, 5, 0, 0, time.UTC)
	clockOut := clockIn.Add(time.Hour)
	entry := client.TimeEntry.Create().
		SetCompanyID(companyID).
		SetUserID(actor.ID).
		SetClockIn(clockIn).
		SetClockOut(clockOut).
		SaveX(ctx)

	queryCount := 0
	client.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			queryCount++
			return next.Query(ctx, query)
		})
	}))
	ref := objectref.New(objectref.TypeTimeEntry, entry.ID)
	got, err := NewActivityResolver(client).Resolve(ctx, companyID, ActivityViewer{ID: actor.ID, Role: "admin"}, []ActivityEntry{
		{ActorID: actor.ID, Target: ref},
		{ActorID: actor.ID, Target: ref},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if queryCount != 3 {
		t.Fatalf("query count = %d, want 3 (actors, settings, time entries)", queryCount)
	}
	if got.Targets[ref].DisplayName != "2026-07-14 09:05 — 10:05" {
		t.Fatalf("time entry name = %q", got.Targets[ref].DisplayName)
	}
}

func TestActivityResolverTechnicianScopeIsPageBoundedIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	const companyID int64 = 404
	tech := client.User.Create().SetCompanyID(companyID).SetEmail("activity-scope-tech@example.test").SetPasswordHash("hash").SetName("Scope Tech").SetRole("tech").SaveX(ctx)
	directCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Direct Customer").SetAssignedTo(tech.ID).SaveX(ctx)
	linkedCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Linked Customer").SaveX(ctx)
	directProject := client.Project.Create().SetCompanyID(companyID).SetCustomerID(directCustomer.ID).SetName("Direct Project").SaveX(ctx)
	linkedProject := client.Project.Create().SetCompanyID(companyID).SetCustomerID(linkedCustomer.ID).SetName("Linked Project").SaveX(ctx)
	assetType := client.AssetType.Create().SetCompanyID(companyID).SetName("Equipment").SaveX(ctx)
	directAsset := client.Asset.Create().SetCompanyID(companyID).SetCustomerID(directCustomer.ID).SetAssetTypeID(assetType.ID).SetName("Direct Asset").SaveX(ctx)
	linkedAsset := client.Asset.Create().SetCompanyID(companyID).SetCustomerID(linkedCustomer.ID).SetAssetTypeID(assetType.ID).SetName("Linked Asset").SaveX(ctx)
	pageJob := client.Job.Create().SetCompanyID(companyID).SetCustomerID(linkedCustomer.ID).SetProjectID(linkedProject.ID).SetAssetID(linkedAsset.ID).SetJobType("Page Job").SaveX(ctx)
	documentJob := client.Job.Create().SetCompanyID(companyID).SetCustomerID(linkedCustomer.ID).SetJobType("Document Job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(pageJob.ID).SetUserID(tech.ID).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(documentJob.ID).SetUserID(tech.ID).SaveX(ctx)
	estimate := client.Estimate.Create().SetCompanyID(companyID).SetCustomerID(linkedCustomer.ID).SetJobID(documentJob.ID).SetTitle("Page Estimate").SaveX(ctx)
	now := time.Now()
	invoice := client.Invoice.Create().SetCompanyID(companyID).SetCustomerID(linkedCustomer.ID).SetJobID(documentJob.ID).SetTitle("Page Invoice").SetInvoiceDate(now).SetDueDate(now.Add(30 * 24 * time.Hour)).SaveX(ctx)

	unrelatedJobIDs := make([]int64, 0, 40)
	for i := 0; i < 40; i++ {
		customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName(fmt.Sprintf("Unrelated Customer %d", i)).SaveX(ctx)
		job := client.Job.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetJobType(fmt.Sprintf("Unrelated Job %d", i)).SaveX(ctx)
		client.JobAssignment.Create().SetJobID(job.ID).SetUserID(tech.ID).SaveX(ctx)
		unrelatedJobIDs = append(unrelatedJobIDs, job.ID)
	}

	targetIDs := map[objectref.Type][]int64{
		objectref.TypeCustomer: {directCustomer.ID, linkedCustomer.ID},
		objectref.TypeJob:      {pageJob.ID},
		objectref.TypeProject:  {directProject.ID, linkedProject.ID},
		objectref.TypeEstimate: {estimate.ID},
		objectref.TypeInvoice:  {invoice.ID},
		objectref.TypeAsset:    {directAsset.ID, linkedAsset.ID},
	}
	queryCount := 0
	client.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			queryCount++
			return next.Query(ctx, query)
		})
	}))
	store := entActivityResolverStore{client: client}
	scope, err := store.technicianScope(ctx, companyID, tech.ID, targetIDs)
	if err != nil {
		t.Fatalf("technicianScope() error = %v", err)
	}
	if queryCount != 2 {
		t.Fatalf("scope query count = %d, want 2", queryCount)
	}
	if !scope.jobs[pageJob.ID] || !scope.jobs[documentJob.ID] || !scope.customers[linkedCustomer.ID] || !scope.projects[linkedProject.ID] || !scope.assets[linkedAsset.ID] || !scope.directCustomers[directCustomer.ID] {
		t.Fatalf("page scope missing expected relationships: %#v", scope)
	}
	for _, id := range unrelatedJobIDs {
		if scope.jobs[id] {
			t.Fatalf("unrelated assigned job %d was loaded into page scope", id)
		}
	}

	refs := []objectref.Ref{
		objectref.New(objectref.TypeCustomer, directCustomer.ID),
		objectref.New(objectref.TypeCustomer, linkedCustomer.ID),
		objectref.New(objectref.TypeJob, pageJob.ID),
		objectref.New(objectref.TypeProject, directProject.ID),
		objectref.New(objectref.TypeProject, linkedProject.ID),
		objectref.New(objectref.TypeEstimate, estimate.ID),
		objectref.New(objectref.TypeInvoice, invoice.ID),
		objectref.New(objectref.TypeAsset, directAsset.ID),
		objectref.New(objectref.TypeAsset, linkedAsset.ID),
	}
	entries := make([]ActivityEntry, 0, len(refs)*2)
	for _, ref := range refs {
		entries = append(entries, ActivityEntry{ActorID: tech.ID, Target: ref}, ActivityEntry{ActorID: tech.ID, Target: ref})
	}
	resolver := NewActivityResolver(client)
	policy := NewPolicyService(client, objectref.NewEntDirectory(client))
	for _, role := range []string{"tech", "technician"} {
		queryCount = 0
		resolved, err := resolver.Resolve(ctx, companyID, ActivityViewer{ID: tech.ID, Role: role}, entries)
		if err != nil {
			t.Fatalf("Resolve(%s) error = %v", role, err)
		}
		if queryCount != 9 {
			t.Fatalf("Resolve(%s) query count = %d, want 9 (actors, two scope, six target types)", role, queryCount)
		}
		for _, ref := range refs {
			want := policy.CanAccessObject(ctx, tech.ID, role, ref, PolicyRead)
			if got := resolved.Targets[ref].Readable; got != want {
				t.Fatalf("Resolve(%s) readability for %v = %v, scalar policy = %v", role, ref, got, want)
			}
		}
	}
}
