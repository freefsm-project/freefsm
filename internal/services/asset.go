package services

import (
	"context"
	"fmt"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/asset"
	"github.com/MartialM1nd/freefsm/internal/ent/job"
)

type AssetService struct {
	client *ent.Client
}

func NewAssetService(client *ent.Client) *AssetService {
	return &AssetService{client: client}
}

type AssetCreateParams struct {
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
	CustomerID      *int64
	LocationID      *int64
	AssetTypeID     *int64
	AssetStatusID   *int64
	Name            *string
	SerialNumber    *string
	Model           *string
	Manufacturer    *string
	Notes           *string
	InstalledAt     *time.Time
	WarrantyExpires *time.Time
	CustomFields    *string
}

func (s *AssetService) List(ctx context.Context, search string, customerID, assetTypeID, assetStatusID int64, page, perPage int) ([]*ent.Asset, int, error) {
	q := s.client.Asset.Query().Where(asset.DeletedAtIsNil())

	if search != "" {
		q = q.Where(
			asset.Or(
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
		Where(asset.CustomerID(customerID)).
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

func (s *AssetService) Create(ctx context.Context, params AssetCreateParams) (*ent.Asset, error) {
	b := s.client.Asset.Create().
		SetCustomerID(params.CustomerID).
		SetAssetTypeID(params.AssetTypeID).
		SetName(params.Name).
		SetSerialNumber(params.SerialNumber).
		SetModel(params.Model).
		SetManufacturer(params.Manufacturer).
		SetNotes(params.Notes).
		SetCustomFields(params.CustomFields)

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
	return a, nil
}

func (s *AssetService) Update(ctx context.Context, id int64, params AssetUpdateParams) (*ent.Asset, error) {
	u := s.client.Asset.UpdateOneID(id)

	if params.CustomerID != nil {
		u.SetCustomerID(*params.CustomerID)
	}
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
	}
	if params.WarrantyExpires != nil {
		u.SetWarrantyExpires(*params.WarrantyExpires)
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

func (s *AssetService) Delete(ctx context.Context, id int64) error {
	if err := s.client.Asset.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete asset %d: %w", id, err)
	}
	return nil
}

func (s *AssetService) Archive(ctx context.Context, id int64) error {
	now := time.Now()
	_, err := s.client.Asset.UpdateOneID(id).SetDeletedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("archive asset %d: %w", id, err)
	}
	return nil
}

func (s *AssetService) Restore(ctx context.Context, id int64) error {
	_, err := s.client.Asset.UpdateOneID(id).ClearDeletedAt().Save(ctx)
	if err != nil {
		return fmt.Errorf("restore asset %d: %w", id, err)
	}
	return nil
}

func (s *AssetService) GetServiceHistory(ctx context.Context, assetID int64) ([]*ent.Job, error) {
	return s.client.Job.Query().
		Where(job.AssetIDEQ(assetID)).
		Order(ent.Desc(job.FieldUpdatedAt)).
		All(ctx)
}

func AssetPaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}
