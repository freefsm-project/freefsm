package services

import (
	"context"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/asset"
	"github.com/freefsm-project/freefsm/internal/ent/customer"
	"github.com/freefsm-project/freefsm/internal/ent/estimate"
	"github.com/freefsm-project/freefsm/internal/ent/invoice"
	"github.com/freefsm-project/freefsm/internal/ent/item"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/jobassignment"
	"github.com/freefsm-project/freefsm/internal/ent/project"
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
		return action == "read" || s.canMutateObject(ctx, objectType, objectID)
	}
	if role == "dispatcher" {
		switch objectType {
		case "customer", "job", "project", "estimate", "invoice", "asset", "item", "time_entry":
			return action == "read" || s.canMutateObject(ctx, objectType, objectID)
		default:
			return false
		}
	}
	if role != "tech" {
		return false
	}
	switch action {
	case "read", "create", "attach_file":
	case "update", "delete":
		return false
	default:
		return false
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
	if err != nil || !exists {
		return false
	}
	active, err := s.client.Job.Query().Where(job.IDEQ(jobID), job.DeletedAtIsNil()).Exist(ctx)
	return err == nil && active
}

func (s *PolicyService) canAccessCustomer(ctx context.Context, customerID, userID int64) bool {
	direct, err := s.client.Customer.Query().Where(customer.IDEQ(customerID), customer.DeletedAtIsNil(), customer.AssignedToEQ(userID)).Exist(ctx)
	if err == nil && direct {
		return true
	}
	active, err := s.client.Customer.Query().Where(customer.IDEQ(customerID), customer.DeletedAtIsNil()).Exist(ctx)
	if err != nil || !active {
		return false
	}
	jobIDs, err := s.assignedJobIDs(ctx, userID)
	if err != nil || len(jobIDs) == 0 {
		return false
	}
	exists, err := s.client.Job.Query().Where(job.CustomerIDEQ(customerID), job.IDIn(jobIDs...)).Exist(ctx)
	return err == nil && exists
}

func (s *PolicyService) canAccessProject(ctx context.Context, projectID, userID int64) bool {
	p, err := s.client.Project.Query().Where(project.IDEQ(projectID), project.DeletedAtIsNil()).Only(ctx)
	if err == nil && s.isCustomerAssignedToUser(ctx, p.CustomerID, userID) {
		return true
	}
	if err != nil {
		return false
	}
	jobIDs, err := s.assignedJobIDs(ctx, userID)
	if err != nil || len(jobIDs) == 0 {
		return false
	}
	exists, err := s.client.Job.Query().Where(job.ProjectIDEQ(projectID), job.IDIn(jobIDs...)).Exist(ctx)
	return err == nil && exists
}

func (s *PolicyService) canAccessAsset(ctx context.Context, assetID, userID int64) bool {
	a, err := s.client.Asset.Query().Where(asset.IDEQ(assetID), asset.DeletedAtIsNil()).Only(ctx)
	if err == nil && s.isCustomerAssignedToUser(ctx, a.CustomerID, userID) {
		return true
	}
	if err != nil {
		return false
	}
	jobIDs, err := s.assignedJobIDs(ctx, userID)
	if err != nil || len(jobIDs) == 0 {
		return false
	}
	exists, err := s.client.Job.Query().Where(job.AssetIDEQ(assetID), job.IDIn(jobIDs...)).Exist(ctx)
	return err == nil && exists
}

func (s *PolicyService) isCustomerAssignedToUser(ctx context.Context, customerID, userID int64) bool {
	direct, err := s.client.Customer.Query().Where(customer.IDEQ(customerID), customer.DeletedAtIsNil(), customer.AssignedToEQ(userID)).Exist(ctx)
	return err == nil && direct
}

func (s *PolicyService) assignedJobIDs(ctx context.Context, userID int64) ([]int64, error) {
	return NewJobService(s.client).assignedJobIDs(ctx, userID)
}

func (s *PolicyService) canMutateObject(ctx context.Context, objectType string, objectID int64) bool {
	if s.client == nil {
		return true
	}
	switch objectType {
	case "customer":
		exists, err := s.client.Customer.Query().Where(customer.IDEQ(objectID), customer.DeletedAtIsNil()).Exist(ctx)
		return err == nil && exists
	case "job":
		exists, err := s.client.Job.Query().Where(job.IDEQ(objectID), job.DeletedAtIsNil()).Exist(ctx)
		return err == nil && exists
	case "project":
		exists, err := s.client.Project.Query().Where(project.IDEQ(objectID), project.DeletedAtIsNil()).Exist(ctx)
		return err == nil && exists
	case "estimate":
		exists, err := s.client.Estimate.Query().Where(estimate.IDEQ(objectID), estimate.DeletedAtIsNil()).Exist(ctx)
		return err == nil && exists
	case "invoice":
		exists, err := s.client.Invoice.Query().Where(invoice.IDEQ(objectID), invoice.DeletedAtIsNil()).Exist(ctx)
		return err == nil && exists
	case "asset":
		exists, err := s.client.Asset.Query().Where(asset.IDEQ(objectID), asset.DeletedAtIsNil()).Exist(ctx)
		return err == nil && exists
	case "item":
		exists, err := s.client.Item.Query().Where(item.IDEQ(objectID), item.DeletedAtIsNil()).Exist(ctx)
		return err == nil && exists
	case "time_entry":
		return true
	default:
		return false
	}
}
