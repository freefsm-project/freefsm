package services

import (
	"context"
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
				_, err := NewProjectService(client).Create(ctx, ProjectCreateParams{
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
				_, err := NewAssetService(client).Create(ctx, AssetCreateParams{
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
				_, err := NewJobService(client).Create(ctx, JobCreateParams{
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
				_, err := NewJobService(client).Create(ctx, JobCreateParams{
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
				_, err := NewJobService(client).Create(ctx, JobCreateParams{
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
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want %q", err, tt.want)
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
	if _, err := NewProjectService(client).Update(ctx, data.projectA.ID, ProjectUpdateParams{CustomerID: &newCustomerID}); err == nil || !strings.Contains(err.Error(), "location does not belong to customer") {
		t.Fatalf("project customer change error = %v, want location ownership error", err)
	}
	if _, err := NewJobService(client).Update(ctx, data.jobA.ID, JobUpdateParams{CustomerID: &newCustomerID}); err == nil || !strings.Contains(err.Error(), "project does not belong to customer") {
		t.Fatalf("job customer change error = %v, want project ownership error", err)
	}
}

func TestServicesAllowUnchangedArchivedLinksIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)
	client.Project.UpdateOneID(data.projectA.ID).SetDeletedAt(time.Now()).SaveX(ctx)

	notes := "unchanged archived project link should not block notes edit"
	if _, err := NewJobService(client).Update(ctx, data.jobA.ID, JobUpdateParams{Notes: &notes}); err != nil {
		t.Fatalf("update with unchanged archived link: %v", err)
	}
}

func TestServicesClearOptionalLinkedIDsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	data := createOwnershipFixture(ctx, t, client)
	zero := int64(0)

	jobResult, err := NewJobService(client).Update(ctx, data.jobA.ID, JobUpdateParams{
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
	customer := client.Customer.Create().SetDisplayName("Archived Customer").SetDeletedAt(time.Now()).SaveX(ctx)
	_, err := NewCustomerContactService(client).Create(ctx, customer.ID, ContactCreateParams{FirstName: "Archived"})
	if err == nil || !strings.Contains(err.Error(), "customer does not exist or is archived") {
		t.Fatalf("Create contact error = %v, want archived customer error", err)
	}
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
	customerA := client.Customer.Create().SetDisplayName("Customer A").SaveX(ctx)
	customerB := client.Customer.Create().SetDisplayName("Customer B").SaveX(ctx)
	locationA := client.Location.Create().SetObjectType("customer").SetObjectID(customerA.ID).SetTitle("Customer A Location").SaveX(ctx)
	assetType := client.AssetType.Create().SetName("Equipment").SaveX(ctx)
	assetA := client.Asset.Create().SetCustomerID(customerA.ID).SetLocationID(locationA.ID).SetAssetTypeID(assetType.ID).SetName("Asset A").SaveX(ctx)
	contactA := client.CustomerContact.Create().SetCustomerID(customerA.ID).SetFirstName("Contact A").SaveX(ctx)
	projectA := client.Project.Create().SetCustomerID(customerA.ID).SetLocationID(locationA.ID).SetName("Project A").SaveX(ctx)
	jobA := client.Job.Create().
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
