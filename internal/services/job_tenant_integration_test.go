package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	enthook "github.com/freefsm-project/freefsm/internal/ent/hook"
)

func TestJobCreateUsesTenantDefaultStatusAfterRenameIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 11
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Tenant customer").SaveX(ctx)
	wantStatusID := createJobDefaultStatus(t, ctx, client, companyID, "Ready for dispatch", 1)
	createJobDefaultStatus(t, ctx, client, 12, "Foreign default", 1)
	localUser := client.User.Create().SetCompanyID(companyID).SetEmail("local-job-user@example.test").SetPasswordHash("hash").SetName("Local user").SaveX(ctx)

	created, err := NewJobService(client).Create(ctx, companyID, JobCreateParams{CustomerID: customer.ID, JobType: "Install", Assignments: []JobAssignment{{UserID: localUser.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if created.CompanyID == nil || *created.CompanyID != companyID {
		t.Fatalf("company ID = %v, want %d", created.CompanyID, companyID)
	}
	assertStatusID(t, created.StatusID, wantStatusID)
	if got := client.JobAssignment.Query().CountX(ctx); got != 1 {
		t.Fatalf("assignment count = %d, want 1", got)
	}
}

func TestMobileJobQueriesAreCompanyScopedIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 13

	user := client.User.Create().SetCompanyID(companyID).SetEmail("scoped-jobs@example.test").SetPasswordHash("hash").SetName("Tech").SaveX(ctx)
	localCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Local").SaveX(ctx)
	foreignCustomer := client.Customer.Create().SetCompanyID(14).SetDisplayName("Foreign").SaveX(ctx)
	local := client.Job.Create().SetCompanyID(companyID).SetCustomerID(localCustomer.ID).SetJobType("Local").SaveX(ctx)
	archived := client.Job.Create().SetCompanyID(companyID).SetCustomerID(localCustomer.ID).SetJobType("Archived").SetDeletedAt(time.Now()).SaveX(ctx)
	foreign := client.Job.Create().SetCompanyID(14).SetCustomerID(foreignCustomer.ID).SetJobType("Foreign").SaveX(ctx)
	for _, jobID := range []int64{local.ID, archived.ID, foreign.ID} {
		client.JobAssignment.Create().SetJobID(jobID).SetUserID(user.ID).SaveX(ctx)
	}

	svc := NewJobService(client)
	all, err := svc.ListAllForCompany(ctx, companyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != local.ID {
		t.Fatalf("ListAllForCompany = %v, want only job %d", jobIDs(all), local.ID)
	}
	assigned, err := svc.ListAssignedAllForCompany(ctx, companyID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(assigned) != 1 || assigned[0].ID != local.ID {
		t.Fatalf("ListAssignedAllForCompany = %v, want only job %d", jobIDs(assigned), local.ID)
	}
}

func TestStatusQueryIsCompanyAndWorkflowScopedIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 15

	localWorkflow := client.StatusWorkflow.Create().SetCompanyID(companyID).SetName("Local jobs").SetObjectType("job").SaveX(ctx)
	projectWorkflow := client.StatusWorkflow.Create().SetCompanyID(companyID).SetName("Local projects").SetObjectType("project").SaveX(ctx)
	foreignWorkflow := client.StatusWorkflow.Create().SetCompanyID(16).SetName("Foreign jobs").SetObjectType("job").SaveX(ctx)
	local := client.Status.Create().SetCompanyID(companyID).SetWorkflowID(localWorkflow.ID).SetName("Local").SetCategoryKey("job:new").SaveX(ctx)
	client.Status.Create().SetCompanyID(companyID).SetWorkflowID(projectWorkflow.ID).SetName("Project").SetCategoryKey("project:new").SaveX(ctx)
	client.Status.Create().SetCompanyID(16).SetWorkflowID(foreignWorkflow.ID).SetName("Foreign").SetCategoryKey("job:new").SaveX(ctx)

	statuses, err := NewStatusService(client).ByObjectTypeForCompany(ctx, companyID, "job")
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].ID != local.ID {
		t.Fatalf("ByObjectTypeForCompany returned %d statuses, want only %d", len(statuses), local.ID)
	}
}

func jobIDs(jobs []*ent.Job) []int64 {
	ids := make([]int64, len(jobs))
	for i, job := range jobs {
		ids[i] = job.ID
	}
	return ids
}

func TestJobCreateRejectsCustomerOutsideAuthenticatedCompanyBeforeWriteIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 21
	createJobDefaultStatus(t, ctx, client, companyID, "New", 1)
	foreign := client.Customer.Create().SetCompanyID(22).SetDisplayName("Foreign").SaveX(ctx)
	companyless := client.Customer.Create().SetDisplayName("Companyless").SaveX(ctx)
	missingID := int64(987654321)

	for _, tc := range []struct {
		name       string
		companyID  int64
		customerID int64
		wantInput  bool
	}{
		{name: "foreign customer", companyID: companyID, customerID: foreign.ID, wantInput: true},
		{name: "companyless customer", companyID: companyID, customerID: companyless.ID, wantInput: true},
		{name: "missing customer", companyID: companyID, customerID: missingID, wantInput: true},
		{name: "zero customer", companyID: companyID, customerID: 0, wantInput: true},
		{name: "missing authenticated company", companyID: 0, customerID: foreign.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := client.Job.Query().CountX(ctx)
			_, err := NewJobService(client).Create(ctx, tc.companyID, JobCreateParams{CustomerID: tc.customerID, JobType: "Rejected"})
			if err == nil {
				t.Fatal("Create error = nil, want validation error")
			}
			if tc.wantInput && !errors.Is(err, ErrInvalidJobInput) {
				t.Fatalf("Create error = %v, want ErrInvalidJobInput", err)
			}
			if got := client.Job.Query().CountX(ctx); got != before {
				t.Fatalf("job count = %d, want %d", got, before)
			}
		})
	}
}

func TestJobCreateReturnsTypedStatusConfigurationErrorIntegration(t *testing.T) {
	for _, tc := range []struct {
		name      string
		defaults  int
		wantCause func(error) bool
	}{
		{name: "missing default", defaults: 0, wantCause: ent.IsNotFound},
		{name: "multiple defaults", defaults: 2, wantCause: ent.IsNotSingular},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := openPolicyTestClient(t)
			defer client.Close()
			ctx := context.Background()
			const companyID int64 = 31
			customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Status customer").SaveX(ctx)
			for i := 0; i < tc.defaults; i++ {
				createJobDefaultStatus(t, ctx, client, companyID, "Default", i+1)
			}

			_, err := NewJobService(client).Create(ctx, companyID, JobCreateParams{CustomerID: customer.ID, JobType: "Install"})
			var configurationError *StatusConfigurationError
			if !errors.As(err, &configurationError) {
				t.Fatalf("error = %T %v, want *StatusConfigurationError", err, err)
			}
			if configurationError.CompanyID != companyID || configurationError.ObjectType != "job" || configurationError.Category != "job:new" {
				t.Fatalf("configuration error context = %#v", configurationError)
			}
			if !tc.wantCause(err) {
				t.Fatalf("underlying cause = %v, want expected Ent query error", err)
			}
			if got := configurationError.Error(); got != "status configuration unavailable" {
				t.Fatalf("public error = %q, want safe generic message", got)
			}
		})
	}
}

func TestJobCreateKeepsInvalidRequestedStatusAsDomainValidationIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 35
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Status customer").SaveX(ctx)
	foreignStatusID := createJobDefaultStatus(t, ctx, client, 36, "Foreign", 1)

	_, err := NewJobService(client).Create(ctx, companyID, JobCreateParams{CustomerID: customer.ID, JobType: "Install", StatusID: foreignStatusID})
	if !errors.Is(err, ErrInvalidDocumentStatus) {
		t.Fatalf("Create error = %v, want ErrInvalidDocumentStatus", err)
	}
	var configurationError *StatusConfigurationError
	if errors.As(err, &configurationError) {
		t.Fatalf("invalid requested status was converted to configuration error: %v", err)
	}
}

func TestJobUpdateDoesNotResolveCreationStatusIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 41
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Update customer").SaveX(ctx)
	createJobDefaultStatus(t, ctx, client, companyID, "First", 1)
	createJobDefaultStatus(t, ctx, client, companyID, "Second", 2)
	job := client.Job.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetJobType("Repair").SaveX(ctx)
	notes := "ordinary update"

	updated, err := NewJobService(client).Update(ctx, companyID, job.ID, JobUpdateParams{Notes: &notes})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Notes != notes {
		t.Fatalf("notes = %q, want %q", updated.Notes, notes)
	}
}

func TestJobCreateRejectsForeignCompanyLinksAndAssignmentsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 51
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Local").SaveX(ctx)
	createJobDefaultStatus(t, ctx, client, companyID, "New", 1)
	foreignProject := client.Project.Create().SetCompanyID(52).SetCustomerID(customer.ID).SetName("Foreign project").SaveX(ctx)
	foreignLocation := client.Location.Create().SetCompanyID(52).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Foreign location").SaveX(ctx)
	foreignContact := client.CustomerContact.Create().SetCompanyID(52).SetCustomerID(customer.ID).SetFirstName("Foreign contact").SaveX(ctx)
	assetType := client.AssetType.Create().SetCompanyID(52).SetName("Foreign equipment").SaveX(ctx)
	foreignAsset := client.Asset.Create().SetCompanyID(52).SetCustomerID(customer.ID).SetAssetTypeID(assetType.ID).SetName("Foreign asset").SaveX(ctx)
	foreignUser := client.User.Create().SetCompanyID(52).SetEmail("foreign-job-user@example.test").SetPasswordHash("hash").SetName("Foreign user").SaveX(ctx)

	for _, tc := range []struct {
		name   string
		params JobCreateParams
		want   JobInputRelation
	}{
		{name: "foreign project", params: JobCreateParams{CustomerID: customer.ID, ProjectID: foreignProject.ID, JobType: "Rejected"}, want: JobInputRelationProject},
		{name: "foreign location", params: JobCreateParams{CustomerID: customer.ID, LocationID: foreignLocation.ID, JobType: "Rejected"}, want: JobInputRelationLocation},
		{name: "foreign contact", params: JobCreateParams{CustomerID: customer.ID, CustomerContactID: foreignContact.ID, JobType: "Rejected"}, want: JobInputRelationContact},
		{name: "foreign asset", params: JobCreateParams{CustomerID: customer.ID, AssetID: foreignAsset.ID, JobType: "Rejected"}, want: JobInputRelationAsset},
		{name: "foreign assignment", params: JobCreateParams{CustomerID: customer.ID, JobType: "Rejected", Assignments: []JobAssignment{{UserID: foreignUser.ID}}}, want: JobInputRelationAssignment},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := client.Job.Query().CountX(ctx)
			_, err := NewJobService(client).Create(ctx, companyID, tc.params)
			var inputErr *JobInputError
			if !errors.As(err, &inputErr) || inputErr.Reason() != JobInputReasonOwnershipMismatch || inputErr.Relation() != tc.want {
				t.Fatalf("Create error = %#v, want ownership mismatch for %q", err, tc.want)
			}
			if got := client.Job.Query().CountX(ctx); got != before {
				t.Fatalf("job count = %d, want %d", got, before)
			}
		})
	}
}

func TestJobCreateAcceptsCanonicalLegacyCompanylessChildLinksIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 55
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Legacy customer").SaveX(ctx)
	createJobDefaultStatus(t, ctx, client, companyID, "New", 1)
	projectWorkflow := client.StatusWorkflow.Create().SetCompanyID(companyID).SetName("Project workflow").SetObjectType("project").SaveX(ctx)
	client.Status.Create().SetCompanyID(companyID).SetWorkflowID(projectWorkflow.ID).SetName("New").SetCategoryKey("project:new").SetIsCategoryDefault(true).SaveX(ctx)
	assetType := client.AssetType.Create().SetCompanyID(companyID).SetName("Equipment").SaveX(ctx)

	location, err := NewLocationService(client).CreateForCustomer(ctx, companyID, customer.ID, CustomerLocationCreateParams{Title: "Main"})
	if err != nil {
		t.Fatalf("create location: %v", err)
	}
	contact, err := NewCustomerContactService(client).Create(ctx, companyID, customer.ID, ContactCreateParams{FirstName: "Contact"})
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}
	project, err := NewProjectService(client).Create(ctx, companyID, ProjectCreateParams{CustomerID: customer.ID, LocationID: location.ID, Name: "Project"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	asset, err := NewAssetService(client).Create(ctx, companyID, AssetCreateParams{CustomerID: customer.ID, LocationID: &location.ID, AssetTypeID: assetType.ID, Name: "Asset"})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	client.Location.UpdateOneID(location.ID).ClearCompanyID().SaveX(ctx)
	client.CustomerContact.UpdateOneID(contact.ID).ClearCompanyID().SaveX(ctx)
	client.Project.UpdateOneID(project.ID).ClearCompanyID().SaveX(ctx)
	client.Asset.UpdateOneID(asset.ID).ClearCompanyID().SaveX(ctx)
	location = client.Location.GetX(ctx, location.ID)
	contact = client.CustomerContact.GetX(ctx, contact.ID)
	project = client.Project.GetX(ctx, project.ID)
	asset = client.Asset.GetX(ctx, asset.ID)
	if location.CompanyID != nil || contact.CompanyID != nil || project.CompanyID != nil || asset.CompanyID != nil {
		t.Fatalf("fixture is not legacy companyless: location=%v contact=%v project=%v asset=%v", location.CompanyID, contact.CompanyID, project.CompanyID, asset.CompanyID)
	}

	created, err := NewJobService(client).Create(ctx, companyID, JobCreateParams{
		CustomerID: customer.ID, ProjectID: project.ID, LocationID: location.ID,
		CustomerContactID: contact.ID, AssetID: asset.ID, JobType: "Repair",
	})
	if err != nil {
		t.Fatalf("Create with canonical legacy links: %v", err)
	}
	if created.CustomerID != customer.ID {
		t.Fatalf("customer ID = %d, want %d", created.CustomerID, customer.ID)
	}

	otherCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Other customer").SaveX(ctx)
	otherLocation, err := NewLocationService(client).CreateForCustomer(ctx, companyID, otherCustomer.ID, CustomerLocationCreateParams{Title: "Other"})
	if err != nil {
		t.Fatalf("create other location: %v", err)
	}
	otherContact, err := NewCustomerContactService(client).Create(ctx, companyID, otherCustomer.ID, ContactCreateParams{FirstName: "Other"})
	if err != nil {
		t.Fatalf("create other contact: %v", err)
	}
	otherProject, err := NewProjectService(client).Create(ctx, companyID, ProjectCreateParams{CustomerID: otherCustomer.ID, Name: "Other"})
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	otherAsset, err := NewAssetService(client).Create(ctx, companyID, AssetCreateParams{CustomerID: otherCustomer.ID, AssetTypeID: assetType.ID, Name: "Other"})
	if err != nil {
		t.Fatalf("create other asset: %v", err)
	}
	client.Location.UpdateOneID(otherLocation.ID).ClearCompanyID().SaveX(ctx)
	client.CustomerContact.UpdateOneID(otherContact.ID).ClearCompanyID().SaveX(ctx)
	client.Project.UpdateOneID(otherProject.ID).ClearCompanyID().SaveX(ctx)
	client.Asset.UpdateOneID(otherAsset.ID).ClearCompanyID().SaveX(ctx)

	for _, tc := range []struct {
		name     string
		relation JobInputRelation
		params   JobCreateParams
	}{
		{name: "project", relation: JobInputRelationProject, params: JobCreateParams{CustomerID: customer.ID, ProjectID: otherProject.ID, JobType: "Rejected"}},
		{name: "location", relation: JobInputRelationLocation, params: JobCreateParams{CustomerID: customer.ID, LocationID: otherLocation.ID, JobType: "Rejected"}},
		{name: "contact", relation: JobInputRelationContact, params: JobCreateParams{CustomerID: customer.ID, CustomerContactID: otherContact.ID, JobType: "Rejected"}},
		{name: "asset", relation: JobInputRelationAsset, params: JobCreateParams{CustomerID: customer.ID, AssetID: otherAsset.ID, JobType: "Rejected"}},
	} {
		t.Run("companyless cross-customer "+tc.name, func(t *testing.T) {
			_, err := NewJobService(client).Create(ctx, companyID, tc.params)
			var inputErr *JobInputError
			if !errors.As(err, &inputErr) || inputErr.Relation() != tc.relation || inputErr.Reason() != JobInputReasonOwnershipMismatch {
				t.Fatalf("Create error = %#v, want ownership mismatch for %s", err, tc.relation)
			}
		})
	}
}

func TestJobCreateNextOccurrenceEnforcesTenantSourceAndAssignmentsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 61
	createJobDefaultStatus(t, ctx, client, companyID, "New", 1)
	localCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Local").SaveX(ctx)
	foreignCustomer := client.Customer.Create().SetCompanyID(62).SetDisplayName("Foreign").SaveX(ctx)
	foreignSource := client.Job.Create().SetCompanyID(62).SetCustomerID(foreignCustomer.ID).SetJobType("Foreign source").SetStartTime(time.Now()).SaveX(ctx)

	if _, err := NewJobService(client).CreateNextOccurrence(ctx, companyID, foreignSource.ID, time.Now().Add(24*time.Hour)); !errors.Is(err, ErrInvalidJobInput) {
		t.Fatalf("CreateNextOccurrence error = %v, want ErrInvalidJobInput", err)
	}

	foreignUser := client.User.Create().SetCompanyID(62).SetEmail("foreign-occurrence-user@example.test").SetPasswordHash("hash").SetName("Foreign user").SaveX(ctx)
	localSource := client.Job.Create().SetCompanyID(companyID).SetCustomerID(localCustomer.ID).SetJobType("Local source").SetStartTime(time.Now()).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(localSource.ID).SetUserID(foreignUser.ID).SaveX(ctx)
	before := client.Job.Query().CountX(ctx)
	if _, err := NewJobService(client).CreateNextOccurrence(ctx, companyID, localSource.ID, time.Now().Add(24*time.Hour)); !errors.Is(err, ErrInvalidJobInput) {
		t.Fatalf("CreateNextOccurrence error = %v, want assignment company error", err)
	}
	if got := client.Job.Query().CountX(ctx); got != before {
		t.Fatalf("job count = %d, want %d", got, before)
	}
}

func TestJobUpdateEnforcesTenantAndReplacementOwnershipIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 71
	localCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Local customer").SaveX(ctx)
	replacementCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Replacement customer").SaveX(ctx)
	archivedCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Archived customer").SetDeletedAt(time.Now()).SaveX(ctx)
	foreignCustomer := client.Customer.Create().SetCompanyID(72).SetDisplayName("Foreign customer").SaveX(ctx)
	localJob := client.Job.Create().SetCompanyID(companyID).SetCustomerID(localCustomer.ID).SetJobType("Local job").SaveX(ctx)
	foreignJob := client.Job.Create().SetCompanyID(72).SetCustomerID(foreignCustomer.ID).SetJobType("Foreign job").SetNotes("unchanged").SaveX(ctx)
	foreignProject := client.Project.Create().SetCompanyID(72).SetCustomerID(localCustomer.ID).SetName("Foreign project").SaveX(ctx)
	foreignLocation := client.Location.Create().SetCompanyID(72).SetObjectType("customer").SetObjectID(localCustomer.ID).SetTitle("Foreign location").SaveX(ctx)
	foreignContact := client.CustomerContact.Create().SetCompanyID(72).SetCustomerID(localCustomer.ID).SetFirstName("Foreign contact").SaveX(ctx)
	assetType := client.AssetType.Create().SetCompanyID(72).SetName("Equipment").SaveX(ctx)
	foreignAsset := client.Asset.Create().SetCompanyID(72).SetCustomerID(localCustomer.ID).SetAssetTypeID(assetType.ID).SetName("Foreign asset").SaveX(ctx)
	foreignUser := client.User.Create().SetCompanyID(72).SetEmail("foreign-update-user@example.test").SetPasswordHash("hash").SetName("Foreign user").SaveX(ctx)

	notes := "attempted mutation"
	if _, err := NewJobService(client).Update(ctx, companyID, foreignJob.ID, JobUpdateParams{Notes: &notes}); !errors.Is(err, ErrInvalidJobInput) {
		t.Fatalf("foreign job Update error = %v, want ErrInvalidJobInput", err)
	}
	if got := client.Job.GetX(ctx, foreignJob.ID).Notes; got != "unchanged" {
		t.Fatalf("foreign job notes = %q, want unchanged", got)
	}

	for _, tc := range []struct {
		name   string
		params JobUpdateParams
	}{
		{name: "foreign customer", params: JobUpdateParams{CustomerID: &foreignCustomer.ID}},
		{name: "archived customer", params: JobUpdateParams{CustomerID: &archivedCustomer.ID}},
		{name: "foreign project", params: JobUpdateParams{ProjectID: &foreignProject.ID}},
		{name: "foreign location", params: JobUpdateParams{LocationID: &foreignLocation.ID}},
		{name: "foreign contact", params: JobUpdateParams{CustomerContactID: &foreignContact.ID}},
		{name: "foreign asset", params: JobUpdateParams{AssetID: &foreignAsset.ID}},
		{name: "foreign assignment", params: JobUpdateParams{Assignments: &[]JobAssignment{{UserID: foreignUser.ID}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := client.Job.GetX(ctx, localJob.ID)
			_, err := NewJobService(client).Update(ctx, companyID, localJob.ID, tc.params)
			if !errors.Is(err, ErrInvalidJobInput) {
				t.Fatalf("Update error = %v, want ErrInvalidJobInput", err)
			}
			after := client.Job.GetX(ctx, localJob.ID)
			if after.CustomerID != before.CustomerID || int64Value(after.ProjectID) != int64Value(before.ProjectID) || int64Value(after.LocationID) != int64Value(before.LocationID) || int64Value(after.CustomerContactID) != int64Value(before.CustomerContactID) || int64Value(after.AssetID) != int64Value(before.AssetID) {
				t.Fatalf("job relationships mutated: before=%#v after=%#v", before, after)
			}
		})
	}

	localProject := client.Project.Create().SetCompanyID(companyID).SetCustomerID(replacementCustomer.ID).SetName("Replacement project").SaveX(ctx)
	localLocation := client.Location.Create().SetCompanyID(companyID).SetObjectType("customer").SetObjectID(replacementCustomer.ID).SetTitle("Replacement location").SaveX(ctx)
	localContact := client.CustomerContact.Create().SetCompanyID(companyID).SetCustomerID(replacementCustomer.ID).SetFirstName("Replacement contact").SaveX(ctx)
	localAssetType := client.AssetType.Create().SetCompanyID(companyID).SetName("Local equipment").SaveX(ctx)
	localAsset := client.Asset.Create().SetCompanyID(companyID).SetCustomerID(replacementCustomer.ID).SetAssetTypeID(localAssetType.ID).SetName("Replacement asset").SaveX(ctx)
	localUser := client.User.Create().SetCompanyID(companyID).SetEmail("local-update-user@example.test").SetPasswordHash("hash").SetName("Local user").SaveX(ctx)
	assignments := []JobAssignment{{UserID: localUser.ID, Role: "lead"}}
	updated, err := NewJobService(client).Update(ctx, companyID, localJob.ID, JobUpdateParams{
		CustomerID:        &replacementCustomer.ID,
		ProjectID:         &localProject.ID,
		LocationID:        &localLocation.ID,
		CustomerContactID: &localContact.ID,
		AssetID:           &localAsset.ID,
		Assignments:       &assignments,
	})
	if err != nil {
		t.Fatalf("matching Update: %v", err)
	}
	if updated.CustomerID != replacementCustomer.ID || int64Value(updated.ProjectID) != localProject.ID || int64Value(updated.LocationID) != localLocation.ID || int64Value(updated.CustomerContactID) != localContact.ID || int64Value(updated.AssetID) != localAsset.ID {
		t.Fatalf("updated relationships = %#v", updated)
	}
	if got := client.JobAssignment.Query().CountX(ctx); got != 1 {
		t.Fatalf("assignment count = %d, want 1", got)
	}
}

func TestJobUpdateRealFormPreservesUnchangedArchivedRelationshipsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 81
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Customer").SaveX(ctx)
	archivedReplacement := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Archived replacement").SetDeletedAt(time.Now()).SaveX(ctx)
	foreignReplacement := client.Customer.Create().SetCompanyID(82).SetDisplayName("Foreign replacement").SaveX(ctx)
	location := client.Location.Create().SetCompanyID(companyID).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Location").SaveX(ctx)
	contact := client.CustomerContact.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetFirstName("Contact").SaveX(ctx)
	project := client.Project.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetLocationID(location.ID).SetName("Project").SaveX(ctx)
	assetType := client.AssetType.Create().SetCompanyID(companyID).SetName("Equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetLocationID(location.ID).SetAssetTypeID(assetType.ID).SetName("Asset").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetProjectID(project.ID).SetLocationID(location.ID).SetCustomerContactID(contact.ID).SetAssetID(asset.ID).SetJobType("Repair").SaveX(ctx)
	client.Customer.UpdateOneID(customer.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	client.Project.UpdateOneID(project.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	client.Asset.UpdateOneID(asset.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	notes := "unrelated real-form edit"

	updated, err := NewJobService(client).Update(ctx, companyID, job.ID, JobUpdateParams{
		CustomerID:        &customer.ID,
		ProjectID:         &project.ID,
		LocationID:        &location.ID,
		CustomerContactID: &contact.ID,
		AssetID:           &asset.ID,
		Notes:             &notes,
	})
	if err != nil {
		t.Fatalf("Update unchanged archived relationships: %v", err)
	}
	if updated.Notes != notes || updated.CustomerID != customer.ID || int64Value(updated.ProjectID) != project.ID || int64Value(updated.LocationID) != location.ID || int64Value(updated.CustomerContactID) != contact.ID || int64Value(updated.AssetID) != asset.ID {
		t.Fatalf("updated job = %#v", updated)
	}
	for _, replacement := range []int64{archivedReplacement.ID, foreignReplacement.ID} {
		_, err := NewJobService(client).Update(ctx, companyID, job.ID, JobUpdateParams{
			CustomerID:        &replacement,
			ProjectID:         &project.ID,
			LocationID:        &location.ID,
			CustomerContactID: &contact.ID,
			AssetID:           &asset.ID,
			Notes:             &notes,
		})
		if !errors.Is(err, ErrInvalidJobInput) {
			t.Fatalf("replacement customer %d error = %v, want ErrInvalidJobInput", replacement, err)
		}
	}
}

func TestJobAssignmentPersistenceFailureRollsBackJobMutationIntegration(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		client := openPolicyTestClient(t)
		defer client.Close()
		ctx := context.Background()
		const companyID int64 = 91
		customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Customer").SaveX(ctx)
		createJobDefaultStatus(t, ctx, client, companyID, "New", 1)
		tech := client.User.Create().SetCompanyID(companyID).SetEmail("atomic-create@example.test").SetPasswordHash("hash").SetName("Tech").SaveX(ctx)
		forceJobAssignmentMutationFailure(client)

		before := client.Job.Query().CountX(ctx)
		_, err := NewJobService(client).Create(ctx, companyID, JobCreateParams{CustomerID: customer.ID, JobType: "Atomic create", Assignments: []JobAssignment{{UserID: tech.ID}}})
		if err == nil || !strings.Contains(err.Error(), "forced assignment persistence failure") {
			t.Fatalf("Create error = %v, want forced assignment failure", err)
		}
		if got := client.Job.Query().CountX(ctx); got != before {
			t.Fatalf("job count = %d, want %d", got, before)
		}
		if got := client.JobAssignment.Query().CountX(ctx); got != 0 {
			t.Fatalf("assignment count = %d, want 0", got)
		}
	})

	t.Run("update", func(t *testing.T) {
		client := openPolicyTestClient(t)
		defer client.Close()
		ctx := context.Background()
		const companyID int64 = 92
		customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Customer").SaveX(ctx)
		originalUser := client.User.Create().SetCompanyID(companyID).SetEmail("atomic-original@example.test").SetPasswordHash("hash").SetName("Original").SaveX(ctx)
		replacementUser := client.User.Create().SetCompanyID(companyID).SetEmail("atomic-replacement@example.test").SetPasswordHash("hash").SetName("Replacement").SaveX(ctx)
		originalAssignments := []JobAssignment{{UserID: originalUser.ID, Name: originalUser.Name, Role: "lead"}}
		job := client.Job.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetJobType("Atomic update").SetNotes("original notes").SetAssignments(SerializeAssignments(originalAssignments)).SaveX(ctx)
		client.JobAssignment.Create().SetJobID(job.ID).SetUserID(originalUser.ID).SetRole("lead").SaveX(ctx)
		forceJobAssignmentMutationFailure(client)
		notes := "mutated notes"
		replacementAssignments := []JobAssignment{{UserID: replacementUser.ID, Role: "replacement"}}

		_, err := NewJobService(client).Update(ctx, companyID, job.ID, JobUpdateParams{Notes: &notes, Assignments: &replacementAssignments})
		if err == nil || !strings.Contains(err.Error(), "forced assignment persistence failure") {
			t.Fatalf("Update error = %v, want forced assignment failure", err)
		}
		persisted := client.Job.GetX(ctx, job.ID)
		if persisted.Notes != job.Notes || persisted.Assignments != job.Assignments {
			t.Fatalf("job mutated after rollback: notes=%q assignments=%q", persisted.Notes, persisted.Assignments)
		}
		rows := client.JobAssignment.Query().AllX(ctx)
		if len(rows) != 1 || rows[0].UserID != originalUser.ID || rows[0].Role != "lead" {
			t.Fatalf("assignments mutated after rollback: %#v", rows)
		}
	})
}

func forceJobAssignmentMutationFailure(client *ent.Client) {
	client.JobAssignment.Use(func(ent.Mutator) ent.Mutator {
		return enthook.JobAssignmentFunc(func(context.Context, *ent.JobAssignmentMutation) (ent.Value, error) {
			return nil, errors.New("forced assignment persistence failure")
		})
	})
}

func createJobDefaultStatus(t *testing.T, ctx context.Context, client *ent.Client, companyID int64, name string, categoryOrder int) int64 {
	t.Helper()
	workflow := client.StatusWorkflow.Create().SetCompanyID(companyID).SetName(name + " workflow").SetObjectType("job").SaveX(ctx)
	return client.Status.Create().SetCompanyID(companyID).SetWorkflowID(workflow.ID).SetName(name).SetCategoryKey("job:new").SetCategoryOrder(categoryOrder).SetIsCategoryDefault(true).SaveX(ctx).ID
}
