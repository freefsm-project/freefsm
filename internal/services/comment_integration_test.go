package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/objectref"
)

func TestCommentServiceUsesObjectRefsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	const companyID int64 = 101
	user := client.User.Create().SetCompanyID(companyID).SetEmail("commenter@example.test").SetPasswordHash("hash").SetName("Commenter").SetRole("dispatcher").SaveX(ctx)
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Comment Target").SaveX(ctx)
	ref := objectref.New(objectref.TypeCustomer, customer.ID)

	created, err := svc.Create(ctx, companyID, ref, user.ID, "Needs follow-up")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ObjectType != ref.ObjectType() || created.ObjectID != ref.ObjectID() {
		t.Fatalf("created ref = %s %d, want %s %d", created.ObjectType, created.ObjectID, ref.ObjectType(), ref.ObjectID())
	}

	comments, err := svc.ListForObject(ctx, companyID, ref)
	if err != nil {
		t.Fatalf("ListForObject: %v", err)
	}
	if len(comments) != 1 || comments[0].Content != "Needs follow-up" {
		t.Fatalf("comments = %#v", comments)
	}
}

func TestCommentServiceRejectsInvalidRefsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	invalid := objectref.Ref{Type: objectref.TypeCustomer}

	if _, err := svc.Create(ctx, 1, invalid, 1, "bad"); err == nil {
		t.Fatal("Create invalid ref error = nil")
	}
	if _, err := svc.ListForObject(ctx, 1, invalid); err == nil {
		t.Fatal("ListForObject invalid ref error = nil")
	}
}

func TestCommentServiceListsArchivedTargetsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	const companyID int64 = 102
	user := client.User.Create().SetCompanyID(companyID).SetEmail("archived-commenter@example.test").SetPasswordHash("hash").SetName("Archived Commenter").SetRole("dispatcher").SaveX(ctx)
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Archived Comment Target").SaveX(ctx)
	ref := objectref.New(objectref.TypeCustomer, customer.ID)

	if _, err := svc.Create(ctx, companyID, ref, user.ID, "before archive"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	client.Customer.UpdateOneID(customer.ID).SetDeletedAt(time.Now()).SaveX(ctx)

	comments, err := svc.ListForObject(ctx, companyID, ref)
	if err != nil {
		t.Fatalf("ListForObject archived target: %v", err)
	}
	if len(comments) != 1 || comments[0].Content != "before archive" {
		t.Fatalf("comments = %#v", comments)
	}
}

func TestCommentServiceRejectsArchivedCreateIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	const companyID int64 = 103
	user := client.User.Create().SetCompanyID(companyID).SetEmail("archived-create@example.test").SetPasswordHash("hash").SetName("Archived Create").SetRole("dispatcher").SaveX(ctx)
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Archived Create Target").SetDeletedAt(time.Now()).SaveX(ctx)

	if _, err := svc.Create(ctx, companyID, objectref.New(objectref.TypeCustomer, customer.ID), user.ID, "blocked"); err == nil {
		t.Fatal("Create archived target error = nil")
	}
}

func TestCommentServiceRejectsArchivedDeleteIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	const companyID int64 = 104
	user := client.User.Create().SetCompanyID(companyID).SetEmail("archived-delete@example.test").SetPasswordHash("hash").SetName("Archived Delete").SetRole("dispatcher").SaveX(ctx)
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Archived Delete Target").SaveX(ctx)
	ref := objectref.New(objectref.TypeCustomer, customer.ID)

	comment, err := svc.Create(ctx, companyID, ref, user.ID, "cannot delete after archive")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	client.Customer.UpdateOneID(customer.ID).SetDeletedAt(time.Now()).SaveX(ctx)

	if err := svc.Delete(ctx, companyID, ref, comment.ID); err == nil {
		t.Fatal("Delete archived target error = nil")
	}
}

func TestCommentServiceRejectsMissingAndUnsupportedTargetsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewCommentService(client, objectref.NewEntDirectory(client))
	const companyID int64 = 105
	user := client.User.Create().SetCompanyID(companyID).SetEmail("bad-comment-target@example.test").SetPasswordHash("hash").SetName("Bad Target").SetRole("dispatcher").SaveX(ctx)
	item := client.Item.Create().SetName("Unsupported Comment Target").SaveX(ctx)

	if _, err := svc.ListForObject(ctx, companyID, objectref.New(objectref.TypeCustomer, 999999)); err == nil {
		t.Fatal("ListForObject missing target error = nil")
	}
	if _, err := svc.Create(ctx, companyID, objectref.New(objectref.TypeCustomer, 999999), user.ID, "missing"); err == nil {
		t.Fatal("Create missing target error = nil")
	}
	if _, err := svc.ListForObject(ctx, companyID, objectref.New(objectref.TypeItem, item.ID)); err == nil {
		t.Fatal("ListForObject unsupported target error = nil")
	}
	if _, err := svc.Create(ctx, companyID, objectref.New(objectref.TypeItem, item.ID), user.ID, "unsupported"); err == nil {
		t.Fatal("Create unsupported target error = nil")
	}
}

func TestCommentServiceIsolatesCompaniesIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	const companyA, companyB int64 = 201, 202
	userA := client.User.Create().SetCompanyID(companyA).SetEmail("comment-a@example.test").SetPasswordHash("hash").SetName("A").SetRole("dispatcher").SaveX(ctx)
	userB := client.User.Create().SetCompanyID(companyB).SetEmail("comment-b@example.test").SetPasswordHash("hash").SetName("B").SetRole("dispatcher").SaveX(ctx)
	targetA := client.Customer.Create().SetCompanyID(companyA).SetDisplayName("A").SaveX(ctx)
	targetB := client.Customer.Create().SetCompanyID(companyB).SetDisplayName("B").SaveX(ctx)
	refA := objectref.New(objectref.TypeCustomer, targetA.ID)
	refB := objectref.New(objectref.TypeCustomer, targetB.ID)
	svc := NewCommentService(client, objectref.NewEntDirectory(client))

	created, err := svc.Create(ctx, companyA, refA, userA.ID, "owned")
	if err != nil {
		t.Fatalf("Create own comment: %v", err)
	}
	if created.CompanyID != companyA {
		t.Fatalf("created company_id=%d want %d", created.CompanyID, companyA)
	}
	if _, err = svc.Create(ctx, companyA, refB, userA.ID, "foreign target"); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("foreign target error=%v, want ErrCommentNotFound", err)
	}
	if _, err = svc.Create(ctx, companyA, refA, userB.ID, "foreign author"); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("foreign author error=%v, want ErrCommentNotFound", err)
	}
	if _, err = svc.GetByID(ctx, companyB, created.ID); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("foreign get error=%v, want ErrCommentNotFound", err)
	}
	if err = svc.Delete(ctx, companyB, refB, created.ID); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("foreign delete error=%v, want ErrCommentNotFound", err)
	}
	listed, err := svc.ListForObject(ctx, companyA, refA)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("own list=%v err=%v", listed, err)
	}
	if err = svc.Delete(ctx, companyA, refA, created.ID); err != nil {
		t.Fatalf("own delete: %v", err)
	}
}

func TestCommentServiceRejectsInvalidTenantIDsIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	svc := NewCommentService(client, &objectref.FakeDirectory{})
	ctx := context.Background()
	ref := objectref.New(objectref.TypeCustomer, 1)

	if _, err := svc.ListForObject(ctx, 0, ref); !errors.Is(err, ErrCommentInvalid) {
		t.Fatalf("ListForObject company error=%v", err)
	}
	if _, err := svc.Create(ctx, 1, ref, 0, "bad"); !errors.Is(err, ErrCommentInvalid) {
		t.Fatalf("Create author error=%v", err)
	}
	if _, err := svc.GetByID(ctx, 1, 0); !errors.Is(err, ErrCommentInvalid) {
		t.Fatalf("GetByID comment error=%v", err)
	}
	if err := svc.Delete(ctx, 1, ref, -1); !errors.Is(err, ErrCommentInvalid) {
		t.Fatalf("Delete comment error=%v", err)
	}
}
