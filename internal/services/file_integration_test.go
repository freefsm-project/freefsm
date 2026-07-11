package services

import (
	"context"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/objectref"
)

func TestFileServiceTargetExistsRejectsArchivedTargets(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 1024)
	now := time.Now()

	activeCustomer := client.Customer.Create().SetDisplayName("Active Customer").SaveX(ctx)
	archivedCustomer := client.Customer.Create().SetDisplayName("Archived Customer").SetDeletedAt(now).SaveX(ctx)
	assetType := client.AssetType.Create().SetName("Equipment").SaveX(ctx)

	tests := []struct {
		name       string
		objectType objectref.Type
		activeID   int64
		archivedID int64
	}{
		{
			name:       "customer",
			objectType: objectref.TypeCustomer,
			activeID:   activeCustomer.ID,
			archivedID: archivedCustomer.ID,
		},
		{
			name:       "job",
			objectType: objectref.TypeJob,
			activeID: client.Job.Create().
				SetCustomerID(activeCustomer.ID).
				SetJobType("Active Job").
				SaveX(ctx).ID,
			archivedID: client.Job.Create().
				SetCustomerID(activeCustomer.ID).
				SetJobType("Archived Job").
				SetDeletedAt(now).
				SaveX(ctx).ID,
		},
		{
			name:       "project",
			objectType: objectref.TypeProject,
			activeID: client.Project.Create().
				SetCustomerID(activeCustomer.ID).
				SetName("Active Project").
				SaveX(ctx).ID,
			archivedID: client.Project.Create().
				SetCustomerID(activeCustomer.ID).
				SetName("Archived Project").
				SetDeletedAt(now).
				SaveX(ctx).ID,
		},
		{
			name:       "estimate",
			objectType: objectref.TypeEstimate,
			activeID: client.Estimate.Create().
				SetCustomerID(activeCustomer.ID).
				SetTitle("Active Estimate").
				SaveX(ctx).ID,
			archivedID: client.Estimate.Create().
				SetCustomerID(activeCustomer.ID).
				SetTitle("Archived Estimate").
				SetDeletedAt(now).
				SaveX(ctx).ID,
		},
		{
			name:       "invoice",
			objectType: objectref.TypeInvoice,
			activeID: client.Invoice.Create().
				SetInvoiceNumber(9001).
				SetCustomerID(activeCustomer.ID).
				SetTitle("Active Invoice").
				SetInvoiceDate(now).
				SetDueDate(now).
				SaveX(ctx).ID,
			archivedID: client.Invoice.Create().
				SetInvoiceNumber(9002).
				SetCustomerID(activeCustomer.ID).
				SetTitle("Archived Invoice").
				SetInvoiceDate(now).
				SetDueDate(now).
				SetDeletedAt(now).
				SaveX(ctx).ID,
		},
		{
			name:       "asset",
			objectType: objectref.TypeAsset,
			activeID: client.Asset.Create().
				SetCustomerID(activeCustomer.ID).
				SetAssetTypeID(assetType.ID).
				SetName("Active Asset").
				SaveX(ctx).ID,
			archivedID: client.Asset.Create().
				SetCustomerID(activeCustomer.ID).
				SetAssetTypeID(assetType.ID).
				SetName("Archived Asset").
				SetDeletedAt(now).
				SaveX(ctx).ID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activeRef := objectref.New(tt.objectType, tt.activeID)
			archivedRef := objectref.New(tt.objectType, tt.archivedID)
			if !svc.TargetExists(ctx, activeRef) {
				t.Fatalf("TargetExists(%q, active ID %d) = false, want true", tt.objectType, tt.activeID)
			}
			if svc.TargetExists(ctx, archivedRef) {
				t.Fatalf("TargetExists(%q, archived ID %d) = true, want false", tt.objectType, tt.archivedID)
			}
			if _, err := svc.List(ctx, archivedRef); err != nil {
				t.Fatalf("List(%q, archived ID %d) error = %v, want nil", tt.objectType, tt.archivedID, err)
			}
		})
	}

	if svc.TargetExists(ctx, objectref.New(objectref.TypeJob, 0)) {
		t.Fatal("TargetExists with zero ID = true, want false")
	}
	if svc.TargetExists(ctx, objectref.New(objectref.Type("unsupported"), activeCustomer.ID)) {
		t.Fatal("TargetExists with unsupported object type = true, want false")
	}
}

func TestFileServiceListRejectsInvalidAndUnsupportedRefs(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 1024)
	customer := client.Customer.Create().SetDisplayName("File List Customer").SaveX(ctx)
	item := client.Item.Create().SetName("Unsupported File Target").SaveX(ctx)

	if _, err := svc.List(ctx, objectref.New(objectref.TypeCustomer, customer.ID)); err != nil {
		t.Fatalf("List(active customer) error = %v, want nil", err)
	}
	if _, err := svc.List(ctx, objectref.New(objectref.TypeCustomer, 0)); err == nil {
		t.Fatal("List(zero ID) error = nil, want error")
	}
	if _, err := svc.List(ctx, objectref.New(objectref.TypeItem, item.ID)); err == nil {
		t.Fatal("List(unsupported files ref) error = nil, want error")
	}
	if _, err := svc.List(ctx, objectref.New(objectref.TypeCustomer, customer.ID+9999)); err == nil {
		t.Fatal("List(missing target) error = nil, want error")
	}
}
