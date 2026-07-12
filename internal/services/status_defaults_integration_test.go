package services

import (
	"context"
	"testing"
	"time"
)

func TestCreationDefaultsUseCategoriesAfterLabelsAreRenamed(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	customer := client.Customer.Create().SetDisplayName("Defaults customer").SaveX(ctx)
	client.CompanySettings.Create().SetBusinessName("Defaults").SaveX(ctx)
	defaults := map[string]struct{ category, label string }{
		"job":      {"job:new", "Queued locally"},
		"project":  {"project:new", "Sales intake"},
		"estimate": {"estimate:draft", "Working copy"},
		"invoice":  {"invoice:draft", "Unreleased"},
	}
	statusIDs := map[string]int64{}
	for typ, item := range defaults {
		workflow := client.StatusWorkflow.Create().SetName(typ).SetObjectType(typ).SaveX(ctx)
		statusIDs[typ] = client.Status.Create().SetWorkflowID(workflow.ID).SetName(item.label).SetCategoryKey(item.category).SetCategoryOrder(1).SetIsCategoryDefault(true).SaveX(ctx).ID
	}

	jobSvc := NewJobService(client)
	job, err := jobSvc.Create(ctx, JobCreateParams{CustomerID: customer.ID, JobType: "Install"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := NewProjectService(client).Create(ctx, ProjectCreateParams{CustomerID: customer.ID, Name: "Project"})
	if err != nil {
		t.Fatal(err)
	}
	estimate, err := NewEstimateService(client).Create(ctx, EstimateCreateParams{CustomerID: customer.ID, Title: "Estimate"})
	if err != nil {
		t.Fatal(err)
	}
	invoice, err := NewInvoiceService(client).Create(ctx, InvoiceCreateParams{CustomerID: customer.ID, Title: "Invoice", InvoiceDate: time.Now(), DueDate: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	next, err := jobSvc.CreateNextOccurrence(ctx, job.ID, time.Now().AddDate(0, 0, 7))
	if err != nil {
		t.Fatal(err)
	}
	assertStatusID(t, job.StatusID, statusIDs["job"])
	assertStatusID(t, next.StatusID, statusIDs["job"])
	assertStatusID(t, project.StatusID, statusIDs["project"])
	assertStatusID(t, estimate.StatusID, statusIDs["estimate"])
	assertStatusID(t, invoice.StatusID, statusIDs["invoice"])
}

func assertStatusID(t *testing.T, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("status=%v want %d", got, want)
	}
}
