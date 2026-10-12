package delivery

import "context"

// HTMLTemplate supplies a future compose default, never modifies a Delivery.
func (s *Service) HTMLTemplate(ctx context.Context, company int64, kind string) (string, error) {
	var value string
	if kind != "invoice" && kind != "estimate" {
		return "", ErrInvalid
	}
	err := s.db.QueryRow(ctx, `SELECT CASE WHEN $2='invoice' THEN invoice_email_html ELSE estimate_email_html END FROM company_settings WHERE company_id=$1`, company, kind).Scan(&value)
	return value, err
}
