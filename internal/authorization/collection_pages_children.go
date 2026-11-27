package authorization

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type CustomerChildPageFilter struct {
	Search, Sort  string
	Archived      bool
	Page, PerPage int
}

type CustomerChildrenPage struct {
	Rows  []CustomerChild
	Total int
}

// CustomerChildrenPage is the full master collection page, retaining current
// parent-identity redaction and edit eligibility. Related lists/pickers continue
// using CustomerChildren and are not silently changed by this entry point.
func (s *Service) CustomerChildrenPage(ctx context.Context, a Actor, kind string, f CustomerChildPageFilter) (CustomerChildrenPage, error) {
	var out CustomerChildrenPage
	table, owner, _, err := childTable(kind)
	if err != nil {
		return out, err
	}
	limit, offset, err := collectionPageBounds(f.Page, f.PerPage)
	if err != nil {
		return out, err
	}
	if f.Sort == "" {
		f.Sort = "name"
	}
	if f.Sort != "name" && f.Sort != "name_desc" {
		return out, ErrConflict
	}
	if a.Channel == "api" {
		return out, ErrForbidden
	}
	err = s.WithinSnapshotPermission(ctx, a, kind+".view", All, func(tx pgx.Tx, p Principal) error {
		if f.Archived && !p.Allows(kind+".archived.view", All) {
			return ErrForbidden
		}
		name := "trim(c.first_name||' '||c.last_name)"
		search := "concat_ws(' ',trim(c.first_name||' '||c.last_name),c.email,c.phone)"
		where := " WHERE c.company_id=$1 AND ($2 OR c.deleted_at IS NULL)"
		if kind == "location" {
			name = "c.title"
			search = "concat_ws(' ',c.title,c.address_1,c.address_2,c.city,c.state,c.zip_code)"
			where += " AND c.object_type='customer'"
		}
		// Contact/Location previously used Go Unicode lowercase. Choose the same
		// installed Unicode SQL collation as discovery rather than regress on C.
		var collation string
		if err := tx.QueryRow(ctx, `SELECT collname FROM pg_catalog.pg_collation WHERE collnamespace='pg_catalog'::regnamespace
 AND collname IN ('pg_c_utf8','C.utf8','C.UTF-8','und-x-icu')
 ORDER BY CASE collname WHEN 'pg_c_utf8' THEN 0 WHEN 'C.utf8' THEN 1 WHEN 'C.UTF-8' THEN 2 ELSE 3 END LIMIT 1`).Scan(&collation); err != nil {
			return err
		}
		fold := func(expr string) string { return `lower((` + expr + `)::text COLLATE pg_catalog."` + collation + `")` }
		where += " AND ($3='' OR strpos(" + fold(search) + "," + fold("$3") + ")>0)"
		order := fold(name)
		if f.Sort == "name_desc" {
			order += " DESC"
		}
		order += ",c.id"
		args := []any{p.CompanyID, f.Archived, f.Search}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` c`+where, args...).Scan(&out.Total); err != nil {
			return err
		}
		args = append(args, limit, offset)
		rows, err := tx.Query(ctx, `/* collection-page:child */ WITH page AS MATERIALIZED (
 SELECT c.id FROM `+table+` c`+where+` ORDER BY `+order+` LIMIT $4 OFFSET $5
 ) SELECT to_jsonb(c)||jsonb_build_object('customer_id',c.`+owner+`),
 EXISTS(SELECT 1 FROM customers p WHERE p.id=c.`+owner+` AND p.company_id=c.company_id AND p.deleted_at IS NULL)
 FROM page JOIN `+table+` c ON c.id=page.id ORDER BY `+order, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Rows = []CustomerChild{}
		for rows.Next() {
			var raw []byte
			var c CustomerChild
			var active bool
			if err := rows.Scan(&raw, &active); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &c); err != nil {
				return err
			}
			c.CanMutate = active
			if !p.Allows("customer.view", All) || !active && !p.Allows("customer.archived.view", All) {
				c.CustomerID = 0
			}
			out.Rows = append(out.Rows, c)
		}
		return rows.Err()
	})
	if err != nil {
		return CustomerChildrenPage{}, err
	}
	return out, nil
}
