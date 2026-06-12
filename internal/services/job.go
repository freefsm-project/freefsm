package services

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/job"
)

type JobService struct {
	client *ent.Client
}

func NewJobService(client *ent.Client) *JobService {
	return &JobService{client: client}
}

type JobCreateParams struct {
	CustomerID  int64
	JobType     string
	Subtitle    string
	StatusID    int64
	BillingType string
	StartTime   time.Time
	EndTime     time.Time
	DueDate     time.Time
	Notes       string
	TechNotes   string
}

type JobUpdateParams struct {
	CustomerID  *int64
	JobType     *string
	Subtitle    *string
	StatusID    *int64
	BillingType *string
	StartTime   *time.Time
	EndTime     *time.Time
	DueDate     *time.Time
	Notes       *string
	TechNotes   *string
}

func (s *JobService) ListAll(ctx context.Context) ([]*ent.Job, error) {
	return s.client.Job.Query().Order(ent.Desc(job.FieldStartTime)).All(ctx)
}

func (s *JobService) List(ctx context.Context, search string, statusID int64, page, perPage int) ([]*ent.Job, int, error) {
	q := s.client.Job.Query()

	if search != "" {
		q = q.Where(
			job.Or(
				job.JobTypeContainsFold(search),
				job.SubtitleContainsFold(search),
			),
		)
	}

	if statusID > 0 {
		q = q.Where(job.StatusIDEQ(statusID))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", err)
	}

	offset := (page - 1) * perPage
	jobs, err := q.
		Order(ent.Desc(job.FieldStartTime)).
		Order(ent.Desc(job.FieldCreatedAt)).
		Limit(perPage).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs: %w", err)
	}

	return jobs, total, nil
}

func (s *JobService) GetByID(ctx context.Context, id int64) (*ent.Job, error) {
	j, err := s.client.Job.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get job %d: %w", id, err)
	}
	return j, nil
}

func (s *JobService) Create(ctx context.Context, params JobCreateParams) (*ent.Job, error) {
	b := s.client.Job.Create().
		SetCustomerID(params.CustomerID).
		SetJobType(params.JobType).
		SetSubtitle(params.Subtitle).
		SetStatusID(params.StatusID).
		SetBillingType(params.BillingType).
		SetNotes(params.Notes).
		SetTechNotes(params.TechNotes)

	if !params.StartTime.IsZero() {
		b.SetStartTime(params.StartTime)
	}
	if !params.EndTime.IsZero() {
		b.SetEndTime(params.EndTime)
	}
	if !params.DueDate.IsZero() {
		b.SetDueDate(params.DueDate)
	}

	j, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	return j, nil
}

func (s *JobService) Update(ctx context.Context, id int64, params JobUpdateParams) (*ent.Job, error) {
	u := s.client.Job.UpdateOneID(id)

	if params.CustomerID != nil {
		u.SetCustomerID(*params.CustomerID)
	}
	if params.JobType != nil {
		u.SetJobType(*params.JobType)
	}
	if params.Subtitle != nil {
		u.SetSubtitle(*params.Subtitle)
	}
	if params.StatusID != nil {
		u.SetStatusID(*params.StatusID)
	}
	if params.BillingType != nil {
		u.SetBillingType(*params.BillingType)
	}
	if params.StartTime != nil {
		u.SetStartTime(*params.StartTime)
	}
	if params.EndTime != nil {
		u.SetEndTime(*params.EndTime)
	}
	if params.DueDate != nil {
		u.SetDueDate(*params.DueDate)
	}
	if params.Notes != nil {
		u.SetNotes(*params.Notes)
	}
	if params.TechNotes != nil {
		u.SetTechNotes(*params.TechNotes)
	}

	j, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update job %d: %w", id, err)
	}
	return j, nil
}

func (s *JobService) Delete(ctx context.Context, id int64) error {
	if err := s.client.Job.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete job %d: %w", id, err)
	}
	return nil
}

func JobPaginationTotalPages(total, perPage int) int {
	return int(math.Ceil(float64(total) / float64(perPage)))
}

var JobBillingTypes = []string{"flat_rate", "hourly", "t_and_m"}
