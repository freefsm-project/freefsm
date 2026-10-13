package exports

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

type CustomerOptions struct {
	Search   string `json:"search"`
	Sort     string `json:"sort"`
	Archived bool   `json:"archived"`
}
type Customers struct{}

func (Customers) Kind() string               { return "customer-operational-v1" }
func (Customers) Permission() string         { return "customer.export" }
func (Customers) Scope() authorization.Scope { return authorization.All }
func (Customers) Columns() []string {
	return []string{"customer_id", "display_name", "kind", "first_name", "last_name", "organization_name", "email", "phone", "service_notes", "archived"}
}
func (Customers) Validate(raw json.RawMessage) error {
	var o CustomerOptions
	if json.Unmarshal(raw, &o) != nil {
		return ErrConflict
	}
	if o.Sort != "name" && o.Sort != "name_desc" && o.Sort != "updated" {
		return ErrConflict
	}
	return nil
}
func (Customers) Authorize(p authorization.Principal, raw json.RawMessage) error {
	var o CustomerOptions
	if err := json.Unmarshal(raw, &o); err != nil {
		return err
	}
	if !p.Allows("customer.view", authorization.All) || o.Archived && !p.Allows("customer.archived.view", authorization.All) {
		return authorization.ErrForbidden
	}
	return nil
}

type customerReference struct {
	ID            int64 `json:"id"`
	Archived      bool  `json:"archived"`
	ContactMatch  bool  `json:"contact_match"`
	LocationMatch bool  `json:"location_match"`
}

func (Customers) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	var o CustomerOptions
	if err := json.Unmarshal(raw, &o); err != nil {
		return err
	}
	order := "lower(display_name),id"
	if o.Sort == "name_desc" {
		order = "lower(display_name) DESC,id DESC"
	}
	if o.Sort == "updated" {
		order = "updated_at DESC,id DESC"
	}
	// One PostgreSQL cursor owns one MVCC snapshot across every fetch. Totals are
	// derived from exactly these rows, never a separately timed COUNT query.
	_, err := tx.Exec(ctx, `DECLARE customer_export_snapshot NO SCROLL CURSOR FOR
 WITH matches AS (SELECT c.*,
 ($3='' OR strpos(lower(concat_ws(' ',c.id::text,c.display_name,c.first_name,c.last_name,c.company_name,c.email,c.phone)),lower($3))>0) AS direct_match,
 ($4 AND EXISTS(SELECT 1 FROM customer_contacts x WHERE x.customer_id=c.id AND x.company_id=c.company_id AND x.deleted_at IS NULL AND strpos(lower(concat_ws(' ',x.first_name,x.last_name,x.email,x.phone)),lower($3))>0)) AS contact_match,
 ($5 AND EXISTS(SELECT 1 FROM locations x WHERE x.object_type='customer' AND x.object_id=c.id AND x.company_id=c.company_id AND x.deleted_at IS NULL AND strpos(lower(concat_ws(' ',x.title,x.address_1,x.address_2,x.city,x.state,x.zip_code)),lower($3))>0)) AS location_match
 FROM customers c WHERE c.company_id=$1 AND ($2 OR c.deleted_at IS NULL))
 SELECT id,display_name,CASE account_type WHEN 'company' THEN 'Organization' ELSE 'Individual' END,first_name,last_name,company_name,email,phone,service_notes,deleted_at IS NOT NULL,NOT direct_match AND contact_match,NOT direct_match AND NOT contact_match AND location_match
 FROM matches WHERE direct_match OR contact_match OR location_match ORDER BY `+order, p.CompanyID, o.Archived, o.Search, p.Allows("contact.view", authorization.All), p.Allows("location.view", authorization.All))
	if err != nil {
		return err
	}
	for {
		rows, err := tx.Query(ctx, `FETCH FORWARD 128 FROM customer_export_snapshot`)
		if err != nil {
			return err
		}
		batch := []Record{}
		for rows.Next() {
			var ref customerReference
			v := make([]string, 10)
			if err := rows.Scan(&ref.ID, &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7], &v[8], &ref.Archived, &ref.ContactMatch, &ref.LocationMatch); err != nil {
				rows.Close()
				return err
			}
			v[0] = fmt.Sprint(ref.ID)
			v[9] = fmt.Sprint(ref.Archived)
			reference, _ := json.Marshal(ref)
			batch = append(batch, Record{Values: v, Reference: reference})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, record := range batch {
			if err := emit(record); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(ctx, `CLOSE customer_export_snapshot`)
	return err
}
func (Customers) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	var ref customerReference
	if err := json.Unmarshal(raw, &ref); err != nil {
		return err
	}
	if !p.Allows("customer.view", authorization.All) || ref.Archived && !p.Allows("customer.archived.view", authorization.All) || ref.ContactMatch && !p.Allows("contact.view", authorization.All) || ref.LocationMatch && !p.Allows("location.view", authorization.All) {
		return authorization.ErrForbidden
	}
	var visible bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customers WHERE id=$1 AND company_id=$2 AND (deleted_at IS NULL OR $3))`, ref.ID, p.CompanyID, p.Allows("customer.archived.view", authorization.All)).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return authorization.ErrForbidden
	}
	return nil
}
