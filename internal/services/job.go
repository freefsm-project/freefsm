package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/job"
	"github.com/MartialM1nd/freefsm/internal/ent/jobassignment"
	"github.com/MartialM1nd/freefsm/internal/ent/user"
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
	AssetID           int64
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
	AssetID           *int64
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
	return s.client.Job.Query().Where(job.DeletedAtIsNil()).Order(ent.Desc(job.FieldStartTime)).All(ctx)
}

func (s *JobService) ListByDateRange(ctx context.Context, start, end time.Time) ([]*ent.Job, error) {
	return s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.StartTimeNotNil(), job.StartTimeGTE(start), job.StartTimeLTE(end)).
		Order(ent.Asc(job.FieldStartTime)).
		All(ctx)
}

func (s *JobService) ListAssignedByDateRange(ctx context.Context, userID int64, start, end time.Time) ([]*ent.Job, error) {
	jobIDs, err := s.assignedJobIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(jobIDs) == 0 {
		return nil, nil
	}
	return s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.IDIn(jobIDs...), job.StartTimeNotNil(), job.StartTimeGTE(start), job.StartTimeLTE(end)).
		Order(ent.Asc(job.FieldStartTime)).
		All(ctx)
}

func (s *JobService) ListByProject(ctx context.Context, projectID int64) ([]*ent.Job, error) {
	return s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.ProjectIDEQ(projectID)).
		Order(ent.Desc(job.FieldCreatedAt)).
		All(ctx)
}

func (s *JobService) List(ctx context.Context, search string, statusID int64, page, perPage int) ([]*ent.Job, int, error) {
	q := s.client.Job.Query().Where(job.DeletedAtIsNil())

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

	offset := PaginationOffset(page, perPage)
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

func (s *JobService) ListAssigned(ctx context.Context, userID int64, search string, statusID int64, page, perPage int) ([]*ent.Job, int, error) {
	jobIDs, err := s.assignedJobIDs(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	if len(jobIDs) == 0 {
		return nil, 0, nil
	}
	q := s.client.Job.Query().Where(job.DeletedAtIsNil(), job.IDIn(jobIDs...))

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
		return nil, 0, fmt.Errorf("count assigned jobs: %w", err)
	}

	jobs, err := q.
		Order(ent.Desc(job.FieldStartTime)).
		Order(ent.Desc(job.FieldCreatedAt)).
		Limit(perPage).
		Offset(PaginationOffset(page, perPage)).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list assigned jobs: %w", err)
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
	if params.AssetID > 0 {
		b.SetAssetID(params.AssetID)
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

	assignments, err := s.hydrateAssignments(ctx, params.Assignments)
	if err != nil {
		return nil, err
	}
	b.SetAssignments(SerializeAssignments(s.assignmentsForStorage(params.Assignments, assignments)))

	j, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	if err := s.replaceAssignments(ctx, j.ID, assignments); err != nil {
		return nil, err
	}
	return j, nil
}

func (s *JobService) Update(ctx context.Context, id int64, params JobUpdateParams) (*ent.Job, error) {
	u := s.client.Job.UpdateOneID(id)
	var assignments []JobAssignment

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
	if params.AssetID != nil {
		u.SetAssetID(*params.AssetID)
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
		var err error
		assignments, err = s.hydrateAssignments(ctx, *params.Assignments)
		if err != nil {
			return nil, err
		}
		u.SetAssignments(SerializeAssignments(s.assignmentsForStorage(*params.Assignments, assignments)))
	}
	if params.Subtasks != nil {
		u.SetSubtasks(SerializeSubtasks(*params.Subtasks))
	}

	j, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update job %d: %w", id, err)
	}
	if params.Assignments != nil {
		if err := s.replaceAssignments(ctx, id, assignments); err != nil {
			return nil, err
		}
	}
	return j, nil
}

func (s *JobService) Assignments(ctx context.Context, jobID int64) ([]JobAssignment, error) {
	rows, err := s.client.JobAssignment.Query().Where(jobassignment.JobIDEQ(jobID)).Order(ent.Asc(jobassignment.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list job assignments: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	userIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		userIDs = append(userIDs, row.UserID)
	}
	users, err := s.client.User.Query().Where(user.IDIn(userIDs...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list assigned users: %w", err)
	}
	names := make(map[int64]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Name
	}
	assignments := make([]JobAssignment, 0, len(rows))
	for _, row := range rows {
		assignments = append(assignments, JobAssignment{UserID: row.UserID, Name: names[row.UserID], Role: row.Role})
	}
	return assignments, nil
}

func (s *JobService) replaceAssignments(ctx context.Context, jobID int64, assignments []JobAssignment) error {
	if _, err := s.client.JobAssignment.Delete().Where(jobassignment.JobIDEQ(jobID)).Exec(ctx); err != nil {
		return fmt.Errorf("delete job assignments: %w", err)
	}
	for _, assignment := range assignments {
		if assignment.UserID <= 0 {
			continue
		}
		if _, err := s.client.JobAssignment.Create().SetJobID(jobID).SetUserID(assignment.UserID).SetRole(strings.TrimSpace(assignment.Role)).Save(ctx); err != nil {
			return fmt.Errorf("create job assignment: %w", err)
		}
	}
	return nil
}

func (s *JobService) hydrateAssignments(ctx context.Context, assignments []JobAssignment) ([]JobAssignment, error) {
	seen := map[int64]bool{}
	userIDs := make([]int64, 0, len(assignments))
	for _, assignment := range assignments {
		if assignment.UserID <= 0 || seen[assignment.UserID] {
			continue
		}
		seen[assignment.UserID] = true
		userIDs = append(userIDs, assignment.UserID)
	}
	if len(userIDs) == 0 {
		return nil, nil
	}
	users, err := s.client.User.Query().Where(user.IDIn(userIDs...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list assignment users: %w", err)
	}
	names := make(map[int64]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Name
	}
	hydrated := make([]JobAssignment, 0, len(userIDs))
	seen = map[int64]bool{}
	for _, assignment := range assignments {
		if assignment.UserID <= 0 || names[assignment.UserID] == "" || seen[assignment.UserID] {
			continue
		}
		seen[assignment.UserID] = true
		hydrated = append(hydrated, JobAssignment{UserID: assignment.UserID, Name: names[assignment.UserID], Role: strings.TrimSpace(assignment.Role)})
	}
	return hydrated, nil
}

func (s *JobService) assignmentsForStorage(submitted []JobAssignment, hydrated []JobAssignment) []JobAssignment {
	stored := make([]JobAssignment, 0, len(submitted))
	stored = append(stored, hydrated...)
	for _, assignment := range submitted {
		if assignment.UserID > 0 || strings.TrimSpace(assignment.Name) == "" {
			continue
		}
		stored = append(stored, JobAssignment{Name: strings.TrimSpace(assignment.Name), Role: strings.TrimSpace(assignment.Role)})
	}
	return stored
}

func (s *JobService) assignedJobIDs(ctx context.Context, userID int64) ([]int64, error) {
	assignments, err := s.client.JobAssignment.Query().Where(jobassignment.UserIDEQ(userID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list assigned job IDs: %w", err)
	}
	ids := make([]int64, 0, len(assignments))
	for _, assignment := range assignments {
		ids = append(ids, assignment.JobID)
	}
	if len(ids) == 0 {
		return ids, nil
	}
	activeIDs, err := s.client.Job.Query().Where(job.IDIn(ids...), job.DeletedAtIsNil()).IDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active assigned job IDs: %w", err)
	}
	return activeIDs, nil
}

func (s *JobService) Delete(ctx context.Context, id int64) error {
	if err := s.client.Job.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete job %d: %w", id, err)
	}
	return nil
}

func (s *JobService) Archive(ctx context.Context, id int64) error {
	now := time.Now()
	_, err := s.client.Job.UpdateOneID(id).SetDeletedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("archive job %d: %w", id, err)
	}
	return nil
}

func (s *JobService) Restore(ctx context.Context, id int64) error {
	_, err := s.client.Job.UpdateOneID(id).ClearDeletedAt().Save(ctx)
	if err != nil {
		return fmt.Errorf("restore job %d: %w", id, err)
	}
	return nil
}

func JobPaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}

func (s *JobService) LineItems(j *ent.Job) []LineItem {
	items, _ := ParseLineItems(j.LineItems)
	if items == nil {
		return []LineItem{}
	}
	return items
}

var JobBillingTypes = []string{"flat_rate", "hourly", "t_and_m"}
