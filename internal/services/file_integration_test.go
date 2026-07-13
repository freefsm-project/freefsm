package services

import (
	"bytes"
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

func TestFileServiceTargetExistsRejectsArchivedTargets(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 1024)
	now := time.Now()
	const companyID int64 = 101

	activeCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Active Customer").SaveX(ctx)
	archivedCustomer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Archived Customer").SetDeletedAt(now).SaveX(ctx)
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
				SetCompanyID(companyID).
				SetCustomerID(activeCustomer.ID).
				SetJobType("Active Job").
				SaveX(ctx).ID,
			archivedID: client.Job.Create().
				SetCompanyID(companyID).
				SetCustomerID(activeCustomer.ID).
				SetJobType("Archived Job").
				SetDeletedAt(now).
				SaveX(ctx).ID,
		},
		{
			name:       "project",
			objectType: objectref.TypeProject,
			activeID: client.Project.Create().
				SetCompanyID(companyID).
				SetCustomerID(activeCustomer.ID).
				SetName("Active Project").
				SaveX(ctx).ID,
			archivedID: client.Project.Create().
				SetCompanyID(companyID).
				SetCustomerID(activeCustomer.ID).
				SetName("Archived Project").
				SetDeletedAt(now).
				SaveX(ctx).ID,
		},
		{
			name:       "estimate",
			objectType: objectref.TypeEstimate,
			activeID: client.Estimate.Create().
				SetCompanyID(companyID).
				SetCustomerID(activeCustomer.ID).
				SetTitle("Active Estimate").
				SaveX(ctx).ID,
			archivedID: client.Estimate.Create().
				SetCompanyID(companyID).
				SetCustomerID(activeCustomer.ID).
				SetTitle("Archived Estimate").
				SetDeletedAt(now).
				SaveX(ctx).ID,
		},
		{
			name:       "invoice",
			objectType: objectref.TypeInvoice,
			activeID: client.Invoice.Create().
				SetCompanyID(companyID).
				SetInvoiceNumber(9001).
				SetCustomerID(activeCustomer.ID).
				SetTitle("Active Invoice").
				SetInvoiceDate(now).
				SetDueDate(now).
				SaveX(ctx).ID,
			archivedID: client.Invoice.Create().
				SetCompanyID(companyID).
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
				SetCompanyID(companyID).
				SetCustomerID(activeCustomer.ID).
				SetAssetTypeID(assetType.ID).
				SetName("Active Asset").
				SaveX(ctx).ID,
			archivedID: client.Asset.Create().
				SetCompanyID(companyID).
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
			if !svc.TargetExists(ctx, companyID, activeRef) {
				t.Fatalf("TargetExists(%q, active ID %d) = false, want true", tt.objectType, tt.activeID)
			}
			if svc.TargetExists(ctx, companyID, archivedRef) {
				t.Fatalf("TargetExists(%q, archived ID %d) = true, want false", tt.objectType, tt.archivedID)
			}
			if _, err := svc.List(ctx, companyID, archivedRef); err != nil {
				t.Fatalf("List(%q, archived ID %d) error = %v, want nil", tt.objectType, tt.archivedID, err)
			}
		})
	}

	if svc.TargetExists(ctx, companyID, objectref.New(objectref.TypeJob, 0)) {
		t.Fatal("TargetExists with zero ID = true, want false")
	}
	if svc.TargetExists(ctx, companyID, objectref.New(objectref.Type("unsupported"), activeCustomer.ID)) {
		t.Fatal("TargetExists with unsupported object type = true, want false")
	}
}

func TestFileServiceListRejectsInvalidAndUnsupportedRefs(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 1024)
	const companyID int64 = 101
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("File List Customer").SaveX(ctx)
	item := client.Item.Create().SetName("Unsupported File Target").SaveX(ctx)

	if _, err := svc.List(ctx, companyID, objectref.New(objectref.TypeCustomer, customer.ID)); err != nil {
		t.Fatalf("List(active customer) error = %v, want nil", err)
	}
	if _, err := svc.List(ctx, companyID, objectref.New(objectref.TypeCustomer, 0)); err == nil {
		t.Fatal("List(zero ID) error = nil, want error")
	}
	if _, err := svc.List(ctx, companyID, objectref.New(objectref.TypeItem, item.ID)); err == nil {
		t.Fatal("List(unsupported files ref) error = nil, want error")
	}
	if _, err := svc.List(ctx, companyID, objectref.New(objectref.TypeCustomer, customer.ID+9999)); err == nil {
		t.Fatal("List(missing target) error = nil, want error")
	}
}

func TestFileServicePersistsAndEnforcesTenantOwnership(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	const companyA, companyB int64 = 101, 202
	refA := objectref.New(objectref.TypeCustomer, 11)
	refB := objectref.New(objectref.TypeCustomer, 22)
	directory := &objectref.FakeDirectory{
		Active:           map[objectref.Ref]bool{refA: true, refB: true},
		Any:              map[objectref.Ref]bool{refA: true, refB: true},
		TargetCompanyIDs: map[objectref.Ref]*int64{refA: int64Pointer(companyA), refB: int64Pointer(companyB)},
	}
	svc := NewFileService(client, directory, t.TempDir(), 1024)
	userA := client.User.Create().SetCompanyID(companyA).SetEmail("file-a@example.com").SetPasswordHash("hash").SetName("A").SaveX(ctx)
	userB := client.User.Create().SetCompanyID(companyB).SetEmail("file-b@example.com").SetPasswordHash("hash").SetName("B").SaveX(ctx)

	created, err := svc.CreateBytes(ctx, companyA, refA, "note.txt", "text/plain", []byte("hello"), userA.ID)
	if err != nil {
		t.Fatalf("CreateBytes: %v", err)
	}
	if created.CompanyID != companyA {
		t.Fatalf("created company=%v, want %d", created.CompanyID, companyA)
	}
	if created.FileSize != int64(len("hello")) {
		t.Fatalf("created file size=%d, want measured size %d", created.FileSize, len("hello"))
	}
	if _, err := svc.GetByID(ctx, companyB, created.ID); !ent.IsNotFound(err) {
		t.Fatalf("cross-company GetByID error=%v, want not found", err)
	}
	if files, err := svc.List(ctx, companyA, refA); err != nil || len(files) != 1 || files[0].ID != created.ID {
		t.Fatalf("List company A files=%v err=%v", files, err)
	}
	if _, err := svc.List(ctx, companyA, refB); err == nil {
		t.Fatal("List accepted a foreign target")
	}
	if _, err := svc.CreateBytes(ctx, companyA, refB, "foreign.txt", "text/plain", []byte("x"), userA.ID); err == nil {
		t.Fatal("CreateBytes accepted a foreign target")
	}
	if _, err := svc.CreateBytes(ctx, companyA, refA, "foreign-user.txt", "text/plain", []byte("x"), userB.ID); err == nil {
		t.Fatal("CreateBytes accepted a foreign uploader")
	}
	if err := svc.Rename(ctx, companyB, created.ID, "stolen.txt"); !ent.IsNotFound(err) {
		t.Fatalf("cross-company Rename error=%v, want not found", err)
	}
	if err := svc.Delete(ctx, companyB, created.ID); !ent.IsNotFound(err) {
		t.Fatalf("cross-company Delete error=%v, want not found", err)
	}
	for _, companyID := range []int64{0, -1} {
		if _, err := svc.List(ctx, companyID, refA); err == nil {
			t.Fatalf("List accepted company ID %d", companyID)
		}
	}
}

func TestFileServiceArchivedTargetsRemainReadableButNotMutable(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	const companyID int64 = 101
	ref := objectref.New(objectref.TypeCustomer, 11)
	directory := &objectref.FakeDirectory{
		Active:           map[objectref.Ref]bool{ref: true},
		Any:              map[objectref.Ref]bool{ref: true},
		TargetCompanyIDs: map[objectref.Ref]*int64{ref: int64Pointer(companyID)},
	}
	svc := NewFileService(client, directory, t.TempDir(), 1024)
	uploader := client.User.Create().SetCompanyID(companyID).SetEmail("archive@example.com").SetPasswordHash("hash").SetName("A").SaveX(ctx)
	created, err := svc.CreateBytes(ctx, companyID, ref, "note.txt", "text/plain", []byte("hello"), uploader.ID)
	if err != nil {
		t.Fatal(err)
	}
	directory.Active[ref] = false

	if _, err := svc.List(ctx, companyID, ref); err != nil {
		t.Fatalf("List archived target: %v", err)
	}
	if _, err := svc.GetByID(ctx, companyID, created.ID); err != nil {
		t.Fatalf("GetByID archived target: %v", err)
	}
	if err := svc.Rename(ctx, companyID, created.ID, "renamed.txt"); !ent.IsNotFound(err) {
		t.Fatalf("Rename archived target error=%v, want not found", err)
	}
	if err := svc.Delete(ctx, companyID, created.ID); !ent.IsNotFound(err) {
		t.Fatalf("Delete archived target error=%v, want not found", err)
	}
	if _, err := svc.CreateBytes(ctx, companyID, ref, "new.txt", "text/plain", []byte("x"), uploader.ID); err == nil {
		t.Fatal("CreateBytes accepted archived target")
	}
	if _, err := svc.GetByID(ctx, companyID, created.ID); err != nil {
		t.Fatalf("file changed after rejected mutations: %v", err)
	}
}

func TestFileServiceCreateRejectsInvalidActualSizesWithoutArtifacts(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	const companyID int64 = 101
	ref := objectref.New(objectref.TypeCustomer, 11)
	directory := &objectref.FakeDirectory{
		Active:           map[objectref.Ref]bool{ref: true},
		Any:              map[objectref.Ref]bool{ref: true},
		TargetCompanyIDs: map[objectref.Ref]*int64{ref: int64Pointer(companyID)},
	}
	uploadDir := t.TempDir()
	svc := NewFileService(client, directory, uploadDir, 5)
	uploader := client.User.Create().SetCompanyID(companyID).SetEmail("size@example.com").SetPasswordHash("hash").SetName("A").SaveX(ctx)

	tests := []struct {
		name     string
		declared int64
		data     []byte
	}{
		{name: "negative declared size", declared: -1, data: []byte("x")},
		{name: "actual reader exceeds maximum", declared: 5, data: []byte("123456")},
		{name: "actual shorter than declared", declared: 5, data: []byte("1234")},
		{name: "actual longer than declared", declared: 4, data: []byte("12345")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.Create(ctx, companyID, ref, "size.txt", "text/plain", tt.declared, bytes.NewReader(tt.data), uploader.ID); err == nil {
				t.Fatal("Create error=nil, want size validation error")
			}
			if count := client.File.Query().CountX(ctx); count != 0 {
				t.Fatalf("file row count=%d, want 0", count)
			}
			assertNoUploadedFiles(t, uploadDir)
		})
	}
}

func assertNoUploadedFiles(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Errorf("unexpected uploaded file %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
