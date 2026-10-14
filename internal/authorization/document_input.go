package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// DocumentCustomer is a purpose-limited selected input. Effective tax belongs
// to separately authorized document pricing; no master exemption flag, billing
// address, notes, fields or sibling records escape through this projection.
type DocumentCustomer struct {
	ID          int64
	CompanyID   int64
	DisplayName string
	TaxRate     string
}

func (s *Service) DocumentCustomer(ctx context.Context, a Actor, customerID, jobID int64, rate string) (DocumentCustomer, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return DocumentCustomer{}, err
	}
	if !p.Allows("customer.view", All) && !p.Allows("customer.view", Context) {
		return DocumentCustomer{}, ErrNotFound
	}
	if !p.Allows("pricing.view", All) && !p.Allows("pricing.view", Assigned) {
		return DocumentCustomer{}, ErrForbidden
	}
	var result DocumentCustomer
	err = s.reads(ctx).QueryRow(ctx, `SELECT c.id,c.company_id,c.display_name,CASE WHEN c.tax_exempt THEN '0' ELSE $4::text END
 FROM customers c JOIN users u ON u.company_id=c.company_id
 JOIN authorization_grants cv ON cv.role_id=u.authorization_role_id AND cv.permission='customer.view'
 JOIN authorization_grants pv ON pv.role_id=u.authorization_role_id AND pv.permission='pricing.view'
 WHERE c.id=$2 AND u.id=$1 AND u.is_active AND c.deleted_at IS NULL AND cv.scope IN ('A','C') AND pv.scope IN ('A','J')
 AND ((cv.scope='A' AND pv.scope='A') OR EXISTS(
 SELECT 1 FROM jobs j JOIN job_assignments ja ON ja.job_id=j.id
 JOIN authorization_grants jv ON jv.role_id=u.authorization_role_id AND jv.permission='job.view' AND jv.scope IN ('A','J')
 WHERE j.id=$3 AND j.customer_id=c.id AND j.company_id=u.company_id AND ja.user_id=u.id AND j.deleted_at IS NULL))`, a.UserID, customerID, jobID, rate).Scan(&result.ID, &result.CompanyID, &result.DisplayName, &result.TaxRate)
	if err == pgx.ErrNoRows {
		return result, ErrNotFound
	}
	return result, err
}
