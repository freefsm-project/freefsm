package services

import (
	"context"
	"testing"
)

func TestStatusServiceBelongsToObjectType(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewStatusService(client)
	jobWorkflow := client.StatusWorkflow.Create().SetName("Jobs").SetObjectType("job").SaveX(ctx)
	invoiceWorkflow := client.StatusWorkflow.Create().SetName("Invoices").SetObjectType("invoice").SaveX(ctx)
	jobStatus := client.Status.Create().SetWorkflowID(jobWorkflow.ID).SetName("Job Status").SetCategoryKey("job:pending").SetCategoryOrder(1).SetIsCategoryDefault(true).SaveX(ctx)
	invoiceStatus := client.Status.Create().SetWorkflowID(invoiceWorkflow.ID).SetName("Invoice Status").SetCategoryKey("invoice:invoiced").SetCategoryOrder(1).SetIsCategoryDefault(true).SaveX(ctx)

	ok, err := svc.BelongsToObjectType(ctx, jobStatus.ID, "job")
	if err != nil || !ok {
		t.Fatalf("job status belongs to job = %v, %v; want true, nil", ok, err)
	}
	ok, err = svc.BelongsToObjectType(ctx, invoiceStatus.ID, "job")
	if err != nil || ok {
		t.Fatalf("invoice status belongs to job = %v, %v; want false, nil", ok, err)
	}
}
