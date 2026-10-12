package authorization

import (
	"context"
	"encoding/json"

	"github.com/freefsm-project/freefsm/internal/ent"
)

type CustomerSearchResult struct {
	Customer *ent.Customer
	Match    string
}

func (s *Service) FindCustomers(ctx context.Context, a Actor, search string, archived bool, page, size int) ([]CustomerSearchResult, int, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return nil, 0, err
	}
	if !p.Allows("customer.view", All) {
		return nil, 0, ErrForbidden
	}
	if archived && !p.Allows("customer.archived.view", All) {
		return nil, 0, ErrForbidden
	}
	if size != 25 && size != 50 && size != 100 {
		size = 25
	}
	if page < 1 {
		page = 1
	}
	// Scalar relationship matches preserve one Customer result and identify a
	// deterministic matching record without exposing denied child identities.
	query := `WITH matches AS (SELECT c.*,coalesce(
 CASE WHEN $3='' OR strpos(lower(concat_ws(' ',c.id::text,c.display_name,c.first_name,c.last_name,c.company_name,c.email,c.phone)),lower($3))>0 THEN 'Customer match' END,
 CASE WHEN $4 THEN (SELECT 'Contact match: '||trim(x.first_name||' '||x.last_name)||' (#'||x.id::text||')' FROM customer_contacts x WHERE x.customer_id=c.id AND x.company_id=c.company_id AND x.deleted_at IS NULL AND strpos(lower(concat_ws(' ',x.first_name,x.last_name,x.email,x.phone)),lower($3))>0 ORDER BY lower(x.first_name||' '||x.last_name),x.id LIMIT 1) END,
 CASE WHEN $5 THEN (SELECT 'Location match: '||x.title||' (#'||x.id::text||')' FROM locations x WHERE x.object_type='customer' AND x.object_id=c.id AND x.company_id=c.company_id AND x.deleted_at IS NULL AND strpos(lower(concat_ws(' ',x.title,x.address_1,x.address_2,x.city,x.state,x.zip_code)),lower($3))>0 ORDER BY lower(x.title),x.id LIMIT 1) END,
 '') AS relationship_match FROM customers c WHERE c.company_id=$1 AND ($2 OR c.deleted_at IS NULL))`
	args := []any{p.CompanyID, archived, search, p.Allows("contact.view", All), p.Allows("location.view", All)}
	var total int
	if err = s.Pool.QueryRow(ctx, query+` SELECT count(*) FROM matches WHERE relationship_match<>''`, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, size, (page-1)*size)
	rows, err := s.Pool.Query(ctx, query+` SELECT to_jsonb(m)||jsonb_build_object('custom_fields',custom_fields::text),relationship_match FROM matches m WHERE relationship_match<>'' ORDER BY lower(display_name),id LIMIT $6 OFFSET $7`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []CustomerSearchResult{}
	for rows.Next() {
		var b []byte
		var match string
		if err = rows.Scan(&b, &match); err != nil {
			return nil, 0, err
		}
		c := &ent.Customer{}
		if err = json.Unmarshal(b, c); err != nil {
			return nil, 0, err
		}
		if !p.Allows("customer.financial.view", All) {
			redactCustomerFinancial(c)
		}
		result = append(result, CustomerSearchResult{c, match})
	}
	return result, total, rows.Err()
}
