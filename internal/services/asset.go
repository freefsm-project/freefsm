package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/asset"
	"github.com/freefsm-project/freefsm/internal/ent/assetstatus"
	"github.com/freefsm-project/freefsm/internal/ent/assettype"
	"github.com/freefsm-project/freefsm/internal/ent/customer"
	"github.com/freefsm-project/freefsm/internal/ent/job"
)

type AssetService struct {
	client *ent.Client
}

func NewAssetService(client *ent.Client) *AssetService {
	return &AssetService{client: client}
}

type AssetCreateParams struct {
	Number          string
	OwnershipClass  string
	CustomerID      int64
	LocationID      *int64
	AssetTypeID     int64
	AssetStatusID   *int64
	Name            string
	SerialNumber    string
	Model           string
	Manufacturer    string
	Notes           string
	InstalledAt     *time.Time
	WarrantyExpires *time.Time
	CustomFields    string
}

type AssetUpdateParams struct {
	ClearInstalledAt     bool
	ClearWarrantyExpires bool
	Number               *string
	OwnershipClass       *string
	CustomerID           *int64
	LocationID           *int64
	AssetTypeID          *int64
	AssetStatusID        *int64
	Name                 *string
	SerialNumber         *string
	Model                *string
	Manufacturer         *string
	Notes                *string
	InstalledAt          *time.Time
	WarrantyExpires      *time.Time
	CustomFields         *string
}

func (s *AssetService) List(ctx context.Context, search string, customerID, assetTypeID, assetStatusID int64, page, perPage int) ([]*ent.Asset, int, error) {
	q := s.client.Asset.Query().Where(asset.DeletedAtIsNil())

	if search != "" {
		q = q.Where(
			asset.Or(
				asset.NumberContainsFold(search),
				asset.NameContainsFold(search),
				asset.SerialNumberContainsFold(search),
				asset.ModelContainsFold(search),
				asset.ManufacturerContainsFold(search),
			),
		)
	}

	if customerID > 0 {
		q = q.Where(asset.CustomerID(customerID))
	}
	if assetTypeID > 0 {
		q = q.Where(asset.AssetTypeID(assetTypeID))
	}
	if assetStatusID > 0 {
		q = q.Where(asset.AssetStatusID(assetStatusID))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count assets: %w", err)
	}

	offset := PaginationOffset(page, perPage)
	assets, err := q.
		Order(ent.Desc(asset.FieldUpdatedAt)).
		Limit(perPage).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list assets: %w", err)
	}

	return assets, total, nil
}

func (s *AssetService) ListForCustomer(ctx context.Context, customerID int64) ([]*ent.Asset, error) {
	return s.client.Asset.Query().
		Where(asset.CustomerID(customerID), asset.DeletedAtIsNil()).
		Order(ent.Asc(asset.FieldName)).
		All(ctx)
}

func (s *AssetService) ListAll(ctx context.Context) ([]*ent.Asset, error) {
	return s.client.Asset.Query().
		Where(asset.DeletedAtIsNil()).
		Order(ent.Asc(asset.FieldName)).
		All(ctx)
}

func (s *AssetService) GetByID(ctx context.Context, id int64) (*ent.Asset, error) {
	a, err := s.client.Asset.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get asset %d: %w", id, err)
	}
	return a, nil
}

func (s *AssetService) Create(ctx context.Context, companyID int64, params AssetCreateParams) (*ent.Asset, error) {
	if needsDetailsTransaction(ctx) {
		return saveDetails(ctx, "asset", 0, 0, &params.CustomFields, func(ctx context.Context, c *ent.Client, fields string) (*ent.Asset, int64, error) {
			params.CustomFields = fields
			v, err := NewAssetService(c).Create(ctx, companyID, params)
			if err != nil {
				return nil, 0, err
			}
			return v, v.ID, nil
		})
	}
	if params.OwnershipClass != "customer_owned" && params.OwnershipClass != "company_owned" {
		return nil, fmt.Errorf("%w: choose an Asset ownership class", authorization.ErrConflict)
	}
	if strings.TrimSpace(params.Name) == "" {
		return nil, fmt.Errorf("%w: Asset name is required", authorization.ErrConflict)
	}
	if params.CustomerID == 0 && params.OwnershipClass == "customer_owned" {
		return nil, fmt.Errorf("%w: Customer-owned Assets require a Customer", authorization.ErrConflict)
	}
	if params.CustomerID == 0 && params.LocationID != nil && *params.LocationID > 0 {
		return nil, fmt.Errorf("%w: unplaced Assets cannot have a Customer Location", authorization.ErrConflict)
	}
	if params.CustomerID > 0 {
		if req, ok := authorization.FromContext(ctx); ok {
			if err := req.Service.ParentAccess(ctx, req.Actor, "customer", params.CustomerID, ""); err != nil {
				return nil, err
			}
		}
		active, err := s.client.Customer.Query().Where(customer.IDEQ(params.CustomerID), customer.CompanyIDEQ(companyID), customer.DeletedAtIsNil()).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !active {
			return nil, fmt.Errorf("%w: select an active Customer", authorization.ErrConflict)
		}
	}
	if err := validateAssetTypeForCompany(ctx, s.client, companyID, params.AssetTypeID); err != nil {
		return nil, err
	}
	if params.AssetStatusID != nil && *params.AssetStatusID > 0 {
		if err := validateAssetStatusForCompany(ctx, s.client, companyID, *params.AssetStatusID); err != nil {
			return nil, err
		}
	}
	if params.LocationID != nil {
		if req, ok := authorization.FromContext(ctx); ok && *params.LocationID > 0 {
			if _, err := req.Service.CustomerChild(ctx, req.Actor, params.CustomerID, *params.LocationID, "location"); err != nil {
				return nil, err
			}
		}
		if err := validateCustomerLocationForCompany(ctx, s.client, companyID, params.CustomerID, *params.LocationID); err != nil {
			return nil, err
		}
	}

	b := s.client.Asset.Create().
		SetCompanyID(companyID).
		SetOwnershipClass(params.OwnershipClass).
		SetAssetTypeID(params.AssetTypeID).
		SetName(params.Name).
		SetSerialNumber(params.SerialNumber).
		SetModel(params.Model).
		SetManufacturer(params.Manufacturer).
		SetNotes(params.Notes).
		SetCustomFields(params.CustomFields)
	if params.CustomerID > 0 {
		b.SetCustomerID(params.CustomerID)
	}
	if strings.TrimSpace(params.Number) != "" {
		b.SetNumber(strings.TrimSpace(params.Number))
	}

	if params.LocationID != nil && *params.LocationID > 0 {
		b.SetLocationID(*params.LocationID)
	}
	if params.AssetStatusID != nil && *params.AssetStatusID > 0 {
		b.SetAssetStatusID(*params.AssetStatusID)
	}
	if params.InstalledAt != nil {
		b.SetInstalledAt(*params.InstalledAt)
	}
	if params.WarrantyExpires != nil {
		b.SetWarrantyExpires(*params.WarrantyExpires)
	}

	a, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create asset: %w", err)
	}
	// Read server-generated Number in the same committing transaction.
	return s.client.Asset.Get(ctx, a.ID)
}

func (s *AssetService) Update(ctx context.Context, companyID, id int64, params AssetUpdateParams) (*ent.Asset, error) {
	if _, ok := authorization.FromContext(ctx); ok {
		return s.SaveDetails(ctx, companyID, id, params)
	}
	return s.update(ctx, companyID, id, params)
}

func (s *AssetService) SaveDetails(ctx context.Context, companyID, id int64, params AssetUpdateParams) (*ent.Asset, error) {
	return saveDetails(ctx, "asset", id, 0, params.CustomFields, func(ctx context.Context, c *ent.Client, fields string) (*ent.Asset, int64, error) {
		params.CustomFields = &fields
		v, err := NewAssetService(c).update(ctx, companyID, id, params)
		return v, id, err
	})
}

func (s *AssetService) update(ctx context.Context, companyID, id int64, params AssetUpdateParams) (*ent.Asset, error) {
	if params.Name != nil && strings.TrimSpace(*params.Name) == "" {
		return nil, fmt.Errorf("%w: Asset name is required", authorization.ErrConflict)
	}
	current, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.CompanyID != nil && *current.CompanyID != companyID {
		return nil, fmt.Errorf("asset does not belong to company")
	}
	if (params.Number != nil && *params.Number != current.Number) || (params.OwnershipClass != nil && *params.OwnershipClass != current.OwnershipClass) || (params.CustomerID != nil && *params.CustomerID != current.CustomerID) {
		return nil, fmt.Errorf("%w: Number, class and Customer association cannot change through Edit", authorization.ErrConflict)
	}
	customerID := current.CustomerID
	locationID := int64Value(current.LocationID)
	assetTypeID := current.AssetTypeID
	assetStatusID := int64Value(current.AssetStatusID)
	if params.CustomerID != nil {
		customerID = *params.CustomerID
	}
	if params.LocationID != nil {
		locationID = *params.LocationID
	}
	if params.AssetTypeID != nil {
		assetTypeID = *params.AssetTypeID
	}
	if params.AssetStatusID != nil {
		assetStatusID = *params.AssetStatusID
	}
	if err := validateAssetTypeForCompany(ctx, s.client, companyID, assetTypeID); err != nil {
		return nil, err
	}
	if assetStatusID > 0 {
		if err := validateAssetStatusForCompany(ctx, s.client, companyID, assetStatusID); err != nil {
			return nil, err
		}
	}
	if locationID != int64Value(current.LocationID) {
		if customerID == 0 && locationID > 0 {
			return nil, fmt.Errorf("%w: unplaced Assets cannot have a Customer Location", authorization.ErrConflict)
		}
		if req, ok := authorization.FromContext(ctx); ok && locationID > 0 {
			if _, err := req.Service.CustomerChild(ctx, req.Actor, customerID, locationID, "location"); err != nil {
				return nil, err
			}
		}
		if err := validateCustomerLocationForCompany(ctx, s.client, companyID, customerID, locationID); err != nil {
			return nil, err
		}
	}

	u := s.client.Asset.UpdateOneID(id).SetCompanyID(companyID)

	if params.LocationID != nil {
		if *params.LocationID > 0 {
			u.SetLocationID(*params.LocationID)
		} else {
			u.ClearLocationID()
		}
	}
	if params.AssetTypeID != nil {
		u.SetAssetTypeID(*params.AssetTypeID)
	}
	if params.AssetStatusID != nil {
		if *params.AssetStatusID > 0 {
			u.SetAssetStatusID(*params.AssetStatusID)
		} else {
			u.ClearAssetStatusID()
		}
	}
	if params.Name != nil {
		u.SetName(*params.Name)
	}
	if params.SerialNumber != nil {
		u.SetSerialNumber(*params.SerialNumber)
	}
	if params.Model != nil {
		u.SetModel(*params.Model)
	}
	if params.Manufacturer != nil {
		u.SetManufacturer(*params.Manufacturer)
	}
	if params.Notes != nil {
		u.SetNotes(*params.Notes)
	}
	if params.InstalledAt != nil {
		u.SetInstalledAt(*params.InstalledAt)
	} else if params.ClearInstalledAt {
		u.ClearInstalledAt()
	}
	if params.WarrantyExpires != nil {
		u.SetWarrantyExpires(*params.WarrantyExpires)
	} else if params.ClearWarrantyExpires {
		u.ClearWarrantyExpires()
	}
	if params.CustomFields != nil {
		u.SetCustomFields(*params.CustomFields)
	}

	a, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update asset %d: %w", id, err)
	}
	return a, nil
}

func validateAssetTypeForCompany(ctx context.Context, client *ent.Client, companyID, assetTypeID int64) error {
	if assetTypeID <= 0 {
		return fmt.Errorf("%w: asset type is required", authorization.ErrConflict)
	}
	exists, err := client.AssetType.Query().Where(
		assettype.IDEQ(assetTypeID),
		assettype.Or(assettype.CompanyIDEQ(companyID), assettype.CompanyIDIsNil()),
	).Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate asset type ownership: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: asset type does not belong to company", authorization.ErrConflict)
	}
	return nil
}

func validateAssetStatusForCompany(ctx context.Context, client *ent.Client, companyID, assetStatusID int64) error {
	exists, err := client.AssetStatus.Query().Where(
		assetstatus.IDEQ(assetStatusID),
		assetstatus.Or(assetstatus.CompanyIDEQ(companyID), assetstatus.CompanyIDIsNil()),
	).Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate asset status ownership: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: asset status does not belong to company", authorization.ErrConflict)
	}
	return nil
}

func (s *AssetService) Delete(ctx context.Context, id int64) error {
	if err := s.client.Asset.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete asset %d: %w", id, err)
	}
	return nil
}

func (s *AssetService) Archive(ctx context.Context, id int64) error {
	if req, ok := authorization.FromContext(ctx); ok {
		return req.Service.AssetLifecycle(ctx, req.Actor, id, false)
	}
	now := time.Now()
	_, err := s.client.Asset.UpdateOneID(id).SetDeletedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("archive asset %d: %w", id, err)
	}
	return nil
}

func (s *AssetService) Restore(ctx context.Context, id int64) error {
	if req, ok := authorization.FromContext(ctx); ok {
		return req.Service.AssetLifecycle(ctx, req.Actor, id, true)
	}
	_, err := s.client.Asset.UpdateOneID(id).ClearDeletedAt().Save(ctx)
	if err != nil {
		return fmt.Errorf("restore asset %d: %w", id, err)
	}
	return nil
}

func (s *AssetService) GetServiceHistory(ctx context.Context, assetID int64) ([]*ent.Job, error) {
	return s.client.Job.Query().
		Where(job.DeletedAtIsNil(), job.AssetIDEQ(assetID)).
		Order(ent.Desc(job.FieldUpdatedAt)).
		All(ctx)
}

func AssetPaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}
