package services

import (
	"context"

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
