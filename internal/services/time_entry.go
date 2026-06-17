package services

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/timeentry"
)

type TimeEntryService struct {
	client *ent.Client
}

func NewTimeEntryService(client *ent.Client) *TimeEntryService {
	return &TimeEntryService{client: client}
}

type TimeEntryCreateParams struct {
	UserID    int64
	IsManual  bool
	Notes     string
	Latitude  *float64
	Longitude *float64
}

type TimeEntryUpdateParams struct {
	IsManual *bool
	ClockIn  *time.Time
	ClockOut *time.Time
	Notes    *string
}

func (s *TimeEntryService) List(ctx context.Context, userID int64, search string, page, perPage int, isAdmin bool) ([]*ent.TimeEntry, int, error) {
	q := s.client.TimeEntry.Query().
		Order(ent.Desc(timeentry.FieldClockIn))

	if !isAdmin {
		q = q.Where(timeentry.UserIDEQ(userID))
	}
	if userID > 0 && isAdmin {
		q = q.Where(timeentry.UserIDEQ(userID))
	}
	if search != "" {
		q = q.Where(timeentry.NotesContainsFold(search))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count time entries: %w", err)
	}

	offset := (page - 1) * perPage
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

func (s *TimeEntryService) ClockIn(ctx context.Context, params TimeEntryCreateParams) (*ent.TimeEntry, error) {
	te, err := s.client.TimeEntry.
		Create().
		SetUserID(params.UserID).
		SetIsManual(false).
		SetClockIn(time.Now()).
		SetNotes(params.Notes).
		SetNillableLatitude(params.Latitude).
		SetNillableLongitude(params.Longitude).
		Save(ctx)
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
	return int(math.Ceil(float64(total) / float64(perPage)))
}
