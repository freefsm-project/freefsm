package services

import (
	"context"
	"fmt"
	"math"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/customer"
)

type CustomerService struct {
	client *ent.Client
}

func NewCustomerService(client *ent.Client) *CustomerService {
	return &CustomerService{client: client}
}

type CustomerCreateParams struct {
	FirstName       string
	LastName        string
	DisplayName     string
	Email           string
	Phone           string
	CompanyName     string
	Notes           string
	Status          string
	AccountType     string
	BillingAddress1 string
	BillingAddress2 string
	BillingCity     string
	BillingState    string
	BillingZipCode  string
	ServiceAddress1 string
	ServiceAddress2 string
	ServiceCity     string
	ServiceState    string
	ServiceZipCode  string
}

type CustomerUpdateParams struct {
	FirstName       *string
	LastName        *string
	DisplayName     *string
	Email           *string
	Phone           *string
	CompanyName     *string
	Notes           *string
	Status          *string
	AccountType     *string
	BillingAddress1 *string
	BillingAddress2 *string
	BillingCity     *string
	BillingState    *string
	BillingZipCode  *string
	ServiceAddress1 *string
	ServiceAddress2 *string
	ServiceCity     *string
	ServiceState    *string
	ServiceZipCode  *string
}

func (s *CustomerService) ListAll(ctx context.Context) ([]*ent.Customer, error) {
	return s.client.Customer.Query().Order(ent.Asc(customer.FieldDisplayName)).All(ctx)
}

func (s *CustomerService) List(ctx context.Context, search, status string, page, perPage int) ([]*ent.Customer, int, error) {
	q := s.client.Customer.Query()

	if search != "" {
		q = q.Where(
			customer.Or(
				customer.DisplayNameContainsFold(search),
				customer.EmailContainsFold(search),
				customer.PhoneContains(search),
				customer.FirstNameContainsFold(search),
				customer.LastNameContainsFold(search),
				customer.CompanyNameContainsFold(search),
			),
		)
	}

	if status != "" {
		q = q.Where(customer.StatusEQ(status))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count customers: %w", err)
	}

	offset := (page - 1) * perPage
	customers, err := q.
		Order(ent.Desc(customer.FieldUpdatedAt)).
		Limit(perPage).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list customers: %w", err)
	}

	return customers, total, nil
}

func (s *CustomerService) GetByID(ctx context.Context, id int64) (*ent.Customer, error) {
	c, err := s.client.Customer.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get customer %d: %w", id, err)
	}
	return c, nil
}

func (s *CustomerService) Create(ctx context.Context, params CustomerCreateParams) (*ent.Customer, error) {
	displayName := params.DisplayName
	if displayName == "" {
		if params.FirstName != "" || params.LastName != "" {
			displayName = params.FirstName + " " + params.LastName
		}
	}
	if displayName == "" {
		if params.CompanyName != "" {
			displayName = params.CompanyName
		}
	}

	c, err := s.client.Customer.
		Create().
		SetFirstName(params.FirstName).
		SetLastName(params.LastName).
		SetDisplayName(displayName).
		SetEmail(params.Email).
		SetPhone(params.Phone).
		SetCompanyName(params.CompanyName).
		SetNotes(params.Notes).
		SetStatus(params.Status).
		SetAccountType(params.AccountType).
		SetBillingAddress1(params.BillingAddress1).
		SetBillingAddress2(params.BillingAddress2).
		SetBillingCity(params.BillingCity).
		SetBillingState(params.BillingState).
		SetBillingZipCode(params.BillingZipCode).
		SetServiceAddress1(params.ServiceAddress1).
		SetServiceAddress2(params.ServiceAddress2).
		SetServiceCity(params.ServiceCity).
		SetServiceState(params.ServiceState).
		SetServiceZipCode(params.ServiceZipCode).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create customer: %w", err)
	}
	return c, nil
}

func (s *CustomerService) Update(ctx context.Context, id int64, params CustomerUpdateParams) (*ent.Customer, error) {
	u := s.client.Customer.UpdateOneID(id)

	if params.FirstName != nil {
		u.SetFirstName(*params.FirstName)
	}
	if params.LastName != nil {
		u.SetLastName(*params.LastName)
	}
	if params.DisplayName != nil {
		u.SetDisplayName(*params.DisplayName)
	}
	if params.Email != nil {
		u.SetEmail(*params.Email)
	}
	if params.Phone != nil {
		u.SetPhone(*params.Phone)
	}
	if params.CompanyName != nil {
		u.SetCompanyName(*params.CompanyName)
	}
	if params.Notes != nil {
		u.SetNotes(*params.Notes)
	}
	if params.Status != nil {
		u.SetStatus(*params.Status)
	}
	if params.AccountType != nil {
		u.SetAccountType(*params.AccountType)
	}
	if params.BillingAddress1 != nil {
		u.SetBillingAddress1(*params.BillingAddress1)
	}
	if params.BillingAddress2 != nil {
		u.SetBillingAddress2(*params.BillingAddress2)
	}
	if params.BillingCity != nil {
		u.SetBillingCity(*params.BillingCity)
	}
	if params.BillingState != nil {
		u.SetBillingState(*params.BillingState)
	}
	if params.BillingZipCode != nil {
		u.SetBillingZipCode(*params.BillingZipCode)
	}
	if params.ServiceAddress1 != nil {
		u.SetServiceAddress1(*params.ServiceAddress1)
	}
	if params.ServiceAddress2 != nil {
		u.SetServiceAddress2(*params.ServiceAddress2)
	}
	if params.ServiceCity != nil {
		u.SetServiceCity(*params.ServiceCity)
	}
	if params.ServiceState != nil {
		u.SetServiceState(*params.ServiceState)
	}
	if params.ServiceZipCode != nil {
		u.SetServiceZipCode(*params.ServiceZipCode)
	}

	c, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update customer %d: %w", id, err)
	}
	return c, nil
}

func (s *CustomerService) Delete(ctx context.Context, id int64) error {
	if err := s.client.Customer.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete customer %d: %w", id, err)
	}
	return nil
}

func PaginationTotalPages(total, perPage int) int {
	return int(math.Ceil(float64(total) / float64(perPage)))
}

var CustomerStatuses = []string{"lead", "opportunity", "customer", "lost", "inactive"}

var CustomerAccountTypes = []string{"individual", "company"}
