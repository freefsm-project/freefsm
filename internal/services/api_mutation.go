package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrMutationForbidden   = errors.New("mutation forbidden")
	ErrMutationJobClosed   = errors.New("mutation job closed")
	ErrMutationJobNotFound = errors.New("mutation job not found")
	ErrSubtaskNotFound     = errors.New("subtask not found")
)

// MutationActor is populated from the authenticated database user, not request data.
type MutationActor struct {
	CompanyID int64
	UserID    int64
	Name      string
	Role      string
}

type APISubtaskResult struct {
	Title     string `json:"title"`
	Completed bool   `json:"completed"`
	SortOrder int    `json:"sort_order"`
}

type APIClockInParams struct {
	Notes     string
	Latitude  *float64
	Longitude *float64
}

type APITimeEntryResult struct {
	ID        int64
	UserID    int64
	JobID     *int64
	ClockIn   time.Time
	ClockOut  *time.Time
	Notes     string
	Latitude  *float64
	Longitude *float64
}

type APIMutationService struct {
	db *pgxpool.Pool
}

func NewAPIMutationService(db *pgxpool.Pool) *APIMutationService {
	return &APIMutationService{db: db}
}

func (s *APIMutationService) SetSubtaskCompletion(ctx context.Context, actor MutationActor, jobID int64, index int, completed bool) (APISubtaskResult, error) {
	if err := validateMutation(actor, jobID); err != nil {
		return APISubtaskResult{}, err
	}
	if index < 0 {
		return APISubtaskResult{}, ErrSubtaskNotFound
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return APISubtaskResult{}, err
	}
	defer tx.Rollback(ctx) // No-op after Commit.
	if err = authorizeJobMutation(ctx, tx, actor, jobID); err != nil {
		return APISubtaskResult{}, err
	}

	var result APISubtaskResult
	err = tx.QueryRow(ctx, `
		UPDATE jobs
		SET subtasks = jsonb_set(subtasks, ARRAY[$3::text, 'completed'], to_jsonb($4::boolean), false),
		    updated_at = NOW()
		WHERE company_id = $1 AND id = $2 AND deleted_at IS NULL
		  AND jsonb_typeof(subtasks) = 'array' AND jsonb_array_length(subtasks) > $3
		RETURNING subtasks->$3->>'title',
		          (subtasks->$3->>'completed')::boolean,
		          COALESCE((subtasks->$3->>'sort_order')::integer, 0)`,
		actor.CompanyID, jobID, index, completed,
	).Scan(&result.Title, &result.Completed, &result.SortOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return APISubtaskResult{}, ErrSubtaskNotFound
	}
	if err != nil {
		return APISubtaskResult{}, fmt.Errorf("update subtask: %w", err)
	}

	action := "subtask_uncompleted"
	if completed {
		action = "subtask_completed"
	}
	if err = insertMutationActivity(ctx, tx, actor, action, "job", jobID, map[string]any{
		"actor_name":  actor.Name,
		"entity_name": result.Title,
	}); err != nil {
		return APISubtaskResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return APISubtaskResult{}, fmt.Errorf("commit subtask mutation: %w", err)
	}
	return result, nil
}

func (s *APIMutationService) ClockIn(ctx context.Context, actor MutationActor, jobID int64, params APIClockInParams) (APITimeEntryResult, error) {
	if err := validateMutation(actor, jobID); err != nil {
		return APITimeEntryResult{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return APITimeEntryResult{}, err
	}
	defer tx.Rollback(ctx)
	if err = authorizeJobMutation(ctx, tx, actor, jobID); err != nil {
		return APITimeEntryResult{}, err
	}

	var result APITimeEntryResult
	err = tx.QueryRow(ctx, `
		INSERT INTO time_entries(company_id, user_id, job_id, is_manual, clock_in, notes, latitude, longitude, created_at, updated_at)
		SELECT $1, u.id, j.id, false, NOW(), $4, $5, $6, NOW(), NOW()
		FROM users u
		JOIN jobs j ON j.id = $3 AND j.company_id = $1 AND j.deleted_at IS NULL
		WHERE u.id = $2 AND u.company_id = $1 AND u.is_active
		RETURNING id, user_id, job_id, clock_in, clock_out, notes, latitude, longitude`,
		actor.CompanyID, actor.UserID, jobID, params.Notes, params.Latitude, params.Longitude,
	).Scan(&result.ID, &result.UserID, &result.JobID, &result.ClockIn, &result.ClockOut, &result.Notes, &result.Latitude, &result.Longitude)
	if errors.Is(err, pgx.ErrNoRows) {
		return APITimeEntryResult{}, ErrMutationJobNotFound
	}
	if err != nil {
		if isAPIMutationActiveEntryConflict(err) {
			return APITimeEntryResult{}, ErrActiveTimeEntry
		}
		return APITimeEntryResult{}, fmt.Errorf("clock in: %w", err)
	}

	if err = insertTimeEntryActivities(ctx, tx, actor, result, "clocked_in"); err != nil {
		return APITimeEntryResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return APITimeEntryResult{}, fmt.Errorf("commit clock in: %w", err)
	}
	return result, nil
}

func (s *APIMutationService) ClockOut(ctx context.Context, actor MutationActor) (APITimeEntryResult, error) {
	if actor.CompanyID <= 0 || actor.UserID <= 0 {
		return APITimeEntryResult{}, fmt.Errorf("invalid mutation actor")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return APITimeEntryResult{}, err
	}
	defer tx.Rollback(ctx)

	var result APITimeEntryResult
	err = tx.QueryRow(ctx, `
		UPDATE time_entries
		SET clock_out = NOW(), updated_at = NOW()
		WHERE company_id = $1 AND user_id = $2 AND clock_out IS NULL
		RETURNING id, user_id, job_id, clock_in, clock_out, notes, latitude, longitude`,
		actor.CompanyID, actor.UserID,
	).Scan(&result.ID, &result.UserID, &result.JobID, &result.ClockIn, &result.ClockOut, &result.Notes, &result.Latitude, &result.Longitude)
	if errors.Is(err, pgx.ErrNoRows) {
		return APITimeEntryResult{}, ErrTimeEntryNotActive
	}
	if err != nil {
		return APITimeEntryResult{}, fmt.Errorf("clock out: %w", err)
	}

	if err = insertTimeEntryActivities(ctx, tx, actor, result, "clocked_out"); err != nil {
		return APITimeEntryResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return APITimeEntryResult{}, fmt.Errorf("commit clock out: %w", err)
	}
	return result, nil
}

func (s *APIMutationService) begin(ctx context.Context) (pgx.Tx, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("API mutation database is required")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin API mutation: %w", err)
	}
	return tx, nil
}

func validateMutation(actor MutationActor, jobID int64) error {
	if actor.CompanyID <= 0 || actor.UserID <= 0 {
		return fmt.Errorf("invalid mutation actor")
	}
	if jobID <= 0 {
		return fmt.Errorf("invalid mutation job")
	}
	return nil
}

func authorizeJobMutation(ctx context.Context, tx pgx.Tx, actor MutationActor, jobID int64) error {
	var statusID *int64
	err := tx.QueryRow(ctx, `
		SELECT status_id
		FROM jobs
		WHERE company_id = $1 AND id = $2 AND deleted_at IS NULL
		FOR UPDATE`, actor.CompanyID, jobID).Scan(&statusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrMutationJobNotFound
	}
	if err != nil {
		return fmt.Errorf("lock mutation job: %w", err)
	}

	switch actor.Role {
	case "admin", "dispatcher":
	case "tech", "technician":
		var assigned bool
		err = tx.QueryRow(ctx, `
			SELECT true
			FROM job_assignments
			WHERE job_id = $1 AND user_id = $2
			FOR SHARE`, jobID, actor.UserID).Scan(&assigned)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMutationForbidden
		}
		if err != nil {
			return fmt.Errorf("lock mutation assignment: %w", err)
		}
	default:
		return ErrMutationForbidden
	}

	if statusID == nil {
		return nil
	}
	var category string
	err = tx.QueryRow(ctx, `SELECT category_key FROM statuses WHERE id = $1 FOR SHARE`, *statusID).Scan(&category)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock mutation job status: %w", err)
	}
	if category == "job:completed" || category == "job:canceled" {
		return ErrMutationJobClosed
	}
	return nil
}

func insertTimeEntryActivities(ctx context.Context, tx pgx.Tx, actor MutationActor, entry APITimeEntryResult, action string) error {
	metadata := map[string]any{
		"actor_name":    actor.Name,
		"time_entry_id": entry.ID,
		"clock_in":      entry.ClockIn.Format(time.RFC3339),
	}
	if err := insertMutationActivity(ctx, tx, actor, action, "time_entry", entry.ID, metadata); err != nil {
		return err
	}
	if entry.JobID != nil {
		if err := insertMutationActivity(ctx, tx, actor, action, "job", *entry.JobID, metadata); err != nil {
			return err
		}
	}
	return nil
}

func insertMutationActivity(ctx context.Context, tx pgx.Tx, actor MutationActor, action, objectType string, objectID int64, metadata map[string]any) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode mutation activity: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO activity_logs(company_id, actor_id, action, object_type, object_id, metadata, created_at)
		VALUES($1, $2, $3, $4, $5, $6, NOW())`,
		actor.CompanyID, actor.UserID, action, objectType, objectID, encoded,
	)
	if err != nil {
		return fmt.Errorf("record mutation activity: %w", err)
	}
	return nil
}

func isAPIMutationActiveEntryConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == activeTimeEntryIndex
}
