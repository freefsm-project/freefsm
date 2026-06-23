package services

import (
	"context"
	"fmt"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/asset"
	"github.com/MartialM1nd/freefsm/internal/ent/customer"
	"github.com/MartialM1nd/freefsm/internal/ent/customercontact"
	"github.com/MartialM1nd/freefsm/internal/ent/estimate"
	"github.com/MartialM1nd/freefsm/internal/ent/job"
	"github.com/MartialM1nd/freefsm/internal/ent/location"
	"github.com/MartialM1nd/freefsm/internal/ent/project"
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

func validateJobCustomerLinks(ctx context.Context, client *ent.Client, customerID, projectID, locationID, contactID, assetID int64) error {
	if err := validateProjectCustomer(ctx, client, customerID, projectID, true); err != nil {
		return err
	}
	if err := validateCustomerLocation(ctx, client, customerID, locationID); err != nil {
		return err
	}
	if err := validateContactCustomer(ctx, client, customerID, contactID); err != nil {
		return err
	}
	if err := validateAssetCustomer(ctx, client, customerID, assetID, true); err != nil {
		return err
	}
	return nil
}

func int64Value(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
