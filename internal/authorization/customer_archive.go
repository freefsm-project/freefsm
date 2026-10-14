package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type ArchiveChild struct {
	Kind string
	ID   int64
	Name string
}
type CustomerArchivePreview struct {
	Snapshot   string
	CustomerID int64
	Name       string
	Children   []ArchiveChild
}

func customerArchivePreview(ctx context.Context, q queryer, p Principal, id int64) (CustomerArchivePreview, error) {
	preview := CustomerArchivePreview{CustomerID: id, Children: []ArchiveChild{}}
	active, err := childCustomer(ctx, q, p, id, false)
	if err != nil {
		return preview, err
	}
	if !active {
		return preview, ErrForbidden
	}
	if !p.Allows("customer.archive", All) {
		return preview, ErrForbidden
	}
	var customerVersion string
	if err = q.QueryRow(ctx, `SELECT display_name,updated_at::text FROM customers WHERE id=$1`, id).Scan(&preview.Name, &customerVersion); err != nil {
		return preview, err
	}
	var blocked bool
	if err = q.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM jobs WHERE customer_id=$1 AND deleted_at IS NULL)
 OR EXISTS(SELECT 1 FROM projects WHERE customer_id=$1 AND deleted_at IS NULL)
 OR EXISTS(SELECT 1 FROM estimates WHERE customer_id=$1 AND deleted_at IS NULL AND conversion_hidden_at IS NULL)
 OR EXISTS(SELECT 1 FROM invoices i LEFT JOIN statuses s ON s.id=i.status_id WHERE i.customer_id=$1 AND i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL AND (coalesce(s.category_key,'invoice:draft')='invoice:draft' OR (coalesce(s.category_key,'')<>'invoice:void' AND i.settlement_state<>'paid')))
 OR EXISTS(SELECT 1 FROM assets WHERE customer_id=$1 AND ownership_class='company_owned')
 OR EXISTS(SELECT 1 FROM customer_credits c WHERE c.customer_id=$1 AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=c.source_payment_id) AND c.original_amount_cents
 -coalesce((SELECT sum(a.amount_cents) FROM credit_applications a WHERE a.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)),0)
 -coalesce((SELECT sum(a.amount_cents) FROM credit_refund_allocations a WHERE a.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_refund' AND r.operation_id=a.refund_id)),0)>0)`, id).Scan(&blocked); err != nil {
		return preview, err
	}
	if blocked {
		return preview, fmt.Errorf("active dependencies block Customer archive")
	}
	versions := []string{customerVersion}
	for _, kind := range []string{"contact", "location"} {
		table, owner, _, _ := childTable(kind)
		name := "trim(first_name || ' ' || last_name)"
		if kind == "location" {
			name = "title"
		}
		rows, err := q.Query(ctx, `SELECT id,`+name+`,updated_at::text FROM `+table+` WHERE `+childWhere(kind, owner)+` AND deleted_at IS NULL ORDER BY id`, id, p.CompanyID)
		if err != nil {
			return preview, err
		}
		for rows.Next() {
			var row ArchiveChild
			var version string
			row.Kind = kind
			if err = rows.Scan(&row.ID, &row.Name, &version); err != nil {
				rows.Close()
				return preview, err
			}
			if !p.Allows(kind+".view", All) || !p.Allows(kind+".archive", All) {
				rows.Close()
				return preview, ErrForbidden
			}
			preview.Children = append(preview.Children, row)
			versions = append(versions, version)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return preview, err
		}
	}
	rows, err := q.Query(ctx, `SELECT id,name,updated_at::text FROM assets WHERE customer_id=$1 AND company_id=$2 AND ownership_class='customer_owned' AND deleted_at IS NULL ORDER BY id`, id, p.CompanyID)
	if err != nil {
		return preview, err
	}
	for rows.Next() {
		var child ArchiveChild
		var version string
		child.Kind = "asset"
		if err = rows.Scan(&child.ID, &child.Name, &version); err != nil {
			rows.Close()
			return preview, err
		}
		if !p.Allows("asset.view", All) || !p.Allows("asset.archive", All) {
			rows.Close()
			return preview, ErrForbidden
		}
		preview.Children = append(preview.Children, child)
		versions = append(versions, version)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return preview, err
	}
	b, err := json.Marshal([]any{preview, versions})
	if err != nil {
		return preview, err
	}
	sum := sha256.Sum256(b)
	preview.Snapshot = hex.EncodeToString(sum[:])
	return preview, nil
}

func (s *Service) PreviewCustomerArchive(ctx context.Context, a Actor, id int64) (CustomerArchivePreview, error) {
	var preview CustomerArchivePreview
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var err error
		preview, err = customerArchivePreview(ctx, tx, p, id)
		return err
	})
	return preview, err
}

func (s *Service) ArchiveCustomer(ctx context.Context, a Actor, id int64, snapshot string) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := childCustomer(ctx, tx, p, id, true); err != nil {
			return err
		}
		preview, err := customerArchivePreview(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if snapshot == "" || snapshot != preview.Snapshot {
			return ErrConflict
		}
		for _, child := range preview.Children {
			before, err := lifecycleSnapshot(ctx, tx, p, child.Kind, child.ID)
			if err != nil {
				return err
			}
			table, _, _, _ := childTable(child.Kind)
			primary := ",is_primary=false"
			if child.Kind == "asset" {
				table = "assets"
				primary = ""
			}
			if _, err = tx.Exec(ctx, `UPDATE `+table+` SET deleted_at=clock_timestamp()`+primary+`,updated_at=clock_timestamp() WHERE id=$1`, child.ID); err != nil {
				return err
			}
			if err = auditLifecycle(ctx, tx, a, p, child.Kind, child.ID, "archive", before, map[string]any{"customer_cascade": id, "primary_cleared": child.Kind != "asset"}); err != nil {
				return err
			}
		}
		before, err := lifecycleSnapshot(ctx, tx, p, "customer", id)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		return auditLifecycle(ctx, tx, a, p, "customer", id, "archive", before, map[string]any{"preview": preview})
	})
}
