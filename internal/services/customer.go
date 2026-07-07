package services

import (
	"context"
	"fmt"
	"time"

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
	TaxExempt       bool
	CustomFields    string
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
	TaxExempt       *bool
	CustomFields    *string
}

func (s *CustomerService) ListAll(ctx context.Context) ([]*ent.Customer, error) {
	return s.client.Customer.Query().Where(customer.DeletedAtIsNil()).Order(ent.Asc(customer.FieldDisplayName)).All(ctx)
}

func (s *CustomerService) ListByIDs(ctx context.Context, ids []int64) ([]*ent.Customer, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return s.client.Customer.Query().Where(customer.DeletedAtIsNil(), customer.IDIn(ids...)).All(ctx)
}

func (s *CustomerService) List(ctx context.Context, search, status string, page, perPage int) ([]*ent.Customer, int, error) {
	q := s.client.Customer.Query().Where(customer.DeletedAtIsNil())

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

	offset := PaginationOffset(page, perPage)
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
		SetTaxExempt(params.TaxExempt).
		SetCustomFields(params.CustomFields).
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
	if params.TaxExempt != nil {
		u.SetTaxExempt(*params.TaxExempt)
	}
	if params.CustomFields != nil {
		u.SetCustomFields(*params.CustomFields)
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

func (s *CustomerService) Archive(ctx context.Context, id int64) error {
	now := time.Now()
	_, err := s.client.Customer.UpdateOneID(id).SetDeletedAt(now).Save(ctx)
	if err != nil {
		return fmt.Errorf("archive customer %d: %w", id, err)
	}
	return nil
}

func (s *CustomerService) Restore(ctx context.Context, id int64) error {
	_, err := s.client.Customer.UpdateOneID(id).ClearDeletedAt().Save(ctx)
	if err != nil {
		return fmt.Errorf("restore customer %d: %w", id, err)
	}
	return nil
}

func PaginationTotalPages(total, perPage int) int {
	return TotalPages(total, perPage)
}

var CustomerStatuses = []string{"lead", "opportunity", "customer", "lost", "inactive"}

var CustomerAccountTypes = []string{"individual", "company"}
