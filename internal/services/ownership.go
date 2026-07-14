package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/asset"
	"github.com/freefsm-project/freefsm/internal/ent/customer"
	"github.com/freefsm-project/freefsm/internal/ent/customercontact"
	"github.com/freefsm-project/freefsm/internal/ent/estimate"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/location"
	"github.com/freefsm-project/freefsm/internal/ent/project"
	"github.com/freefsm-project/freefsm/internal/ent/status"
	"github.com/freefsm-project/freefsm/internal/ent/statusworkflow"
)

func validateCustomerLocation(ctx context.Context, client *ent.Client, customerID, locationID int64) error {
	if locationID <= 0 {
		return nil
	}
	exists, err := client.Location.Query().
		Where(location.IDEQ(locationID), location.ObjectTypeEQ("customer"), location.ObjectIDEQ(customerID)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate location customer: %w", err)
	}
	if !exists {
		return fmt.Errorf("location does not belong to customer")
	}
	return nil
}

var ErrInvalidDocumentStatus = fmt.Errorf("status must belong to the same company and document workflow")

var ErrInvalidJobInput = errors.New("invalid job input")

type JobInputError struct {
	Cause error
}

func (e *JobInputError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrInvalidJobInput.Error()
	}
	return fmt.Sprintf("%s: %v", ErrInvalidJobInput, e.Cause)
}

func (e *JobInputError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *JobInputError) Is(target error) bool {
	return target == ErrInvalidJobInput
}

func invalidJobInput(message string) error {
	return &JobInputError{Cause: errors.New(message)}
}

type StatusConfigurationError struct {
	CompanyID  int64
	ObjectType string
	Category   string
	Cause      error
}

func (e *StatusConfigurationError) Error() string {
	return "status configuration unavailable"
}

func (e *StatusConfigurationError) Unwrap() error {
	return e.Cause
}

func creationStatus(ctx context.Context, client *ent.Client, requested int64, companyID *int64, objectType, category string) (int64, error) {
	if requested > 0 {
		if err := validateDocumentStatus(ctx, client, requested, companyID, objectType); err != nil {
			return 0, err
		}
		if objectType == "invoice" {
			selected, err := client.Status.Get(ctx, requested)
			if err != nil {
				return 0, fmt.Errorf("load initial invoice status: %w", err)
			}
			if selected.CategoryKey == "invoice:partially_paid" || selected.CategoryKey == "invoice:paid" {
				return 0, ErrInvalidDocumentStatus
			}
		}
		return requested, nil
	}
	q := client.Status.Query().Where(
		status.CategoryKeyEQ(category),
		status.IsCategoryDefaultEQ(true),
		status.HasWorkflowWith(statusworkflow.ObjectTypeEQ(objectType)),
	)
	if companyID != nil {
		q = q.Where(status.CompanyIDEQ(*companyID), status.HasWorkflowWith(statusworkflow.CompanyIDEQ(*companyID)))
	} else {
		q = q.Where(status.CompanyIDIsNil(), status.HasWorkflowWith(statusworkflow.CompanyIDIsNil()))
	}
	st, err := q.Only(ctx)
	if err != nil {
		var id int64
		if companyID != nil {
			id = *companyID
		}
		return 0, &StatusConfigurationError{CompanyID: id, ObjectType: objectType, Category: category, Cause: err}
	}
	return st.ID, nil
}

func validateDocumentStatus(ctx context.Context, client *ent.Client, statusID int64, companyID *int64, objectType string) error {
	if statusID <= 0 {
		return nil
	}
	q := client.Status.Query().Where(status.IDEQ(statusID), status.HasWorkflowWith(statusworkflow.ObjectTypeEQ(objectType)))
	if companyID != nil {
		q = q.Where(status.CompanyIDEQ(*companyID), status.HasWorkflowWith(statusworkflow.CompanyIDEQ(*companyID)))
	} else {
		q = q.Where(status.CompanyIDIsNil(), status.HasWorkflowWith(statusworkflow.CompanyIDIsNil()))
	}
	ok, err := q.Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate %s status: %w", objectType, err)
	}
	if !ok {
		return ErrInvalidDocumentStatus
	}
	return nil
}

func validateActiveCustomer(ctx context.Context, client *ent.Client, customerID int64) error {
	if customerID <= 0 {
		return fmt.Errorf("customer is required")
	}
	exists, err := client.Customer.Query().Where(customer.IDEQ(customerID), customer.DeletedAtIsNil()).Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate customer: %w", err)
	}
	if !exists {
		return fmt.Errorf("customer does not exist or is archived")
	}
	return nil
}

func validateProjectCustomer(ctx context.Context, client *ent.Client, customerID, projectID int64, requireActive bool) error {
	if projectID <= 0 {
		return nil
	}
	q := client.Project.Query().Where(project.IDEQ(projectID), project.CustomerIDEQ(customerID))
	if requireActive {
		q = q.Where(project.DeletedAtIsNil())
	}
	exists, err := q.Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate project customer: %w", err)
	}
	if !exists {
		return fmt.Errorf("project does not belong to customer")
	}
	return nil
}

func validateAssetCustomer(ctx context.Context, client *ent.Client, customerID, assetID int64, requireActive bool) error {
	if assetID <= 0 {
		return nil
	}
	q := client.Asset.Query().Where(asset.IDEQ(assetID), asset.CustomerID(customerID))
	if requireActive {
		q = q.Where(asset.DeletedAtIsNil())
	}
	exists, err := q.Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate asset customer: %w", err)
	}
	if !exists {
		return fmt.Errorf("asset does not belong to customer")
	}
	return nil
}

func validateContactCustomer(ctx context.Context, client *ent.Client, customerID, contactID int64) error {
	if contactID <= 0 {
		return nil
	}
	exists, err := client.CustomerContact.Query().
		Where(customercontact.IDEQ(contactID), customercontact.CustomerIDEQ(customerID)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate contact customer: %w", err)
	}
	if !exists {
		return fmt.Errorf("contact does not belong to customer")
	}
	return nil
}

func validateJobCustomer(ctx context.Context, client *ent.Client, customerID, jobID int64, requireActive bool) error {
	if jobID <= 0 {
		return nil
	}
	q := client.Job.Query().Where(job.IDEQ(jobID), job.CustomerIDEQ(customerID))
	if requireActive {
		q = q.Where(job.DeletedAtIsNil())
	}
	exists, err := q.Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate job customer: %w", err)
	}
	if !exists {
		return fmt.Errorf("job does not belong to customer")
	}
	return nil
}

func validateEstimateCustomer(ctx context.Context, client *ent.Client, customerID, estimateID int64, requireActive bool) error {
	if estimateID <= 0 {
		return nil
	}
	q := client.Estimate.Query().Where(estimate.IDEQ(estimateID), estimate.CustomerIDEQ(customerID))
	if requireActive {
		q = q.Where(estimate.DeletedAtIsNil())
		q = q.Where(estimate.ConversionHiddenAtIsNil())
	}
	exists, err := q.Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate estimate customer: %w", err)
	}
	if !exists {
		return fmt.Errorf("estimate does not belong to customer")
	}
	return nil
}

func validateJobCustomerLinks(ctx context.Context, client *ent.Client, companyID, customerID, projectID, locationID, contactID, assetID int64) error {
	checks := []struct {
		name   string
		id     int64
		exists func() (bool, error)
	}{
		{
			name: "project", id: projectID,
			exists: func() (bool, error) {
				return client.Project.Query().Where(project.IDEQ(projectID), project.CompanyIDEQ(companyID), project.CustomerIDEQ(customerID), project.DeletedAtIsNil()).Exist(ctx)
			},
		},
		{
			name: "location", id: locationID,
			exists: func() (bool, error) {
				return client.Location.Query().Where(location.IDEQ(locationID), location.CompanyIDEQ(companyID), location.ObjectTypeEQ("customer"), location.ObjectIDEQ(customerID)).Exist(ctx)
			},
		},
		{
			name: "contact", id: contactID,
			exists: func() (bool, error) {
				return client.CustomerContact.Query().Where(customercontact.IDEQ(contactID), customercontact.CompanyIDEQ(companyID), customercontact.CustomerIDEQ(customerID)).Exist(ctx)
			},
		},
		{
			name: "asset", id: assetID,
			exists: func() (bool, error) {
				return client.Asset.Query().Where(asset.IDEQ(assetID), asset.CompanyIDEQ(companyID), asset.CustomerID(customerID), asset.DeletedAtIsNil()).Exist(ctx)
			},
		},
	}
	for _, check := range checks {
		if check.id <= 0 {
			continue
		}
		exists, err := check.exists()
		if err != nil {
			return fmt.Errorf("validate %s customer and company: %w", check.name, err)
		}
		if !exists {
			return invalidJobInput(fmt.Sprintf("%s does not belong to customer and company", check.name))
		}
	}
	return nil
}

func int64Value(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
