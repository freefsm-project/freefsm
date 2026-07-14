package services

import (
	"context"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/asset"
	"github.com/freefsm-project/freefsm/internal/ent/customer"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/jobassignment"
	"github.com/freefsm-project/freefsm/internal/ent/project"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

type PolicyAction string

const (
	PolicyRead       PolicyAction = "read"
	PolicyCreate     PolicyAction = "create"
	PolicyUpdate     PolicyAction = "update"
	PolicyDelete     PolicyAction = "delete"
	PolicyAttachFile PolicyAction = "attach_file"
)

type PolicyService struct {
	client  *ent.Client
	objects objectref.Directory
}

func NewPolicyService(client *ent.Client, objects objectref.Directory) *PolicyService {
	return &PolicyService{client: client, objects: objects}
}

func (s *PolicyService) CanAccessObject(ctx context.Context, userID int64, role string, ref objectref.Ref, action PolicyAction) bool {
	if userID <= 0 || !ref.Valid() || s.objects == nil || !validPolicyAction(action) {
		return false
	}
	if action == PolicyAttachFile && !s.objects.Supports(ref.Type, objectref.CapFiles) {
		return false
	}
	if role == "admin" || role == "dispatcher" {
		return policyRoleAllows(role, ref.Type, action) && s.targetExists(ctx, ref, action)
	}
	if !policyRoleAllows(role, ref.Type, action) {
		return false
	}
	if !s.targetExists(ctx, ref, action) || s.client == nil {
		return false
	}
	switch ref.Type {
	case objectref.TypeJob:
		if action == PolicyUpdate {
			return false
		}
		return s.IsUserAssignedToJob(ctx, ref.ID, userID)
	case objectref.TypeCustomer:
		return s.canAccessCustomer(ctx, ref.ID, userID)
	case objectref.TypeProject:
		return s.canAccessProject(ctx, ref.ID, userID)
	case objectref.TypeAsset:
		return s.canAccessAsset(ctx, ref.ID, userID)
	case objectref.TypeEstimate:
		e, err := s.client.Estimate.Get(ctx, ref.ID)
		return err == nil && e.JobID != nil && s.IsUserAssignedToJob(ctx, *e.JobID, userID)
	case objectref.TypeInvoice:
		i, err := s.client.Invoice.Get(ctx, ref.ID)
		return err == nil && i.JobID != nil && s.IsUserAssignedToJob(ctx, *i.JobID, userID)
	default:
		return false
	}
}

func policyRoleAllows(role string, typ objectref.Type, action PolicyAction) bool {
	if !validPolicyAction(action) {
		return false
	}
	switch role {
	case "admin":
		return true
	case "dispatcher":
		switch typ {
		case objectref.TypeCustomer, objectref.TypeJob, objectref.TypeProject, objectref.TypeEstimate,
			objectref.TypeInvoice, objectref.TypeAsset, objectref.TypeItem, objectref.TypeTimeEntry:
			return true
		default:
			return false
		}
	case "tech", "technician":
		switch action {
		case PolicyRead, PolicyCreate, PolicyUpdate, PolicyAttachFile:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func isTechnicianRole(role string) bool {
	return role == "tech" || role == "technician"
}

func validPolicyAction(action PolicyAction) bool {
	switch action {
	case PolicyRead, PolicyCreate, PolicyUpdate, PolicyDelete, PolicyAttachFile:
		return true
	default:
		return false
	}
}

func (s *PolicyService) targetExists(ctx context.Context, ref objectref.Ref, action PolicyAction) bool {
	mode := objectref.ExistsAny
	if action != PolicyRead && s.objects.Supports(ref.Type, objectref.CapArchive) {
		mode = objectref.ExistsActive
	}
	exists, err := s.objects.Exists(ctx, ref, mode)
	return err == nil && exists
}

func (s *PolicyService) IsUserAssignedToJob(ctx context.Context, jobID, userID int64) bool {
	exists, err := s.client.JobAssignment.Query().Where(jobassignment.JobIDEQ(jobID), jobassignment.UserIDEQ(userID)).Exist(ctx)
	if err != nil || !exists {
		return false
	}
	active, err := s.client.Job.Query().Where(job.IDEQ(jobID), job.DeletedAtIsNil()).Exist(ctx)
	return err == nil && active
}

// CanCreateDocumentForJob is the handler seam for technician document creation.
// Company ownership remains the caller/module's responsibility because the legacy
// policy service does not carry an actor company scope.
func (s *PolicyService) CanCreateDocumentForJob(ctx context.Context, userID int64, role string, jobID int64) bool {
	if userID <= 0 || jobID <= 0 {
		return false
	}
	if role == "admin" || role == "dispatcher" {
		return true
	}
	return isTechnicianRole(role) && s.IsUserAssignedToJob(ctx, jobID, userID)
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
