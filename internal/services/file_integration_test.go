package services

import (
	"context"
	"testing"
	"time"

	"github.com/MartialM1nd/freefsm/internal/objectref"
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
		objectType string
		activeID   int64
		archivedID int64
	}{
		{
			name:       "customer",
			objectType: "customer",
			activeID:   activeCustomer.ID,
			archivedID: archivedCustomer.ID,
		},
		{
			name:       "job",
			objectType: "job",
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
			objectType: "project",
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
			objectType: "estimate",
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
			objectType: "invoice",
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
			objectType: "asset",
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
			if !svc.TargetExists(ctx, tt.objectType, tt.activeID) {
				t.Fatalf("TargetExists(%q, active ID %d) = false, want true", tt.objectType, tt.activeID)
			}
			if svc.TargetExists(ctx, tt.objectType, tt.archivedID) {
				t.Fatalf("TargetExists(%q, archived ID %d) = true, want false", tt.objectType, tt.archivedID)
			}
		})
	}

	if svc.TargetExists(ctx, "job", 0) {
		t.Fatal("TargetExists with zero ID = true, want false")
	}
	if svc.TargetExists(ctx, "unsupported", activeCustomer.ID) {
		t.Fatal("TargetExists with unsupported object type = true, want false")
	}
}
