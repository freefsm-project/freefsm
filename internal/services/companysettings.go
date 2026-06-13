package services

import (
	"context"

	"github.com/MartialM1nd/freefsm/internal/ent"
)

type CompanySettingsService struct {
	client *ent.Client
}

func NewCompanySettingsService(client *ent.Client) *CompanySettingsService {
	return &CompanySettingsService{client: client}
}

func (s *CompanySettingsService) Get(ctx context.Context) (*ent.CompanySettings, error) {
	return s.client.CompanySettings.Query().First(ctx)
}

type CompanySettingsParams struct {
	BusinessName    string
	Address         string
	City            string
	State           string
	Zip             string
	Phone           string
	Email           string
	TaxID           string
	DefaultTaxRate  string
	InvoicePrefix   string
	EstimatePrefix  string
	DefaultDueDays  int
}

func (s *CompanySettingsService) Save(ctx context.Context, p CompanySettingsParams) error {
	cs, err := s.Get(ctx)
	if err != nil {
		return err
	}
	_, err = s.client.CompanySettings.UpdateOne(cs).
		SetBusinessName(p.BusinessName).
		SetAddress(p.Address).
		SetCity(p.City).
		SetState(p.State).
		SetZip(p.Zip).
		SetPhone(p.Phone).
		SetEmail(p.Email).
		SetTaxID(p.TaxID).
		SetDefaultTaxRate(p.DefaultTaxRate).
		SetInvoicePrefix(p.InvoicePrefix).
		SetEstimatePrefix(p.EstimatePrefix).
		SetDefaultDueDays(p.DefaultDueDays).
		Save(ctx)
	return err
}
