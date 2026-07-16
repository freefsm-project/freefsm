package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/timeentry"
)

type TimeEntryService struct {
	client *ent.Client
}

var ErrActiveTimeEntry = errors.New("user already has an active time entry")

func NewTimeEntryService(client *ent.Client) *TimeEntryService {
	return &TimeEntryService{client: client}
}

type TimeEntryCreateParams struct {
	UserID    int64
	JobID     int64
	IsManual  bool
	Notes     string
	Latitude  *float64
	Longitude *float64
}

type TimeEntryUpdateParams struct {
	IsManual *bool
	ClockIn  *time.Time
	ClockOut *time.Time
	JobID    *int64
	ClearJob bool
	Notes    *string
}

type TimeEntryListFilter struct {
	UserID        int64
	Search        string
	ClockInFrom   *time.Time
	ClockInBefore *time.Time
}

func (s *TimeEntryService) List(ctx context.Context, filter TimeEntryListFilter, page, perPage int) ([]*ent.TimeEntry, int, error) {
	q := s.client.TimeEntry.Query().
		Order(ent.Desc(timeentry.FieldClockIn))

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
		).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("get active time entry: %w", err)
	}
	return te, nil
}

func (s *TimeEntryService) HasActiveEntry(ctx context.Context, userID int64) (bool, error) {
	count, err := s.client.TimeEntry.Query().
		Where(
			timeentry.UserIDEQ(userID),
			timeentry.ClockOutIsNil(),
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
	if err := s.ensureNoActiveEntry(ctx, params.UserID); err != nil {
		return nil, err
	}
	c := s.client.TimeEntry.
		Create().
		SetUserID(params.UserID).
		SetIsManual(false).
		SetClockIn(time.Now()).
		SetNotes(params.Notes).
		SetNillableLatitude(params.Latitude).
		SetNillableLongitude(params.Longitude)
	if params.JobID > 0 {
		c.SetJobID(params.JobID)
	}
	te, err := c.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("clock in: %w", err)
	}
	return te, nil
}

func (s *TimeEntryService) ClockOut(ctx context.Context, entryID int64) (*ent.TimeEntry, error) {
	te, err := s.client.TimeEntry.UpdateOneID(entryID).
		SetClockOut(time.Now()).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("clock out: %w", err)
	}
	return te, nil
}

func (s *TimeEntryService) Create(ctx context.Context, params TimeEntryCreateParams) (*ent.TimeEntry, error) {
	te, err := s.client.TimeEntry.
		Create().
		SetUserID(params.UserID).
		SetIsManual(true).
		SetClockIn(time.Now()).
		SetNotes(params.Notes).
		SetNillableLatitude(params.Latitude).
		SetNillableLongitude(params.Longitude).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create time entry: %w", err)
	}
	return te, nil
}

func (s *TimeEntryService) Update(ctx context.Context, id int64, params TimeEntryUpdateParams) (*ent.TimeEntry, error) {
	u := s.client.TimeEntry.UpdateOneID(id)

	if params.IsManual != nil {
		u.SetIsManual(*params.IsManual)
	}
	if params.ClockIn != nil {
		u.SetClockIn(*params.ClockIn)
	}
	if params.ClockOut != nil {
		u.SetClockOut(*params.ClockOut)
	}
	if params.ClearJob {
		u.ClearJobID()
	} else if params.JobID != nil {
		u.SetJobID(*params.JobID)
	}
	if params.Notes != nil {
		u.SetNotes(*params.Notes)
	}

	te, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update time entry %d: %w", id, err)
	}
	return te, nil
}

func (s *TimeEntryService) Delete(ctx context.Context, id int64) error {
	if err := s.client.TimeEntry.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete time entry %d: %w", id, err)
	}
	return nil
}

func TimeEntryDuration(clockIn, clockOut time.Time) string {
	var d time.Duration
	if clockOut.IsZero() || clockOut.Before(clockIn) {
		d = time.Since(clockIn)
	} else {
		d = clockOut.Sub(clockIn)
	}

	hours := int(math.Floor(d.Hours()))
	minutes := int(math.Ceil(d.Minutes())) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

func TimeEntryPaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}
