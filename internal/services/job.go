package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/customer"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/jobassignment"
	"github.com/freefsm-project/freefsm/internal/ent/user"
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

func (s *JobService) ListUnscheduled(ctx context.Context) ([]*ent.Job, error) {
	return s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.StartTimeIsNil()).
		Order(ent.Desc(job.FieldCreatedAt)).
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

func (s *JobService) ListAssignedUnscheduled(ctx context.Context, userID int64) ([]*ent.Job, error) {
	jobIDs, err := s.assignedJobIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(jobIDs) == 0 {
		return nil, nil
	}
	return s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.IDIn(jobIDs...), job.StartTimeIsNil()).
		Order(ent.Desc(job.FieldCreatedAt)).
		All(ctx)
}

func (s *JobService) ListByProject(ctx context.Context, projectID int64) ([]*ent.Job, error) {
	return s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.ProjectIDEQ(projectID)).
		Order(ent.Desc(job.FieldCreatedAt)).
		All(ctx)
}

func (s *JobService) ListByCustomer(ctx context.Context, customerID int64, limit int) ([]*ent.Job, error) {
	q := s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.CustomerIDEQ(customerID)).
		Order(ent.Desc(job.FieldCreatedAt))
	if limit > 0 {
		q = q.Limit(limit)
	}
	return q.All(ctx)
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

func (s *JobService) ListForCustomer(ctx context.Context, customerID int64, search string, statusID int64, page, perPage int) ([]*ent.Job, int, error) {
	q := s.client.Job.Query().Where(job.DeletedAtIsNil(), job.CustomerIDEQ(customerID))

	if search != "" {
		q = q.Where(job.Or(job.JobTypeContainsFold(search), job.SubtitleContainsFold(search)))
	}
	if statusID > 0 {
		q = q.Where(job.StatusIDEQ(statusID))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count customer jobs: %w", err)
	}
	jobs, err := q.Order(ent.Desc(job.FieldStartTime)).Order(ent.Desc(job.FieldCreatedAt)).Limit(perPage).Offset(PaginationOffset(page, perPage)).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list customer jobs: %w", err)
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

func (s *JobService) ListAssignedAll(ctx context.Context, userID int64) ([]*ent.Job, error) {
	jobIDs, err := s.assignedJobIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(jobIDs) == 0 {
		return nil, nil
	}
	return s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.IDIn(jobIDs...)).
		Order(ent.Desc(job.FieldStartTime)).
		Order(ent.Desc(job.FieldCreatedAt)).
		All(ctx)
}

func (s *JobService) GetByID(ctx context.Context, id int64) (*ent.Job, error) {
	j, err := s.client.Job.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get job %d: %w", id, err)
	}
	return j, nil
}

func (s *JobService) GetByIDForCompany(ctx context.Context, companyID, id int64) (*ent.Job, error) {
	if companyID <= 0 {
		return nil, fmt.Errorf("get job for company: company is required")
	}
	j, err := s.client.Job.Query().Where(job.IDEQ(id), job.CompanyIDEQ(companyID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("get job %d for company: %w", id, err)
	}
	return j, nil
}

func (s *JobService) activeCustomerForCompany(ctx context.Context, companyID, customerID int64) (*ent.Customer, error) {
	if customerID <= 0 {
		return nil, invalidJobInput(JobInputReasonRequired, JobInputRelationCustomer)
	}
	jobCustomer, err := s.client.Customer.Query().Where(customer.IDEQ(customerID), customer.CompanyIDEQ(companyID), customer.DeletedAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, invalidJobInput(JobInputReasonOwnershipMismatch, JobInputRelationCustomer)
	}
	if err != nil {
		return nil, fmt.Errorf("get active job customer for company: %w", err)
	}
	return jobCustomer, nil
}

func (s *JobService) Create(ctx context.Context, companyID int64, params JobCreateParams) (*ent.Job, error) {
	if companyID <= 0 {
		return nil, fmt.Errorf("create job: company is required")
	}
	lineItems, err := EncodeLineItems(params.LineItems)
	if err != nil {
		return nil, fmt.Errorf("encode job line items: %w", err)
	}
	jobCustomer, err := s.activeCustomerForCompany(ctx, companyID, params.CustomerID)
	if err != nil {
		return nil, err
	}
	if err := validateJobCustomerLinks(ctx, s.client, companyID, params.CustomerID, params.ProjectID, params.LocationID, params.CustomerContactID, params.AssetID); err != nil {
		return nil, err
	}
	statusID, err := creationStatus(ctx, s.client, params.StatusID, jobCustomer.CompanyID, "job", "job:new")
	if err != nil {
		return nil, err
	}
	assignments, err := s.hydrateAssignments(ctx, companyID, params.Assignments)
	if err != nil {
		return nil, err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create job transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txClient := tx.Client()
	b := txClient.Job.Create().
		SetCompanyID(companyID).
		SetCustomerID(params.CustomerID).
		SetJobType(params.JobType).
		SetSubtitle(params.Subtitle).
		SetBillingType(params.BillingType).
		SetNotes(params.Notes).
		SetTechNotes(params.TechNotes).
		SetLineItems(lineItems).
		SetVisits(SerializeVisits(params.Visits)).
		SetAssignments(SerializeAssignments(params.Assignments)).
		SetSubtasks(SerializeSubtasks(params.Subtasks)).
		SetCustomFields(params.CustomFields)

	b.SetStatusID(statusID)

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

	b.SetAssignments(SerializeAssignments(s.assignmentsForStorage(params.Assignments, assignments)))

	j, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	if err := s.replaceAssignments(ctx, txClient, j.ID, assignments); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create job transaction: %w", err)
	}
	return j.Unwrap(), nil
}

func (s *JobService) CreateNextOccurrence(ctx context.Context, companyID, sourceID int64, nextStart time.Time) (*ent.Job, error) {
	if companyID <= 0 {
		return nil, fmt.Errorf("create next job occurrence: company is required")
	}
	if nextStart.IsZero() {
		return nil, fmt.Errorf("next occurrence start time is required")
	}
	source, err := s.GetByIDForCompany(ctx, companyID, sourceID)
	if ent.IsNotFound(err) {
		return nil, invalidJobInput(JobInputReasonOwnershipMismatch, JobInputRelationJob)
	}
	if err != nil {
		return nil, err
	}
	var delta time.Duration
	if source.StartTime != nil && !source.StartTime.IsZero() {
		delta = nextStart.Sub(*source.StartTime)
	}

	assignments, err := s.Assignments(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	if len(assignments) == 0 {
		assignments = ParseAssignments(source.Assignments)
	}
	lineItems, err := DecodeLineItems(source.LineItems)
	if err != nil {
		return nil, fmt.Errorf("parse source job line items: %w", err)
	}

	params := JobCreateParams{
		CustomerID:        source.CustomerID,
		ProjectID:         int64Value(source.ProjectID),
		LocationID:        int64Value(source.LocationID),
		CustomerContactID: int64Value(source.CustomerContactID),
		AssetID:           int64Value(source.AssetID),
		JobType:           source.JobType,
		Subtitle:          source.Subtitle,
		BillingType:       source.BillingType,
		StartTime:         nextStart,
		EndTime:           nextStart.Add(time.Hour),
		DueDate:           shiftedTime(source.DueDate, delta),
		ArrivalStart:      shiftedTime(source.ArrivalWindowStart, delta),
		ArrivalEnd:        shiftedTime(source.ArrivalWindowEnd, delta),
		Notes:             source.Notes,
		TechNotes:         source.TechNotes,
		LineItems:         lineItems,
		Visits:            shiftVisits(ParseVisits(source.Visits), source.StartTime, nextStart),
		Assignments:       assignments,
		Subtasks:          resetSubtasks(ParseSubtasks(source.Subtasks)),
		CustomFields:      source.CustomFields,
	}
	if source.StartTime == nil || source.StartTime.IsZero() {
		params.DueDate = time.Time{}
		params.ArrivalStart = time.Time{}
		params.ArrivalEnd = time.Time{}
	}
	return s.Create(ctx, companyID, params)
}

func shiftedTime(t *time.Time, delta time.Duration) time.Time {
	if t == nil || t.IsZero() {
		return time.Time{}
	}
	return t.Add(delta)
}

func resetSubtasks(subtasks []JobSubtask) []JobSubtask {
	for i := range subtasks {
		subtasks[i].Completed = false
	}
	return subtasks
}

func shiftVisits(visits []JobVisit, sourceStart *time.Time, nextStart time.Time) []JobVisit {
	if len(visits) == 0 || sourceStart == nil || sourceStart.IsZero() || nextStart.IsZero() {
		return visits
	}
	dateDelta := daysBetween(sourceStart.In(nextStart.Location()), nextStart)
	if dateDelta == 0 {
		return visits
	}
	for i := range visits {
		if visits[i].Date == "" {
			continue
		}
		visitDate, err := time.Parse("2006-01-02", visits[i].Date)
		if err != nil {
			continue
		}
		visits[i].Date = visitDate.AddDate(0, 0, dateDelta).Format("2006-01-02")
	}
	return visits
}

func daysBetween(from, to time.Time) int {
	fromDate := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	toDate := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return int(toDate.Sub(fromDate).Hours() / 24)
}

func (s *JobService) Update(ctx context.Context, companyID, id int64, params JobUpdateParams) (*ent.Job, error) {
	if companyID <= 0 {
		return nil, fmt.Errorf("update job: company is required")
	}
	current, err := s.GetByIDForCompany(ctx, companyID, id)
	if ent.IsNotFound(err) {
		return nil, invalidJobInput(JobInputReasonOwnershipMismatch, JobInputRelationJob)
	}
	if err != nil {
		return nil, err
	}
	var encodedLineItems string
	if params.LineItems != nil {
		var err error
		encodedLineItems, err = EncodeLineItems(*params.LineItems)
		if err != nil {
			return nil, fmt.Errorf("encode job line items: %w", err)
		}
	}
	customerID := current.CustomerID
	projectID := int64Value(current.ProjectID)
	locationID := int64Value(current.LocationID)
	contactID := int64Value(current.CustomerContactID)
	assetID := int64Value(current.AssetID)
	if params.CustomerID != nil {
		customerID = *params.CustomerID
	}
	if params.ProjectID != nil {
		projectID = *params.ProjectID
	}
	if params.LocationID != nil {
		locationID = *params.LocationID
	}
	if params.CustomerContactID != nil {
		contactID = *params.CustomerContactID
	}
	if params.AssetID != nil {
		assetID = *params.AssetID
	}
	customerChanged := params.CustomerID != nil && *params.CustomerID != current.CustomerID
	projectChanged := params.ProjectID != nil && *params.ProjectID != int64Value(current.ProjectID)
	locationChanged := params.LocationID != nil && *params.LocationID != int64Value(current.LocationID)
	contactChanged := params.CustomerContactID != nil && *params.CustomerContactID != int64Value(current.CustomerContactID)
	assetChanged := params.AssetID != nil && *params.AssetID != int64Value(current.AssetID)
	if customerChanged {
		if _, err := s.activeCustomerForCompany(ctx, companyID, customerID); err != nil {
			return nil, err
		}
	}
	projectToValidate, locationToValidate, contactToValidate, assetToValidate := int64(0), int64(0), int64(0), int64(0)
	if customerChanged || projectChanged {
		projectToValidate = projectID
	}
	if customerChanged || locationChanged {
		locationToValidate = locationID
	}
	if customerChanged || contactChanged {
		contactToValidate = contactID
	}
	if customerChanged || assetChanged {
		assetToValidate = assetID
	}
	if err := validateJobCustomerLinks(ctx, s.client, companyID, customerID, projectToValidate, locationToValidate, contactToValidate, assetToValidate); err != nil {
		return nil, err
	}

	var assignments []JobAssignment
	if params.Assignments != nil {
		assignments, err = s.hydrateAssignments(ctx, companyID, *params.Assignments)
		if err != nil {
			return nil, err
		}
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update job transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txClient := tx.Client()
	u := txClient.Job.UpdateOneID(id)

	if params.CustomerID != nil {
		u.SetCustomerID(*params.CustomerID)
	}
	if params.JobType != nil {
		u.SetJobType(*params.JobType)
	}
	if params.Subtitle != nil {
		u.SetSubtitle(*params.Subtitle)
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
		if *params.ProjectID > 0 {
			u.SetProjectID(*params.ProjectID)
		} else {
			u.ClearProjectID()
		}
	}
	if params.LocationID != nil {
		if *params.LocationID > 0 {
			u.SetLocationID(*params.LocationID)
		} else {
			u.ClearLocationID()
		}
	}
	if params.CustomerContactID != nil {
		if *params.CustomerContactID > 0 {
			u.SetCustomerContactID(*params.CustomerContactID)
		} else {
			u.ClearCustomerContactID()
		}
	}
	if params.AssetID != nil {
		if *params.AssetID > 0 {
			u.SetAssetID(*params.AssetID)
		} else {
			u.ClearAssetID()
		}
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
		u.SetLineItems(encodedLineItems)
	}
	if params.CustomFields != nil {
		u.SetCustomFields(*params.CustomFields)
	}

	if params.Visits != nil {
		u.SetVisits(SerializeVisits(*params.Visits))
	}
	if params.Assignments != nil {
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
		if err := s.replaceAssignments(ctx, txClient, id, assignments); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update job transaction: %w", err)
	}
	return j.Unwrap(), nil
}

func (s *JobService) Move(ctx context.Context, id, userID int64, startTime, endTime time.Time) (*ent.Job, error) {
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.CompanyID == nil {
		return nil, fmt.Errorf("move job: company is required")
	}
	assignments, err := s.hydrateAssignments(ctx, *current.CompanyID, []JobAssignment{{UserID: userID}})
	if err != nil {
		return nil, err
	}

	u := s.client.Job.UpdateOneID(id).SetStartTime(startTime).SetAssignments(SerializeAssignments(assignments))
	if endTime.IsZero() {
		u.ClearEndTime()
	} else {
		u.SetEndTime(endTime)
	}

	j, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("move job %d: %w", id, err)
	}
	if err := s.replaceAssignments(ctx, s.client, id, assignments); err != nil {
		return nil, err
	}
	return j, nil
}

func (s *JobService) MoveTime(ctx context.Context, id int64, startTime, endTime time.Time) (*ent.Job, error) {
	u := s.client.Job.UpdateOneID(id).SetStartTime(startTime)
	if endTime.IsZero() {
		u.ClearEndTime()
	} else {
		u.SetEndTime(endTime)
	}
	j, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("move job time %d: %w", id, err)
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

func (s *JobService) replaceAssignments(ctx context.Context, client *ent.Client, jobID int64, assignments []JobAssignment) error {
	if _, err := client.JobAssignment.Delete().Where(jobassignment.JobIDEQ(jobID)).Exec(ctx); err != nil {
		return fmt.Errorf("delete job assignments: %w", err)
	}
	for _, assignment := range assignments {
		if assignment.UserID <= 0 {
			continue
		}
		if _, err := client.JobAssignment.Create().SetJobID(jobID).SetUserID(assignment.UserID).SetRole(strings.TrimSpace(assignment.Role)).Save(ctx); err != nil {
			return fmt.Errorf("create job assignment: %w", err)
		}
	}
	return nil
}

func (s *JobService) hydrateAssignments(ctx context.Context, companyID int64, assignments []JobAssignment) ([]JobAssignment, error) {
	if companyID <= 0 {
		return nil, fmt.Errorf("hydrate job assignments: company is required")
	}
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
	users, err := s.client.User.Query().Where(user.IDIn(userIDs...), user.CompanyIDEQ(companyID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list assignment users: %w", err)
	}
	names := make(map[int64]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Name
	}
	if len(users) != len(userIDs) {
		return nil, invalidJobInput(JobInputReasonOwnershipMismatch, JobInputRelationAssignment)
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
	items, _ := DecodeLineItems(j.LineItems)
	if items == nil {
		return []LineItem{}
	}
	return items
}

var JobBillingTypes = []string{"flat_rate", "hourly", "t_and_m"}
