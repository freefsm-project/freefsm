package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestTimeEntryClockInRequiresCanonicalActor(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTimeEntryService(client)
	const companyID int64 = 41
	user := client.User.Create().SetCompanyID(companyID).SetName("Tech").SetEmail("tech@example.com").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetJobType("Repair").SaveX(ctx)

	_, err := svc.ClockIn(ctx, TimeEntryCreateParams{UserID: user.ID, JobID: job.ID})
	if !errors.Is(err, authorization.ErrForbidden) || client.TimeEntry.Query().CountX(ctx) != 0 {
		t.Fatalf("unattributed clock accepted: %v", err)
	}
}

func TestTimeEntryLegacyForeignJobCallerDeniedIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()

	user := client.User.Create().SetCompanyID(51).SetName("Tech").SetEmail("foreign-job-tech@example.com").SetPasswordHash("hash").SaveX(ctx)
	customer := client.Customer.Create().SetCompanyID(52).SetDisplayName("Foreign").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(52).SetCustomerID(customer.ID).SetJobType("Foreign").SaveX(ctx)

	if _, err := NewTimeEntryService(client).ClockIn(ctx, TimeEntryCreateParams{UserID: user.ID, JobID: job.ID}); err == nil {
		t.Fatal("ClockIn error = nil, want foreign-job rejection")
	}
	if got := client.TimeEntry.Query().CountX(ctx); got != 0 {
		t.Fatalf("time entry count = %d, want 0", got)
	}
}

func TestTimeEntryGetActiveByUserForCompanyScopesCompanyIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()

	const companyID int64 = 53
	user := client.User.Create().SetCompanyID(companyID).SetName("Tech").SetEmail("scoped-active-tech@example.com").SetPasswordHash("hash").SaveX(ctx)
	entry := client.TimeEntry.Create().SetCompanyID(companyID).SetUserID(user.ID).SetClockIn(time.Now()).SaveX(ctx)
	svc := NewTimeEntryService(client)

	got, err := svc.GetActiveByUserForCompany(ctx, companyID, user.ID)
	if err != nil {
		t.Fatalf("GetActiveByUserForCompany: %v", err)
	}
	if got.ID != entry.ID {
		t.Fatalf("entry ID = %d, want %d", got.ID, entry.ID)
	}
	if _, err = svc.GetActiveByUserForCompany(ctx, companyID+1, user.ID); !ent.IsNotFound(err) {
		t.Fatalf("foreign-company error = %v, want ent not found", err)
	}
	if got, err = svc.GetActiveByUser(ctx, user.ID); err != nil || got.ID != entry.ID {
		t.Fatalf("GetActiveByUser compatibility result = (%v, %v), want entry %d", got, err, entry.ID)
	}
}

func TestTimeEntryManualCreateRequiresCanonicalActorIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()

	const companyID int64 = 54
	user := client.User.Create().SetCompanyID(companyID).SetName("Tech").SetEmail("manual-entry-tech@example.com").SetPasswordHash("hash").SaveX(ctx)
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetJobType("Repair").SaveX(ctx)

	_, err := NewTimeEntryService(client).Create(ctx, TimeEntryCreateParams{UserID: user.ID, JobID: job.ID})
	if !errors.Is(err, authorization.ErrForbidden) || client.TimeEntry.Query().CountX(ctx) != 0 {
		t.Fatalf("unattributed manual time accepted: %v", err)
	}
}

func TestTimeEntryUnattributedCreationDeniedForAllLegacySubjectsAndJobs(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()

	const companyID int64 = 55
	active := client.User.Create().SetCompanyID(companyID).SetName("Active").SetEmail("creation-active@example.com").SetPasswordHash("hash").SaveX(ctx)
	inactive := client.User.Create().SetCompanyID(companyID).SetName("Inactive").SetEmail("creation-inactive@example.com").SetPasswordHash("hash").SetIsActive(false).SaveX(ctx)
	companyless := client.User.Create().SetName("Companyless").SetEmail("creation-companyless@example.com").SetPasswordHash("hash").SaveX(ctx)
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Customer").SaveX(ctx)
	archived := client.Job.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetJobType("Archived").SetDeletedAt(time.Now()).SaveX(ctx)
	foreignCustomer := client.Customer.Create().SetCompanyID(companyID + 1).SetDisplayName("Foreign").SaveX(ctx)
	foreign := client.Job.Create().SetCompanyID(companyID + 1).SetCustomerID(foreignCustomer.ID).SetJobType("Foreign").SaveX(ctx)
	svc := NewTimeEntryService(client)

	creators := []struct {
		name string
		call func(TimeEntryCreateParams) (*ent.TimeEntry, error)
	}{
		{name: "ClockIn", call: func(params TimeEntryCreateParams) (*ent.TimeEntry, error) { return svc.ClockIn(ctx, params) }},
		{name: "Create", call: func(params TimeEntryCreateParams) (*ent.TimeEntry, error) { return svc.Create(ctx, params) }},
	}
	for _, creator := range creators {
		for _, params := range []TimeEntryCreateParams{
			{UserID: inactive.ID},
			{UserID: companyless.ID},
			{UserID: 999999},
			{UserID: active.ID, JobID: archived.ID},
			{UserID: active.ID, JobID: foreign.ID},
		} {
			if _, err := creator.call(params); !errors.Is(err, authorization.ErrForbidden) {
				t.Fatalf("%s(%+v) error = %v, want canonical actor denial", creator.name, params, err)
			}
		}
	}
	if got := client.TimeEntry.Query().CountX(ctx); got != 0 {
		t.Fatalf("time entry count = %d, want 0", got)
	}
}

func TestTimeEntryConcurrentLegacyClockInDeniedIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	user := client.User.Create().SetCompanyID(61).SetName("Tech").SetEmail("concurrent-clock-in@example.com").SetPasswordHash("hash").SaveX(ctx)
	svc := NewTimeEntryService(client)

	const requests = 8
	errs := make(chan error, requests)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.ClockIn(ctx, TimeEntryCreateParams{UserID: user.ID})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded := 0
	for err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		if !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("ClockIn error = %v, want canonical actor denial", err)
		}
	}
	if succeeded != 0 {
		t.Fatalf("unattributed clock-ins = %d, want 0", succeeded)
	}
	if got := client.TimeEntry.Query().CountX(ctx); got != 0 {
		t.Fatalf("time entry count = %d, want 0", got)
	}
}

func TestTimeEntryConcurrentLegacyClockOutDeniedIntegration(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()
	ctx := context.Background()
	user := client.User.Create().SetCompanyID(71).SetName("Tech").SetEmail("concurrent-clock-out@example.com").SetPasswordHash("hash").SaveX(ctx)
	entry := client.TimeEntry.Create().SetCompanyID(71).SetUserID(user.ID).SetClockIn(time.Now()).SaveX(ctx)
	svc := NewTimeEntryService(client)

	errs := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.ClockOut(ctx, entry.ID)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded, inactive := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, authorization.ErrForbidden):
			inactive++
		default:
			t.Fatalf("ClockOut error = %v", err)
		}
	}
	if succeeded != 0 || inactive != 2 {
		t.Fatalf("clock-out results = %d success, %d denied; want 0 and 2", succeeded, inactive)
	}
}

func TestActiveTimeEntryConflictMapping(t *testing.T) {
	matching := fmt.Errorf("save: %w", &pgconn.PgError{Code: "23505", ConstraintName: activeTimeEntryIndex})
	if !isActiveTimeEntryConflict(matching) {
		t.Fatal("named active-entry unique violation was not recognized")
	}
	other := &pgconn.PgError{Code: "23505", ConstraintName: "other_unique_index"}
	if isActiveTimeEntryConflict(other) {
		t.Fatal("unrelated unique violation was recognized as an active-entry conflict")
	}
}

func TestTimeEntryUpdateRequiresCanonicalActor(t *testing.T) {
	client := openPolicyTestClient(t)
	defer client.Close()

	ctx := context.Background()
	svc := NewTimeEntryService(client)
	user := client.User.Create().SetName("Tech").SetEmail("tech-update@example.com").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Customer").SaveX(ctx)
	job := client.Job.Create().SetCustomerID(customer.ID).SetJobType("Repair").SaveX(ctx)
	entry := client.TimeEntry.Create().SetUserID(user.ID).SetClockIn(time.Now()).SaveX(ctx)

	_, err := svc.Update(ctx, entry.ID, TimeEntryUpdateParams{JobID: &job.ID})
	if !errors.Is(err, authorization.ErrForbidden) || client.TimeEntry.GetX(ctx, entry.ID).JobID != nil {
		t.Fatalf("unattributed correction accepted: %v", err)
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
	boundaryStep := time.Microsecond

	client.TimeEntry.Create().SetUserID(selectedUser.ID).SetClockIn(from).SetClockOut(from.Add(time.Hour)).SetNotes("matching repair").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(selectedUser.ID).SetClockIn(before.Add(-boundaryStep)).SetClockOut(before.Add(time.Hour - boundaryStep)).SetNotes("matching repair").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(selectedUser.ID).SetClockIn(from.Add(-boundaryStep)).SetClockOut(from.Add(time.Hour - boundaryStep)).SetNotes("matching repair before range").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(selectedUser.ID).SetClockIn(before).SetClockOut(before.Add(time.Hour)).SetNotes("matching repair after range").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(selectedUser.ID).SetClockIn(from.Add(time.Hour)).SetClockOut(from.Add(2 * time.Hour)).SetNotes("different work").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(otherUser.ID).SetClockIn(from.Add(time.Hour)).SetClockOut(from.Add(2 * time.Hour)).SetNotes("matching repair other user").SaveX(ctx)

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
	if !entries[0].ClockIn.Equal(before.Add(-boundaryStep)) || !entries[1].ClockIn.Equal(from) {
		t.Fatalf("clock-ins = [%s, %s], want exclusive upper and inclusive lower boundaries", entries[0].ClockIn, entries[1].ClockIn)
	}
}
