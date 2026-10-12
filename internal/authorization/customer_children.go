package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/jackc/pgx/v5"
)

// CustomerChild is a master representation, never a Job Context projection.
type CustomerChild struct {
	ID           int64      `json:"id"`
	CustomerID   int64      `json:"customer_id,omitempty"`
	CanMutate    bool       `json:"-"`
	FirstName    string     `json:"first_name"`
	LastName     string     `json:"last_name"`
	Email        string     `json:"email"`
	Phone        string     `json:"phone"`
	RoleTitle    string     `json:"role_title"`
	Title        string     `json:"title"`
	Address1     string     `json:"address_1"`
	Address2     string     `json:"address_2"`
	City         string     `json:"city"`
	State        string     `json:"state"`
	ZipCode      string     `json:"zip_code"`
	Notes        string     `json:"notes"`
	ServiceNotes string     `json:"service_notes"`
	AccessNotes  string     `json:"access_notes"`
	IsPrimary    bool       `json:"is_primary"`
	DeletedAt    *time.Time `json:"deleted_at"`
}

func (c CustomerChild) Name() string {
	if c.Title != "" {
		return c.Title
	}
	return strings.TrimSpace(c.FirstName + " " + c.LastName)
}

func childTable(kind string) (table, owner, order string, err error) {
	switch kind {
	case "contact":
		return "customer_contacts", "customer_id", "lower(first_name || ' ' || last_name),id", nil
	case "location":
		return "locations", "object_id", "lower(title),id", nil
	default:
		return "", "", "", ErrNotFound
	}
}

func childCustomer(ctx context.Context, q queryer, p Principal, customerID int64, lock bool) (bool, error) {
	if !p.Allows("customer.view", All) {
		return false, ErrNotFound
	}
	active, err := customerLifecycleState(ctx, q, p, customerID, lock)
	if err != nil {
		return false, err
	}
	if !active && !p.Allows("customer.archived.view", All) {
		return false, ErrNotFound
	}
	return active, nil
}

// A lifecycle invariant is not permission to disclose a Customer master. Only
// use this after authorizing the child or an independently selected Customer.
func customerLifecycleState(ctx context.Context, q queryer, p Principal, customerID int64, lock bool) (bool, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var active bool
	err := q.QueryRow(ctx, `SELECT deleted_at IS NULL FROM customers WHERE id=$1 AND company_id=$2`+suffix, customerID, p.CompanyID).Scan(&active)
	if err == pgx.ErrNoRows {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	return active, nil
}

func childCommandOwner(ctx context.Context, q queryer, p Principal, kind string, customerID, id int64) (int64, error) {
	table, owner, _, err := childTable(kind)
	if err != nil {
		return 0, err
	}
	if !p.Allows(kind+".view", All) {
		return 0, ErrNotFound
	}
	var actual int64
	var archived bool
	where := "id=$1 AND company_id=$2"
	if kind == "location" {
		where += " AND object_type='customer'"
	}
	err = q.QueryRow(ctx, `SELECT `+owner+`,deleted_at IS NOT NULL FROM `+table+` WHERE `+where, id, p.CompanyID).Scan(&actual, &archived)
	if err == pgx.ErrNoRows {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if customerID != 0 && customerID != actual || archived && !p.Allows(kind+".archived.view", All) {
		return 0, ErrNotFound
	}
	return actual, nil
}

func childWhere(kind, owner string) string {
	where := owner + "=$1 AND company_id=$2"
	if kind == "location" {
		where += " AND object_type='customer'"
	}
	return where
}

func (s *Service) CustomerActive(ctx context.Context, a Actor, id int64) (bool, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return false, err
	}
	return childCustomer(ctx, s.Pool, p, id, false)
}

func (s *Service) CustomerChildren(ctx context.Context, a Actor, customerID int64, kind string, archived bool) ([]CustomerChild, error) {
	return s.customerChildren(ctx, a, customerID, kind, archived, 0)
}

func (s *Service) CustomerChild(ctx context.Context, a Actor, customerID, id int64, kind string) (CustomerChild, error) {
	rows, err := s.customerChildren(ctx, a, customerID, kind, false, id)
	if err != nil {
		return CustomerChild{}, err
	}
	if len(rows) != 1 {
		return CustomerChild{}, ErrNotFound
	}
	return rows[0], nil
}

func (s *Service) customerChildren(ctx context.Context, a Actor, customerID int64, kind string, archived bool, id int64) ([]CustomerChild, error) {
	table, owner, order, err := childTable(kind)
	if err != nil {
		return nil, err
	}
	p, err := s.Principal(ctx, a)
	if err != nil {
		return nil, err
	}
	if !p.Allows(kind+".view", All) {
		return nil, ErrNotFound
	}
	where := "($1::bigint=0 OR " + owner + "=$1) AND company_id=$2"
	if kind == "location" {
		where += " AND object_type='customer'"
	}
	if id != 0 {
		where += " AND id=$3"
		archived = p.Allows(kind+".archived.view", All)
	}
	if archived {
		if !p.Allows(kind+".archived.view", All) {
			return nil, ErrForbidden
		}
	} else {
		where += " AND deleted_at IS NULL"
	}
	args := []any{customerID, p.CompanyID}
	if id != 0 {
		args = append(args, id)
	}
	rows, err := s.Pool.Query(ctx, `SELECT to_jsonb(c)||jsonb_build_object('customer_id',`+owner+`),EXISTS(SELECT 1 FROM customers p WHERE p.id=c.`+owner+` AND p.company_id=c.company_id AND p.deleted_at IS NULL) FROM `+table+` c WHERE `+where+` ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []CustomerChild{}
	for rows.Next() {
		var b []byte
		var c CustomerChild
		var active bool
		if err = rows.Scan(&b, &active); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		c.CanMutate = active
		if !p.Allows("customer.view", All) || !active && !p.Allows("customer.archived.view", All) {
			c.CustomerID = 0
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// SaveCustomerChild accepts a patch of explicit fields. Ownership is part of the
// command's address, not editable input. Parent locking serializes primary and
// lifecycle changes; the authorization transaction serializes grant revocation.
func (s *Service) SaveCustomerChild(ctx context.Context, a Actor, customerID, id int64, kind string, fields map[string]string, primary *bool) (int64, error) {
	table, owner, _, err := childTable(kind)
	if err != nil {
		return 0, err
	}
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows(kind+".view", All) {
			return ErrNotFound
		}
		var active bool
		if id == 0 {
			active, err = childCustomer(ctx, tx, p, customerID, true)
		} else {
			customerID, err = childCommandOwner(ctx, tx, p, kind, customerID, id)
			if err == nil {
				active, err = customerLifecycleState(ctx, tx, p, customerID, true)
			}
		}
		if err != nil {
			return err
		}
		if !active {
			return ErrForbidden
		}
		action := kind + ".edit"
		if id == 0 {
			action = kind + ".create"
		}
		if !p.Allows(action, All) {
			return ErrForbidden
		}
		var before CustomerChild
		if id != 0 {
			var b []byte
			err = tx.QueryRow(ctx, `SELECT to_jsonb(c) FROM `+table+` c WHERE `+childWhere(kind, owner)+` AND id=$3 FOR UPDATE`, customerID, p.CompanyID, id).Scan(&b)
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if err = json.Unmarshal(b, &before); err != nil {
				return err
			}
			if before.DeletedAt != nil {
				if !p.Allows(kind+".archived.view", All) {
					return ErrNotFound
				}
				return ErrForbidden
			}
		}
		allowed := map[string]bool{"notes": true, "service_notes": true}
		if kind == "contact" {
			for _, key := range []string{"first_name", "last_name", "email", "phone", "role_title"} {
				allowed[key] = true
			}
		} else {
			for _, key := range []string{"title", "address_1", "address_2", "city", "state", "zip_code", "access_notes"} {
				allowed[key] = true
			}
		}
		for key := range fields {
			if !allowed[key] {
				return fmt.Errorf("invalid master field")
			}
		}
		value := func(key, old string) string {
			if v, ok := fields[key]; ok {
				return v
			}
			return old
		}
		if kind == "contact" && strings.TrimSpace(value("first_name", before.FirstName)+" "+value("last_name", before.LastName)) == "" {
			return fmt.Errorf("Contact name is required")
		}
		if kind == "location" && strings.TrimSpace(value("title", before.Title)) == "" {
			return fmt.Errorf("Location title is required")
		}
		if primary != nil && *primary {
			if _, err = tx.Exec(ctx, `UPDATE `+table+` SET is_primary=false WHERE `+childWhere(kind, owner)+` AND is_primary`, customerID, p.CompanyID); err != nil {
				return err
			}
		}
		if id == 0 {
			query := `INSERT INTO ` + table + ` (` + owner + `,company_id) VALUES($1,$2) RETURNING id`
			if kind == "location" {
				query = `INSERT INTO locations(object_id,company_id,object_type,title) VALUES($1,$2,'customer',$3) RETURNING id`
				err = tx.QueryRow(ctx, query, customerID, p.CompanyID, fields["title"]).Scan(&id)
			} else {
				err = tx.QueryRow(ctx, query, customerID, p.CompanyID).Scan(&id)
			}
			if err != nil {
				return err
			}
		}
		args := []any{id}
		sets := []string{"updated_at=clock_timestamp()"}
		for key, value := range fields {
			args = append(args, value)
			sets = append(sets, fmt.Sprintf("%s=$%d", key, len(args)))
		}
		if primary != nil {
			args = append(args, *primary)
			sets = append(sets, fmt.Sprintf("is_primary=$%d", len(args)))
		}
		if _, err = tx.Exec(ctx, `UPDATE `+table+` SET `+strings.Join(sets, ",")+` WHERE id=$1`, args...); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, action, All, kind, id, map[string]any{"before": before, "fields": fields, "primary": primary, "customer_id": customerID})
	})
	return id, err
}

func (s *Service) CustomerChildLifecycle(ctx context.Context, a Actor, customerID, id int64, kind string, restore bool) error {
	table, owner, _, err := childTable(kind)
	if err != nil {
		return err
	}
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows(kind+".view", All) {
			return ErrNotFound
		}
		customerID, err = childCommandOwner(ctx, tx, p, kind, customerID, id)
		if err != nil {
			return err
		}
		active, err := customerLifecycleState(ctx, tx, p, customerID, true)
		if err != nil {
			return err
		}
		if !active {
			return ErrForbidden
		}
		action := kind + ".archive"
		if restore {
			action = kind + ".restore"
		}
		var archived bool
		err = tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM `+table+` WHERE `+childWhere(kind, owner)+` AND id=$3 FOR UPDATE`, customerID, p.CompanyID, id).Scan(&archived)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if archived && !p.Allows(kind+".archived.view", All) {
			return ErrNotFound
		}
		if !p.Allows(action, All) {
			return ErrForbidden
		}
		if archived == !restore {
			return saveaction.ConfirmNoChange(ctx, tx)
		}
		if !restore {
			column := "customer_contact_id"
			if kind == "location" {
				column = "location_id"
			}
			var blocked bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs j LEFT JOIN statuses s ON s.id=j.status_id WHERE j.`+column+`=$1 AND j.deleted_at IS NULL AND coalesce(s.category_key,'') NOT IN ('job:completed','job:canceled'))`, id).Scan(&blocked); err != nil {
				return err
			}
			if kind == "location" && !blocked {
				err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects p LEFT JOIN statuses s ON s.id=p.status_id WHERE p.location_id=$1 AND p.deleted_at IS NULL AND coalesce(s.category_key,'') NOT IN ('project:completed','project:canceled'))`, id).Scan(&blocked)
				if err != nil {
					return err
				}
			}
			if blocked {
				return fmt.Errorf("unfinished work blocks archive")
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE `+table+` SET deleted_at=CASE WHEN $2 THEN NULL ELSE clock_timestamp() END,is_primary=false,updated_at=clock_timestamp() WHERE id=$1`, id, restore); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, action, All, kind, id, map[string]any{"customer_id": customerID})
	})
}
