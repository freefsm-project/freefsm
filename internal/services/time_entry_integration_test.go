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

func TestTimeEntryListFiltersByUserSearchAndClockInRange(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTimeEntryService(client)
	selectedUser := client.User.Create().SetName("Selected Tech").SetEmail("selected-list@example.com").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	otherUser := client.User.Create().SetName("Other Tech").SetEmail("other-list@example.com").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	from := time.Date(2026, time.July, 10, 4, 0, 0, 0, time.UTC)
	before := time.Date(2026, time.July, 11, 4, 0, 0, 0, time.UTC)

	client.TimeEntry.Create().SetUserID(selectedUser.ID).SetClockIn(from).SetNotes("matching repair").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(selectedUser.ID).SetClockIn(before.Add(-time.Nanosecond)).SetNotes("matching repair").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(selectedUser.ID).SetClockIn(from.Add(-time.Nanosecond)).SetNotes("matching repair before range").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(selectedUser.ID).SetClockIn(before).SetNotes("matching repair after range").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(selectedUser.ID).SetClockIn(from.Add(time.Hour)).SetNotes("different work").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(otherUser.ID).SetClockIn(from.Add(time.Hour)).SetNotes("matching repair other user").SaveX(ctx)

	entries, total, err := svc.List(ctx, TimeEntryListFilter{
		UserID:        selectedUser.ID,
		Search:        "MATCHING",
		ClockInFrom:   &from,
		ClockInBefore: &before,
	}, 1, 25)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(entries) != 2 {
		t.Fatalf("total=%d len(entries)=%d, want 2", total, len(entries))
	}
	if !entries[0].ClockIn.Equal(before.Add(-time.Nanosecond)) || !entries[1].ClockIn.Equal(from) {
		t.Fatalf("clock-ins = [%s, %s], want exclusive upper and inclusive lower boundaries", entries[0].ClockIn, entries[1].ClockIn)
	}
}
