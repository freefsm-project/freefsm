package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTimeEntryClockInStoresJobAndBlocksActiveEntry(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTimeEntryService(client)
	user := client.User.Create().SetName("Tech").SetEmail("tech@example.com").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Customer").SaveX(ctx)
	job := client.Job.Create().SetCustomerID(customer.ID).SetJobType("Repair").SaveX(ctx)

	entry, err := svc.ClockIn(ctx, TimeEntryCreateParams{UserID: user.ID, JobID: job.ID})
	if err != nil {
		t.Fatalf("ClockIn: %v", err)
	}
	if entry.JobID == nil || *entry.JobID != job.ID {
		t.Fatalf("JobID = %v, want %d", entry.JobID, job.ID)
	}

	_, err = svc.ClockIn(ctx, TimeEntryCreateParams{UserID: user.ID})
	if !errors.Is(err, ErrActiveTimeEntry) {
		t.Fatalf("ClockIn active error = %v, want ErrActiveTimeEntry", err)
	}
}

func TestTimeEntryUpdateSetsAndClearsJob(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTimeEntryService(client)
	user := client.User.Create().SetName("Tech").SetEmail("tech-update@example.com").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Customer").SaveX(ctx)
	job := client.Job.Create().SetCustomerID(customer.ID).SetJobType("Repair").SaveX(ctx)
	entry := client.TimeEntry.Create().SetUserID(user.ID).SetClockIn(time.Now()).SaveX(ctx)

	updated, err := svc.Update(ctx, entry.ID, TimeEntryUpdateParams{JobID: &job.ID})
	if err != nil {
		t.Fatalf("Update set job: %v", err)
	}
	if updated.JobID == nil || *updated.JobID != job.ID {
		t.Fatalf("JobID = %v, want %d", updated.JobID, job.ID)
	}

	updated, err = svc.Update(ctx, entry.ID, TimeEntryUpdateParams{ClearJob: true})
	if err != nil {
		t.Fatalf("Update clear job: %v", err)
	}
	if updated.JobID != nil {
		t.Fatalf("JobID = %v, want nil", updated.JobID)
	}
}
