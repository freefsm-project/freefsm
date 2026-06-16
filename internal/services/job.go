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
	CustomerID        int64
	ProjectID         int64
	LocationID        int64
	CustomerContactID int64
	JobType           string
	Subtitle          string
	StatusID          int64
	BillingType       string
	StartTime         time.Time
	EndTime           time.Time
	DueDate           time.Time
	ArrivalStart      time.Time
	ArrivalEnd        time.Time
	Notes             string
	TechNotes         string
	LineItems         []LineItem
	Visits            []JobVisit
	Assignments       []JobAssignment
	Subtasks          []JobSubtask
	CustomFields      string
}

type JobUpdateParams struct {
	CustomerID        *int64
	ProjectID         *int64
	LocationID        *int64
	CustomerContactID *int64
	JobType           *string
	Subtitle          *string
	StatusID          *int64
	BillingType       *string
	StartTime         *time.Time
	EndTime           *time.Time
	DueDate           *time.Time
	ArrivalStart      *time.Time
	ArrivalEnd        *time.Time
	Notes             *string
	TechNotes         *string
	LineItems         *[]LineItem
	Visits            *[]JobVisit
	Assignments       *[]JobAssignment
	Subtasks          *[]JobSubtask
	CustomFields      *string
}

func (s *JobService) ListAll(ctx context.Context) ([]*ent.Job, error) {
	return s.client.Job.Query().Order(ent.Desc(job.FieldStartTime)).All(ctx)
}

func (s *JobService) ListByDateRange(ctx context.Context, start, end time.Time) ([]*ent.Job, error) {
	return s.client.Job.Query().
		Where(job.StartTimeNotNil(), job.StartTimeGTE(start), job.StartTimeLTE(end)).
		Order(ent.Asc(job.FieldStartTime)).
		All(ctx)
}

func (s *JobService) ListByProject(ctx context.Context, projectID int64) ([]*ent.Job, error) {
	return s.client.Job.Query().
		Where(job.ProjectIDEQ(projectID)).
		Order(ent.Desc(job.FieldCreatedAt)).
		All(ctx)
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
		SetBillingType(params.BillingType).
		SetNotes(params.Notes).
		SetTechNotes(params.TechNotes).
		SetLineItems(SerializeLineItems(params.LineItems)).
		SetVisits(SerializeVisits(params.Visits)).
		SetAssignments(SerializeAssignments(params.Assignments)).
		SetSubtasks(SerializeSubtasks(params.Subtasks)).
		SetCustomFields(params.CustomFields)

	if params.StatusID > 0 {
		b.SetStatusID(params.StatusID)
	}

	if params.ProjectID > 0 {
		b.SetProjectID(params.ProjectID)
	}
	if params.LocationID > 0 {
		b.SetLocationID(params.LocationID)
	}
	if params.CustomerContactID > 0 {
		b.SetCustomerContactID(params.CustomerContactID)
	}
	if !params.StartTime.IsZero() {
		b.SetStartTime(params.StartTime)
	}
	if !params.EndTime.IsZero() {
		b.SetEndTime(params.EndTime)
	}
	if !params.DueDate.IsZero() {
		b.SetDueDate(params.DueDate)
	}
	if !params.ArrivalStart.IsZero() {
		b.SetArrivalWindowStart(params.ArrivalStart)
	}
	if !params.ArrivalEnd.IsZero() {
		b.SetArrivalWindowEnd(params.ArrivalEnd)
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
	if params.ProjectID != nil {
		u.SetProjectID(*params.ProjectID)
	}
	if params.LocationID != nil {
		u.SetLocationID(*params.LocationID)
	}
	if params.CustomerContactID != nil {
		u.SetCustomerContactID(*params.CustomerContactID)
	}
	if params.ArrivalStart != nil {
		u.SetArrivalWindowStart(*params.ArrivalStart)
	}
	if params.ArrivalEnd != nil {
		u.SetArrivalWindowEnd(*params.ArrivalEnd)
	}
	if params.Notes != nil {
		u.SetNotes(*params.Notes)
	}
	if params.TechNotes != nil {
		u.SetTechNotes(*params.TechNotes)
	}
	if params.LineItems != nil {
		u.SetLineItems(SerializeLineItems(*params.LineItems))
	}
	if params.CustomFields != nil {
		u.SetCustomFields(*params.CustomFields)
	}

	if params.Visits != nil {
		u.SetVisits(SerializeVisits(*params.Visits))
	}
	if params.Assignments != nil {
		u.SetAssignments(SerializeAssignments(*params.Assignments))
	}
	if params.Subtasks != nil {
		u.SetSubtasks(SerializeSubtasks(*params.Subtasks))
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

func (s *JobService) LineItems(j *ent.Job) []LineItem {
	items, _ := ParseLineItems(j.LineItems)
	if items == nil {
		return []LineItem{}
	}
	return items
}

var JobBillingTypes = []string{"flat_rate", "hourly", "t_and_m"}
