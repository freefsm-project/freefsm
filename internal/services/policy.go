package services

import (
	"context"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/customer"
	"github.com/MartialM1nd/freefsm/internal/ent/job"
	"github.com/MartialM1nd/freefsm/internal/ent/jobassignment"
)

type PolicyService struct {
	client *ent.Client
}

func NewPolicyService(client *ent.Client) *PolicyService {
	return &PolicyService{client: client}
}

func (s *PolicyService) CanAccessObject(ctx context.Context, userID int64, role, objectType string, objectID int64, action string) bool {
	if userID <= 0 || objectID <= 0 {
		return false
	}
	if role == "admin" {
		return true
	}
	if role == "dispatcher" {
		switch objectType {
		case "customer", "job", "project", "estimate", "invoice", "asset", "item", "time_entry":
			return true
		default:
			return false
		}
	}
	if objectType == "item" && action == "read" {
		return true
	}

	switch objectType {
	case "job":
		return s.IsUserAssignedToJob(ctx, objectID, userID)
	case "customer":
		return s.canAccessCustomer(ctx, objectID, userID)
	case "project":
		return s.canAccessProject(ctx, objectID, userID)
	case "asset":
		return s.canAccessAsset(ctx, objectID, userID)
	case "estimate":
		return false
	case "invoice":
		return false
	default:
		return false
	}
}

func (s *PolicyService) IsUserAssignedToJob(ctx context.Context, jobID, userID int64) bool {
	exists, err := s.client.JobAssignment.Query().Where(jobassignment.JobIDEQ(jobID), jobassignment.UserIDEQ(userID)).Exist(ctx)
	return err == nil && exists
}

func (s *PolicyService) canAccessCustomer(ctx context.Context, customerID, userID int64) bool {
	direct, err := s.client.Customer.Query().Where(customer.IDEQ(customerID), customer.AssignedToEQ(userID)).Exist(ctx)
	if err == nil && direct {
		return true
	}
	jobIDs, err := s.assignedJobIDs(ctx, userID)
	if err != nil || len(jobIDs) == 0 {
		return false
	}
	exists, err := s.client.Job.Query().Where(job.CustomerIDEQ(customerID), job.IDIn(jobIDs...)).Exist(ctx)
	return err == nil && exists
}

func (s *PolicyService) canAccessProject(ctx context.Context, projectID, userID int64) bool {
	p, err := s.client.Project.Get(ctx, projectID)
	if err == nil && s.isCustomerAssignedToUser(ctx, p.CustomerID, userID) {
		return true
	}
	jobIDs, err := s.assignedJobIDs(ctx, userID)
	if err != nil || len(jobIDs) == 0 {
		return false
	}
	exists, err := s.client.Job.Query().Where(job.ProjectIDEQ(projectID), job.IDIn(jobIDs...)).Exist(ctx)
	return err == nil && exists
}

func (s *PolicyService) canAccessAsset(ctx context.Context, assetID, userID int64) bool {
	a, err := s.client.Asset.Get(ctx, assetID)
	if err == nil && s.isCustomerAssignedToUser(ctx, a.CustomerID, userID) {
		return true
	}
	jobIDs, err := s.assignedJobIDs(ctx, userID)
	if err != nil || len(jobIDs) == 0 {
		return false
	}
	exists, err := s.client.Job.Query().Where(job.AssetIDEQ(assetID), job.IDIn(jobIDs...)).Exist(ctx)
	return err == nil && exists
}

func (s *PolicyService) isCustomerAssignedToUser(ctx context.Context, customerID, userID int64) bool {
	direct, err := s.client.Customer.Query().Where(customer.IDEQ(customerID), customer.AssignedToEQ(userID)).Exist(ctx)
	return err == nil && direct
}

func (s *PolicyService) assignedJobIDs(ctx context.Context, userID int64) ([]int64, error) {
	return NewJobService(s.client).assignedJobIDs(ctx, userID)
}
