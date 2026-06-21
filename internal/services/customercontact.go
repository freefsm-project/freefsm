package services

import (
	"context"
	"fmt"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/customercontact"
)

type CustomerContactService struct {
	client *ent.Client
}

func NewCustomerContactService(client *ent.Client) *CustomerContactService {
	return &CustomerContactService{client: client}
}

func (s *CustomerContactService) ListByCustomer(ctx context.Context, customerID int64) ([]*ent.CustomerContact, error) {
	return s.client.CustomerContact.Query().
		Where(customercontact.CustomerIDEQ(customerID)).
		Order(ent.Asc(customercontact.FieldSortOrder)).
		All(ctx)
}

func (s *CustomerContactService) ListAll(ctx context.Context) ([]*ent.CustomerContact, error) {
	return s.client.CustomerContact.Query().Order(ent.Asc(customercontact.FieldSortOrder)).All(ctx)
}

type ContactCreateParams struct {
	FirstName string
	LastName  string
	Email     string
	Phone     string
	Notes     string
}

type ContactUpdateParams struct {
	FirstName *string
	LastName  *string
	Email     *string
	Phone     *string
	Notes     *string
}

func (s *CustomerContactService) Create(ctx context.Context, customerID int64, params ContactCreateParams) (*ent.CustomerContact, error) {
	c, err := s.client.CustomerContact.Create().
		SetCustomerID(customerID).
		SetFirstName(params.FirstName).
		SetLastName(params.LastName).
		SetEmail(params.Email).
		SetPhone(params.Phone).
		SetNotes(params.Notes).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create contact: %w", err)
	}
	return c, nil
}

func (s *CustomerContactService) Update(ctx context.Context, id int64, params ContactUpdateParams) (*ent.CustomerContact, error) {
	u := s.client.CustomerContact.UpdateOneID(id)
	if params.FirstName != nil {
		u.SetFirstName(*params.FirstName)
	}
	if params.LastName != nil {
		u.SetLastName(*params.LastName)
	}
	if params.Email != nil {
		u.SetEmail(*params.Email)
	}
	if params.Phone != nil {
		u.SetPhone(*params.Phone)
	}
	if params.Notes != nil {
		u.SetNotes(*params.Notes)
	}
	c, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update contact: %w", err)
	}
	return c, nil
}

func (s *CustomerContactService) GetByID(ctx context.Context, id int64) (*ent.CustomerContact, error) {
	return s.client.CustomerContact.Get(ctx, id)
}

func (s *CustomerContactService) Delete(ctx context.Context, id int64) error {
	if err := s.client.CustomerContact.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete contact: %w", err)
	}
	return nil
}
