package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
)

func TestServicesRejectCrossCustomerLinksIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)

	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{
			name: "project rejects foreign location",
			run: func() error {
				_, err := NewProjectService(client).Create(ctx, 1, ProjectCreateParams{
					CustomerID: data.customerB.ID,
					Name:       "Bad Project",
					LocationID: data.locationA.ID,
				})
				return err
			},
			want: "location does not belong to customer",
		},
		{
			name: "asset rejects foreign location",
			run: func() error {
				_, err := NewAssetService(client).Create(ctx, 1, AssetCreateParams{
					CustomerID:  data.customerB.ID,
					LocationID:  &data.locationA.ID,
					AssetTypeID: data.assetType.ID,
					Name:        "Bad Asset",
				})
				return err
			},
			want: "location does not belong to customer",
		},
		{
			name: "job rejects foreign project",
			run: func() error {
				_, err := NewJobService(client).Create(ctx, 1, JobCreateParams{
					CustomerID: data.customerB.ID,
					ProjectID:  data.projectA.ID,
					JobType:    "Bad Job",
				})
				return err
			},
			want: "project does not belong to customer",
		},
		{
			name: "job rejects foreign contact",
			run: func() error {
				_, err := NewJobService(client).Create(ctx, 1, JobCreateParams{
					CustomerID:        data.customerB.ID,
					CustomerContactID: data.contactA.ID,
					JobType:           "Bad Job",
				})
				return err
			},
			want: "contact does not belong to customer",
		},
		{
			name: "job rejects foreign asset",
			run: func() error {
				_, err := NewJobService(client).Create(ctx, 1, JobCreateParams{
					CustomerID: data.customerB.ID,
					AssetID:    data.assetA.ID,
					JobType:    "Bad Job",
				})
				return err
			},
			want: "asset does not belong to customer",
		},
		{
			name: "estimate rejects foreign job",
			run: func() error {
				_, err := NewEstimateService(client).Create(ctx, EstimateCreateParams{
					CustomerID: data.customerB.ID,
					JobID:      data.jobA.ID,
					Title:      "Bad Estimate",
				})
				return err
			},
			want: "job does not belong to customer",
		},
		{
			name: "invoice rejects foreign job",
			run: func() error {
				_, err := NewInvoiceService(client).Create(ctx, InvoiceCreateParams{
					CustomerID:  data.customerB.ID,
					JobID:       data.jobA.ID,
					Title:       "Bad Invoice",
					InvoiceDate: time.Now(),
					DueDate:     time.Now(),
				})
				return err
			},
			want: "job does not belong to customer",
		},
		{
			name: "invoice rejects foreign estimate",
			run: func() error {
				_, err := NewInvoiceService(client).Create(ctx, InvoiceCreateParams{
					CustomerID:  data.customerB.ID,
					EstimateID:  data.estimateA.ID,
					Title:       "Bad Invoice",
					InvoiceDate: time.Now(),
					DueDate:     time.Now(),
				})
				return err
			},
			want: "estimate does not belong to customer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatal("error = nil, want ownership error")
			}
			if !strings.Contains(tt.name, "job rejects") && !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
			if strings.Contains(tt.name, "job rejects") && !errors.Is(err, ErrInvalidJobInput) {
				t.Fatalf("error = %v, want ErrInvalidJobInput", err)
			}
		})
	}
}

func TestServicesRejectCustomerChangeWithExistingForeignLinkIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)

	newCustomerID := data.customerB.ID
	if _, err := NewProjectService(client).Update(ctx, 1, data.projectA.ID, ProjectUpdateParams{CustomerID: &newCustomerID}); err == nil || !strings.Contains(err.Error(), "location does not belong to customer") {
		t.Fatalf("project customer change error = %v, want location ownership error", err)
	}
	if _, err := NewJobService(client).Update(ctx, 1, data.jobA.ID, JobUpdateParams{CustomerID: &newCustomerID}); !errors.Is(err, ErrInvalidJobInput) {
		t.Fatalf("job customer change error = %v, want ErrInvalidJobInput", err)
	}
}

func TestServicesAllowUnchangedArchivedLinksIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)
	client.Project.UpdateOneID(data.projectA.ID).SetDeletedAt(time.Now()).SaveX(ctx)

	notes := "unchanged archived project link should not block notes edit"
	if _, err := NewJobService(client).Update(ctx, 1, data.jobA.ID, JobUpdateParams{Notes: &notes}); err != nil {
		t.Fatalf("update with unchanged archived link: %v", err)
	}
}

func TestServicesClearOptionalLinkedIDsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)
	zero := int64(0)

	jobResult, err := NewJobService(client).Update(ctx, 1, data.jobA.ID, JobUpdateParams{
		ProjectID:         &zero,
		LocationID:        &zero,
		CustomerContactID: &zero,
		AssetID:           &zero,
	})
	if err != nil {
		t.Fatalf("clear job links: %v", err)
	}
	if jobResult.ProjectID != nil || jobResult.LocationID != nil || jobResult.CustomerContactID != nil || jobResult.AssetID != nil {
		t.Fatalf("job links were not cleared: %#v", jobResult)
	}

	estimateResult, err := NewEstimateService(client).Update(ctx, data.estimateA.ID, EstimateUpdateParams{JobID: &zero})
	if err != nil {
		t.Fatalf("clear estimate job: %v", err)
	}
	if estimateResult.JobID != nil {
		t.Fatalf("estimate job ID was not cleared: %v", *estimateResult.JobID)
	}

	invoice := client.Invoice.Create().
		SetInvoiceNumber(9001).
		SetCustomerID(data.customerA.ID).
		SetJobID(data.jobA.ID).
		SetEstimateID(data.estimateA.ID).
		SetTitle("Invoice").
		SetInvoiceDate(time.Now()).
		SetDueDate(time.Now()).
		SaveX(ctx)
	invoiceResult, err := NewInvoiceService(client).Update(ctx, invoice.ID, InvoiceUpdateParams{JobID: &zero, EstimateID: &zero})
	if err != nil {
		t.Fatalf("clear invoice links: %v", err)
	}
	if invoiceResult.JobID != nil || invoiceResult.EstimateID != nil {
		t.Fatalf("invoice links were not cleared: %#v", invoiceResult)
	}
}

func TestCustomerContactGetByCustomerRejectsForeignContactIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)
	contactB := client.CustomerContact.Create().
		SetCustomerID(data.customerB.ID).
		SetFirstName("Other").
		SaveX(ctx)

	if _, err := NewCustomerContactService(client).GetByCustomer(ctx, data.customerA.ID, contactB.ID); err == nil {
		t.Fatal("GetByCustomer returned foreign contact")
	}
}

func TestCustomerContactCreateRejectsArchivedCustomerIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(1).SetDisplayName("Archived Customer").SetDeletedAt(time.Now()).SaveX(ctx)
	_, err := NewCustomerContactService(client).Create(ctx, 1, customer.ID, ContactCreateParams{FirstName: "Archived"})
	if err == nil || !strings.Contains(err.Error(), "customer does not exist, is archived, or belongs to another company") {
		t.Fatalf("Create contact error = %v, want archived customer error", err)
	}
}

func TestCustomerOwnedServiceWritesUseAuthenticatedCompanyIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 301
	const foreignCompanyID int64 = 302

	customer, err := NewCustomerService(client).Create(ctx, companyID, CustomerCreateParams{DisplayName: "Local customer"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	if customer.CompanyID == nil || *customer.CompanyID != companyID {
		t.Fatalf("customer company = %v, want %d", customer.CompanyID, companyID)
	}
	replacement := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Replacement").SaveX(ctx)
	foreign := client.Customer.Create().SetCompanyID(foreignCompanyID).SetDisplayName("Foreign").SaveX(ctx)
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
	for name, got := range map[string]*int64{"location": location.CompanyID, "contact": contact.CompanyID, "project": project.CompanyID, "asset": asset.CompanyID} {
		if got == nil || *got != companyID {
			t.Errorf("%s company = %v, want %d", name, got, companyID)
		}
	}

	if _, err := NewLocationService(client).CreateForCustomer(ctx, companyID, foreign.ID, CustomerLocationCreateParams{Title: "Rejected"}); err == nil {
		t.Fatal("location create accepted foreign customer")
	}
	if _, err := NewCustomerContactService(client).Create(ctx, companyID, foreign.ID, ContactCreateParams{FirstName: "Rejected"}); err == nil {
		t.Fatal("contact create accepted foreign customer")
	}
	if _, err := NewProjectService(client).Create(ctx, companyID, ProjectCreateParams{CustomerID: foreign.ID, Name: "Rejected"}); err == nil {
		t.Fatal("project create accepted foreign customer")
	}
	if _, err := NewAssetService(client).Create(ctx, companyID, AssetCreateParams{CustomerID: foreign.ID, AssetTypeID: assetType.ID, Name: "Rejected"}); err == nil {
		t.Fatal("asset create accepted foreign customer")
	}

	projectReplacement := replacement.ID
	updatedProject, err := NewProjectService(client).Update(ctx, companyID, project.ID, ProjectUpdateParams{CustomerID: &projectReplacement, LocationID: int64PtrForServiceTest(0)})
	if err != nil {
		t.Fatalf("reassign project: %v", err)
	}
	updatedAsset, err := NewAssetService(client).Update(ctx, companyID, asset.ID, AssetUpdateParams{CustomerID: &projectReplacement, LocationID: int64PtrForServiceTest(0)})
	if err != nil {
		t.Fatalf("reassign asset: %v", err)
	}
	if updatedProject.CustomerID != replacement.ID || updatedAsset.CustomerID != replacement.ID {
		t.Fatalf("reassignment failed: project=%d asset=%d", updatedProject.CustomerID, updatedAsset.CustomerID)
	}
	for name, got := range map[string]*int64{"returned project": updatedProject.CompanyID, "returned asset": updatedAsset.CompanyID} {
		if got == nil || *got != companyID {
			t.Errorf("%s company = %v, want %d", name, got, companyID)
		}
	}
	persistedProject := client.Project.GetX(ctx, project.ID)
	persistedAsset := client.Asset.GetX(ctx, asset.ID)
	for name, got := range map[string]*int64{"persisted project": persistedProject.CompanyID, "persisted asset": persistedAsset.CompanyID} {
		if got == nil || *got != companyID {
			t.Errorf("%s company = %v, want %d", name, got, companyID)
		}
	}
	foreignID := foreign.ID
	if _, err := NewProjectService(client).Update(ctx, companyID, project.ID, ProjectUpdateParams{CustomerID: &foreignID}); err == nil {
		t.Fatal("project update accepted foreign customer")
	}
	if _, err := NewAssetService(client).Update(ctx, companyID, asset.ID, AssetUpdateParams{CustomerID: &foreignID}); err == nil {
		t.Fatal("asset update accepted foreign customer")
	}
}

func TestProjectAndAssetUpdateCannotClaimCompanylessForeignCustomerRowsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 311
	const foreignCompanyID int64 = 312
	localCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Local").SaveX(ctx)
	foreignCustomer := client.Customer.Create().SetCompanyID(foreignCompanyID).SetDisplayName("Foreign").SaveX(ctx)
	localAssetType := client.AssetType.Create().SetCompanyID(companyID).SetName("Equipment").SaveX(ctx)
	project := client.Project.Create().SetCustomerID(foreignCustomer.ID).SetName("Foreign project").SaveX(ctx)
	asset := client.Asset.Create().SetCustomerID(foreignCustomer.ID).SetAssetTypeID(localAssetType.ID).SetName("Foreign asset").SaveX(ctx)

	if _, err := NewProjectService(client).Update(ctx, companyID, project.ID, ProjectUpdateParams{CustomerID: &localCustomer.ID}); err == nil {
		t.Fatal("project update claimed companyless foreign-customer row")
	}
	if _, err := NewAssetService(client).Update(ctx, companyID, asset.ID, AssetUpdateParams{CustomerID: &localCustomer.ID}); err == nil {
		t.Fatal("asset update claimed companyless foreign-customer row")
	}
	persistedProject := client.Project.GetX(ctx, project.ID)
	persistedAsset := client.Asset.GetX(ctx, asset.ID)
	if persistedProject.CompanyID != nil || persistedProject.CustomerID != foreignCustomer.ID || persistedProject.Name != project.Name {
		t.Fatalf("project mutated after rejected claim: %#v", persistedProject)
	}
	if persistedAsset.CompanyID != nil || persistedAsset.CustomerID != foreignCustomer.ID || persistedAsset.Name != asset.Name {
		t.Fatalf("asset mutated after rejected claim: %#v", persistedAsset)
	}
}

func TestAssetWritesRejectForeignTypeAndStatusIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 321
	const foreignCompanyID int64 = 322
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Local").SaveX(ctx)
	localType := client.AssetType.Create().SetCompanyID(companyID).SetName("Local type").SaveX(ctx)
	foreignType := client.AssetType.Create().SetCompanyID(foreignCompanyID).SetName("Foreign type").SaveX(ctx)
	localStatus := client.AssetStatus.Create().SetCompanyID(companyID).SetName("Local status").SaveX(ctx)
	foreignStatus := client.AssetStatus.Create().SetCompanyID(foreignCompanyID).SetName("Foreign status").SaveX(ctx)

	for _, tc := range []struct {
		name   string
		params AssetCreateParams
	}{
		{name: "foreign type", params: AssetCreateParams{CustomerID: customer.ID, AssetTypeID: foreignType.ID, Name: "Rejected"}},
		{name: "foreign status", params: AssetCreateParams{CustomerID: customer.ID, AssetTypeID: localType.ID, AssetStatusID: &foreignStatus.ID, Name: "Rejected"}},
	} {
		t.Run("create "+tc.name, func(t *testing.T) {
			before := client.Asset.Query().CountX(ctx)
			if _, err := NewAssetService(client).Create(ctx, companyID, tc.params); err == nil {
				t.Fatalf("asset create accepted %s", tc.name)
			}
			if got := client.Asset.Query().CountX(ctx); got != before {
				t.Fatalf("asset count = %d, want %d", got, before)
			}
		})
	}
	zeroStatus := int64(0)
	withoutStatus, err := NewAssetService(client).Create(ctx, companyID, AssetCreateParams{CustomerID: customer.ID, AssetTypeID: localType.ID, AssetStatusID: &zeroStatus, Name: "No status"})
	if err != nil {
		t.Fatalf("create with zero status: %v", err)
	}
	if withoutStatus.AssetStatusID != nil {
		t.Fatalf("zero create status persisted as %v, want nil", *withoutStatus.AssetStatusID)
	}

	asset := client.Asset.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetAssetTypeID(localType.ID).SetAssetStatusID(localStatus.ID).SetName("Original").SaveX(ctx)
	for _, tc := range []struct {
		name   string
		params AssetUpdateParams
	}{
		{name: "foreign type", params: AssetUpdateParams{AssetTypeID: &foreignType.ID}},
		{name: "foreign status", params: AssetUpdateParams{AssetStatusID: &foreignStatus.ID}},
	} {
		t.Run("update "+tc.name, func(t *testing.T) {
			if _, err := NewAssetService(client).Update(ctx, companyID, asset.ID, tc.params); err == nil {
				t.Fatalf("asset update accepted %s", tc.name)
			}
			persisted := client.Asset.GetX(ctx, asset.ID)
			if persisted.AssetTypeID != localType.ID || int64Value(persisted.AssetStatusID) != localStatus.ID || persisted.Name != asset.Name {
				t.Fatalf("asset mutated after rejected update: %#v", persisted)
			}
		})
	}
	cleared, err := NewAssetService(client).Update(ctx, companyID, asset.ID, AssetUpdateParams{AssetStatusID: &zeroStatus})
	if err != nil {
		t.Fatalf("clear asset status: %v", err)
	}
	if cleared.AssetStatusID != nil || client.Asset.GetX(ctx, asset.ID).AssetStatusID != nil {
		t.Fatal("zero update status did not clear optional status")
	}

	newName := "Rejected unrelated update"
	foreignTypeAsset := client.Asset.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetAssetTypeID(foreignType.ID).SetName("Foreign type asset").SaveX(ctx)
	if _, err := NewAssetService(client).Update(ctx, companyID, foreignTypeAsset.ID, AssetUpdateParams{Name: &newName}); err == nil {
		t.Fatal("asset update accepted effective foreign type")
	}
	if got := client.Asset.GetX(ctx, foreignTypeAsset.ID).Name; got != foreignTypeAsset.Name {
		t.Fatalf("foreign-type asset name = %q, want %q", got, foreignTypeAsset.Name)
	}
	foreignStatusAsset := client.Asset.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetAssetTypeID(localType.ID).SetAssetStatusID(foreignStatus.ID).SetName("Foreign status asset").SaveX(ctx)
	if _, err := NewAssetService(client).Update(ctx, companyID, foreignStatusAsset.ID, AssetUpdateParams{Name: &newName}); err == nil {
		t.Fatal("asset update accepted effective foreign status")
	}
	if got := client.Asset.GetX(ctx, foreignStatusAsset.ID).Name; got != foreignStatusAsset.Name {
		t.Fatalf("foreign-status asset name = %q, want %q", got, foreignStatusAsset.Name)
	}
}

func int64PtrForServiceTest(v int64) *int64 {
	return &v
}

type ownershipFixture struct {
	customerA *ent.Customer
	customerB *ent.Customer
	locationA *ent.Location
	assetType *ent.AssetType
	assetA    *ent.Asset
	contactA  *ent.CustomerContact
	projectA  *ent.Project
	jobA      *ent.Job
	estimateA *ent.Estimate
}

func createOwnershipFixture(ctx context.Context, t *testing.T, client *ent.Client) ownershipFixture {
	t.Helper()
	const companyID int64 = 1
	customerA := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Customer A").SaveX(ctx)
	customerB := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Customer B").SaveX(ctx)
	locationA := client.Location.Create().SetCompanyID(companyID).SetObjectType("customer").SetObjectID(customerA.ID).SetTitle("Customer A Location").SaveX(ctx)
	assetType := client.AssetType.Create().SetCompanyID(companyID).SetName("Equipment").SaveX(ctx)
	assetA := client.Asset.Create().SetCompanyID(companyID).SetCustomerID(customerA.ID).SetLocationID(locationA.ID).SetAssetTypeID(assetType.ID).SetName("Asset A").SaveX(ctx)
	contactA := client.CustomerContact.Create().SetCompanyID(companyID).SetCustomerID(customerA.ID).SetFirstName("Contact A").SaveX(ctx)
	projectA := client.Project.Create().SetCompanyID(companyID).SetCustomerID(customerA.ID).SetLocationID(locationA.ID).SetName("Project A").SaveX(ctx)
	jobA := client.Job.Create().
		SetCompanyID(companyID).
		SetCustomerID(customerA.ID).
		SetProjectID(projectA.ID).
		SetLocationID(locationA.ID).
		SetCustomerContactID(contactA.ID).
		SetAssetID(assetA.ID).
		SetJobType("Job A").
		SaveX(ctx)
	estimateA := client.Estimate.Create().SetCustomerID(customerA.ID).SetJobID(jobA.ID).SetTitle("Estimate A").SaveX(ctx)

	return ownershipFixture{
		customerA: customerA,
		customerB: customerB,
		locationA: locationA,
		assetType: assetType,
		assetA:    assetA,
		contactA:  contactA,
		projectA:  projectA,
		jobA:      jobA,
		estimateA: estimateA,
	}
}
