package services

import (
	"context"
	"errors"
	"testing"

	"github.com/freefsm-project/freefsm/internal/ent/job"
)

func TestStatusServiceJobDeleteRequiresReplacementAndReassigns(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewStatusService(client)
	wf := client.StatusWorkflow.Create().SetName("Jobs").SetObjectType("job").SaveX(ctx)
	oldStatus := client.Status.Create().SetWorkflowID(wf.ID).SetName("Old").SaveX(ctx)
	replacement := client.Status.Create().SetWorkflowID(wf.ID).SetName("New").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Customer").SaveX(ctx)
	client.Job.Create().SetCustomerID(customer.ID).SetJobType("Job").SetStatusID(oldStatus.ID).SaveX(ctx)

	if err := svc.Delete(ctx, "job", oldStatus.ID, nil); !errors.Is(err, ErrReplacementStatusNeeded) {
		t.Fatalf("Delete without replacement error = %v, want %v", err, ErrReplacementStatusNeeded)
	}
	if err := svc.Delete(ctx, "job", oldStatus.ID, &replacement.ID); err != nil {
		t.Fatalf("Delete with replacement: %v", err)
	}
	count := client.Job.Query().Where(job.StatusIDEQ(replacement.ID)).CountX(ctx)
	if count != 1 {
		t.Fatalf("jobs reassigned to replacement = %d, want 1", count)
	}
}

func TestStatusServiceDeletesUnusedJobStatusWithoutReplacement(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewStatusService(client)
	wf := client.StatusWorkflow.Create().SetName("Jobs").SetObjectType("job").SaveX(ctx)
	unused := client.Status.Create().SetWorkflowID(wf.ID).SetName("Unused").SaveX(ctx)

	if err := svc.Delete(ctx, "job", unused.ID, nil); err != nil {
		t.Fatalf("Delete unused status: %v", err)
	}
}

func TestStatusServiceBelongsToObjectType(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewStatusService(client)
	jobWorkflow := client.StatusWorkflow.Create().SetName("Jobs").SetObjectType("job").SaveX(ctx)
	invoiceWorkflow := client.StatusWorkflow.Create().SetName("Invoices").SetObjectType("invoice").SaveX(ctx)
	jobStatus := client.Status.Create().SetWorkflowID(jobWorkflow.ID).SetName("Job Status").SaveX(ctx)
	invoiceStatus := client.Status.Create().SetWorkflowID(invoiceWorkflow.ID).SetName("Invoice Status").SaveX(ctx)

	ok, err := svc.BelongsToObjectType(ctx, jobStatus.ID, "job")
	if err != nil || !ok {
		t.Fatalf("job status belongs to job = %v, %v; want true, nil", ok, err)
	}
	ok, err = svc.BelongsToObjectType(ctx, invoiceStatus.ID, "job")
	if err != nil || ok {
		t.Fatalf("invoice status belongs to job = %v, %v; want false, nil", ok, err)
	}
}

func TestStatusServiceValidatesConversionCapabilities(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	svc := NewStatusService(client)
	jobs := client.StatusWorkflow.Create().SetName("Jobs").SetObjectType("job").SaveX(ctx)
	jobStatus := client.Status.Create().SetWorkflowID(jobs.ID).SetName("Ready").SaveX(ctx)
	if _, err := svc.UpdateDocumentCapabilities(ctx, jobStatus.ID, true, "standard"); !errors.Is(err, ErrInvalidStatusCapability) {
		t.Fatalf("job convertible error=%v", err)
	}
	estimates := client.StatusWorkflow.Create().SetName("Estimates").SetObjectType("estimate").SaveX(ctx)
	draft := client.Status.Create().SetWorkflowID(estimates.ID).SetName("Initial").SetDocumentRole("draft").SaveX(ctx)
	approved := client.Status.Create().SetWorkflowID(estimates.ID).SetName("Approved").SaveX(ctx)
	if _, err := svc.UpdateDocumentCapabilities(ctx, draft.ID, false, "standard"); !errors.Is(err, ErrDraftStatusRequired) {
		t.Fatalf("draft removal error=%v", err)
	}
	updated, err := svc.UpdateDocumentCapabilities(ctx, approved.ID, true, "draft")
	if err != nil {
		t.Fatal(err)
	}
	if !updated.EstimateConvertible || updated.DocumentRole != "draft" {
		t.Fatalf("updated=%#v", updated)
	}
	draft = client.Status.GetX(ctx, draft.ID)
	if draft.DocumentRole != "standard" {
		t.Fatalf("previous draft role=%q", draft.DocumentRole)
	}
}
