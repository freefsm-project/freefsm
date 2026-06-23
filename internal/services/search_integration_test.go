package services

import (
	"context"
	"testing"
)

func TestSearchTechScopeFindsAssignedResultBeyondBroadLimit(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	search := NewSearchService(client)
	tech := client.User.Create().SetEmail("search-tech@example.test").SetPasswordHash("hash").SetName("Search Tech").SetRole("tech").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Search Customer").SaveX(ctx)

	for i := 0; i < 6; i++ {
		client.Job.Create().SetCustomerID(customer.ID).SetJobType("Needle Unassigned").SaveX(ctx)
	}
	assignedJob := client.Job.Create().SetCustomerID(customer.ID).SetJobType("Needle Assigned").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(assignedJob.ID).SetUserID(tech.ID).SaveX(ctx)

	_, jobs, _, invoices, estimates, err := search.Search(ctx, "Needle", 1, tech.ID, tech.Role)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}
	if jobs[0].ID != assignedJob.ID {
		t.Fatalf("job result ID = %d, want assigned job %d", jobs[0].ID, assignedJob.ID)
	}
	if len(invoices) != 0 || len(estimates) != 0 {
		t.Fatalf("tech billing results = invoices %d estimates %d, want none", len(invoices), len(estimates))
	}
}
