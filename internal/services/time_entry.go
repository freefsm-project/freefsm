package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/timeentry"
	"github.com/jackc/pgx/v5/pgconn"
)

type TimeEntryService struct {
	client *ent.Client
}

var (
	ErrActiveTimeEntry    = errors.New("user already has an active time entry")
	ErrTimeEntryNotActive = errors.New("time entry is not active")
)

const activeTimeEntryIndex = "time_entries_one_active_per_user"

func NewTimeEntryService(client *ent.Client) *TimeEntryService {
	return &TimeEntryService{client: client}
}

type TimeEntryCreateParams struct {
	ClockIn, ClockOut *time.Time
	UserID            int64
	JobID             int64
	IsManual          bool
	Notes             string
	Latitude          *float64
	Longitude         *float64
}

type TimeEntryUpdateParams struct {
	Reason   string
	Reopen   bool
	IsManual *bool
	ClockIn  *time.Time
	ClockOut *time.Time
	JobID    *int64
	ClearJob bool
	Notes    *string
}

type TimeEntryListFilter struct {
	IncludeVoid   bool
	UserID        int64
	Search        string
	ClockInFrom   *time.Time
	ClockInBefore *time.Time
}

func (s *TimeEntryService) List(ctx context.Context, filter TimeEntryListFilter, page, perPage int) ([]*ent.TimeEntry, int, error) {
	q := s.client.TimeEntry.Query().
		Order(ent.Desc(timeentry.FieldClockIn))
	if !filter.IncludeVoid {
		q = q.Where(timeentry.VoidedAtIsNil())
	}

	if filter.UserID > 0 {
		q = q.Where(timeentry.UserIDEQ(filter.UserID))
	}
	if filter.Search != "" {
		q = q.Where(timeentry.NotesContainsFold(filter.Search))
	}
	if filter.ClockInFrom != nil {
		q = q.Where(timeentry.ClockInGTE(*filter.ClockInFrom))
	}
	if filter.ClockInBefore != nil {
		q = q.Where(timeentry.ClockInLT(*filter.ClockInBefore))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count time entries: %w", err)
	}

	offset := PaginationOffset(page, perPage)
	entries, err := q.
		Limit(perPage).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list time entries: %w", err)
	}

	return entries, total, nil
}

func (s *TimeEntryService) GetByID(ctx context.Context, id int64) (*ent.TimeEntry, error) {
	te, err := s.client.TimeEntry.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get time entry %d: %w", id, err)
	}
	return te, nil
}

func (s *TimeEntryService) GetActiveByUser(ctx context.Context, userID int64) (*ent.TimeEntry, error) {
	te, err := s.client.TimeEntry.Query().
		Where(
			timeentry.UserIDEQ(userID),
			timeentry.ClockOutIsNil(),
			timeentry.VoidedAtIsNil(),
		).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("get active time entry: %w", err)
	}
	return te, nil
}

func (s *TimeEntryService) GetActiveByUserForCompany(ctx context.Context, companyID, userID int64) (*ent.TimeEntry, error) {
	te, err := s.client.TimeEntry.Query().
		Where(
			timeentry.CompanyIDEQ(companyID),
			timeentry.UserIDEQ(userID),
			timeentry.ClockOutIsNil(),
			timeentry.VoidedAtIsNil(),
		).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("get active time entry for company: %w", err)
	}
	return te, nil
}

func (s *TimeEntryService) HasActiveEntry(ctx context.Context, userID int64) (bool, error) {
	count, err := s.client.TimeEntry.Query().
		Where(
			timeentry.UserIDEQ(userID),
			timeentry.ClockOutIsNil(),
			timeentry.VoidedAtIsNil(),
		).
		Count(ctx)
	if err != nil {
		return false, fmt.Errorf("check active entry: %w", err)
	}
	return count > 0, nil
}

func (s *TimeEntryService) ensureNoActiveEntry(ctx context.Context, userID int64) error {
	hasActive, err := s.HasActiveEntry(ctx, userID)
	if err != nil {
		return err
	}
	if hasActive {
		return ErrActiveTimeEntry
	}
	return nil
}

func (s *TimeEntryService) ClockIn(ctx context.Context, params TimeEntryCreateParams) (*ent.TimeEntry, error) {
	req, ok := authorization.FromContext(ctx)
	if !ok || params.UserID != req.Actor.UserID {
		return nil, authorization.ErrForbidden
	}
	if params.Latitude != nil || params.Longitude != nil {
		return nil, &authorization.ClockConflict{Code: "invalid_gps"}
	}
	entry, err := req.Service.Clock(ctx, req.Actor, false, authorization.ClockCommand{JobID: params.JobID, Notes: params.Notes})
	if err != nil {
		return nil, err
	}
	return s.GetByID(ctx, entry.ID)
}

func (s *TimeEntryService) ClockOut(ctx context.Context, entryID int64) (*ent.TimeEntry, error) {
	req, ok := authorization.FromContext(ctx)
	if !ok {
		return nil, authorization.ErrForbidden
	}
	entry, err := req.Service.Clock(ctx, req.Actor, true, authorization.ClockCommand{EntryID: entryID})
	if err != nil {
		return nil, err
	}
	return s.GetByID(ctx, entry.ID)
}

func isActiveTimeEntryConflict(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505" && pgErr.ConstraintName == activeTimeEntryIndex
	}
	return ent.IsConstraintError(err) && strings.Contains(err.Error(), activeTimeEntryIndex)
}

func (s *TimeEntryService) Create(ctx context.Context, params TimeEntryCreateParams) (*ent.TimeEntry, error) {
	req, ok := authorization.FromContext(ctx)
	if !ok {
		return nil, authorization.ErrForbidden
	}
	if params.Latitude != nil || params.Longitude != nil {
		return nil, &authorization.ClockConflict{Code: "invalid_gps"}
	}
	id, err := req.Service.CreateManualTimeEntry(ctx, req.Actor, authorization.ManualTimeEntry{UserID: params.UserID, JobID: params.JobID, ClockIn: params.ClockIn, ClockOut: params.ClockOut, Notes: params.Notes})
	if err != nil {
		return nil, err
	}
	return s.GetByID(ctx, id)
}

func (s *TimeEntryService) Update(ctx context.Context, id int64, params TimeEntryUpdateParams) (*ent.TimeEntry, error) {
	if params.IsManual != nil {
		return nil, authorization.ErrForbidden
	}
	req, ok := authorization.FromContext(ctx)
	if !ok {
		return nil, authorization.ErrForbidden
	}
	err := req.Service.CorrectTimeEntry(ctx, req.Actor, id, authorization.TimeEntryCorrection{Reason: params.Reason, Reopen: params.Reopen, ClockIn: params.ClockIn, ClockOut: params.ClockOut, JobID: params.JobID, ClearJob: params.ClearJob, Notes: params.Notes})
	if err != nil {
		return nil, err
	}
	return s.GetByID(ctx, id)
}

func (s *TimeEntryService) Delete(ctx context.Context, id int64) error {
	// Time is retained history. The reasoned terminal Void workflow replaces
	// this obsolete destructive contract; an ID alone never authorizes erasure.
	return authorization.ErrForbidden
}

func TimeEntryDuration(clockIn, clockOut time.Time) string {
	var d time.Duration
	if clockOut.IsZero() {
		d = time.Since(clockIn)
	} else {
		d = clockOut.Sub(clockIn)
	}
	if d < 0 {
		return "Invalid recorded interval"
	}
	return d.String()
}

func TimeEntryPaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}
