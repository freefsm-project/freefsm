package services

import (
	"context"
	"testing"
	"time"
)

func TestCustomerListAllForCompanyExcludesForeignCompanylessAndDeletedIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()

	client.Customer.Create().SetCompanyID(71).SetDisplayName("Zulu").SaveX(ctx)
	wantFirst := client.Customer.Create().SetCompanyID(71).SetDisplayName("Alpha").SaveX(ctx)
	client.Customer.Create().SetCompanyID(72).SetDisplayName("Foreign").SaveX(ctx)
	client.Customer.Create().SetDisplayName("Companyless").SaveX(ctx)
	client.Customer.Create().SetCompanyID(71).SetDisplayName("Deleted").SetDeletedAt(time.Now()).SaveX(ctx)

	got, err := NewCustomerService(client).ListAllForCompany(ctx, 71)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != wantFirst.ID || got[0].DisplayName != "Alpha" || got[1].DisplayName != "Zulu" {
		t.Fatalf("tenant customers = %#v, want Alpha and Zulu only in name order", got)
	}
}

func TestUserListActiveForCompanyExcludesForeignCompanylessAndInactiveIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()

	wantFirst := client.User.Create().SetCompanyID(81).SetEmail("alpha-tenant-options@example.test").SetPasswordHash("hash").SetName("Alpha").SaveX(ctx)
	client.User.Create().SetCompanyID(81).SetEmail("zulu-tenant-options@example.test").SetPasswordHash("hash").SetName("Zulu").SaveX(ctx)
	client.User.Create().SetCompanyID(82).SetEmail("foreign-tenant-options@example.test").SetPasswordHash("hash").SetName("Foreign").SaveX(ctx)
	client.User.Create().SetEmail("companyless-tenant-options@example.test").SetPasswordHash("hash").SetName("Companyless").SaveX(ctx)
	client.User.Create().SetCompanyID(81).SetEmail("inactive-tenant-options@example.test").SetPasswordHash("hash").SetName("Inactive").SetIsActive(false).SaveX(ctx)

	got, err := NewUserService(client).ListActiveForCompany(ctx, 81)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != wantFirst.ID || got[0].Name != "Alpha" || got[1].Name != "Zulu" {
		t.Fatalf("active tenant users = %#v, want Alpha and Zulu only in name order", got)
	}
}

func TestUserListByIDsForCompanyIncludesInactiveTenantUserOnlyIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()

	inactive := client.User.Create().SetCompanyID(91).SetEmail("inactive-existing-assignment@example.test").SetPasswordHash("hash").SetName("Inactive Tenant User").SetIsActive(false).SaveX(ctx)
	foreign := client.User.Create().SetCompanyID(92).SetEmail("foreign-existing-assignment@example.test").SetPasswordHash("hash").SetName("Foreign User").SaveX(ctx)
	companyless := client.User.Create().SetEmail("companyless-existing-assignment@example.test").SetPasswordHash("hash").SetName("Companyless User").SaveX(ctx)

	got, err := NewUserService(client).ListByIDsForCompany(ctx, 91, []int64{inactive.ID, foreign.ID, companyless.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != inactive.ID {
		t.Fatalf("scoped existing assignment users = %#v, want inactive tenant user only", got)
	}
}

func TestJobUpdatePreservesExistingInactiveTenantAssignmentIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	const companyID int64 = 101

	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Assignment Customer").SaveX(ctx)
	createJobDefaultStatus(t, ctx, client, companyID, "New", 1)
	inactive := client.User.Create().SetCompanyID(companyID).SetEmail("preserved-inactive-assignment@example.test").SetPasswordHash("hash").SetName("Former Technician").SetIsActive(false).SaveX(ctx)
	job := client.Job.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetJobType("Repair").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(inactive.ID).SetRole("helper").SaveX(ctx)

	assignments := []JobAssignment{{UserID: inactive.ID, Name: inactive.Name, Role: "helper"}}
	if _, err := NewJobService(client).Update(ctx, companyID, job.ID, JobUpdateParams{Assignments: &assignments}); err != nil {
		t.Fatalf("update with existing inactive tenant assignment: %v", err)
	}
	rows := client.JobAssignment.Query().AllX(ctx)
	if len(rows) != 1 || rows[0].JobID != job.ID || rows[0].UserID != inactive.ID || rows[0].Role != "helper" {
		t.Fatalf("preserved job assignments = %#v, want inactive tenant assignment unchanged", rows)
	}
}
