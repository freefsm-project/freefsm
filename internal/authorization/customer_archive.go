package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type ArchiveChild struct {
	Kind    string
	ID      int64
	Name    string
	Primary bool
}
type CustomerArchivePreview struct {
	Snapshot            string
	CustomerID          int64
	Name                string
	Children            []ArchiveChild
	RequiredPermissions []string
	Blockers            []CustomerArchiveBlocker
	missingChildGrant   bool
}

func customerArchivePreview(ctx context.Context, q queryer, p Principal, id int64) (CustomerArchivePreview, error) {
	preview := CustomerArchivePreview{CustomerID: id, Children: []ArchiveChild{}, RequiredPermissions: []string{"customer.archive"}, Blockers: []CustomerArchiveBlocker{}}
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
	if preview.Blockers, err = customerArchiveBlockers(ctx, q, p, id); err != nil {
		return preview, err
	}
	versions := []string{customerVersion}
	for _, kind := range []string{"contact", "location"} {
		table, owner, _, _ := childTable(kind)
		name := "trim(first_name || ' ' || last_name)"
		if kind == "location" {
			name = "title"
		}
		rows, err := q.Query(ctx, `SELECT id,`+name+`,updated_at::text,is_primary FROM `+table+` WHERE `+childWhere(kind, owner)+` AND deleted_at IS NULL ORDER BY id`, id, p.CompanyID)
		if err != nil {
			return preview, err
		}
		hasChildren := false
		for rows.Next() {
			var row ArchiveChild
			var version string
			row.Kind = kind
			if err = rows.Scan(&row.ID, &row.Name, &version, &row.Primary); err != nil {
				rows.Close()
				return preview, err
			}
			if p.Allows(kind+".view", All) {
				preview.Children = append(preview.Children, row)
			}
			hasChildren = true
			versions = append(versions, version)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return preview, err
		}
		if hasChildren {
			preview.RequiredPermissions = append(preview.RequiredPermissions, kind+".archive")
			preview.checkChildGrants(p, kind)
		}
	}
	rows, err := q.Query(ctx, `SELECT id,name,updated_at::text FROM assets WHERE customer_id=$1 AND company_id=$2 AND ownership_class='customer_owned' AND deleted_at IS NULL ORDER BY id`, id, p.CompanyID)
	if err != nil {
		return preview, err
	}
	hasAssets := false
	for rows.Next() {
		var child ArchiveChild
		var version string
		child.Kind = "asset"
		if err = rows.Scan(&child.ID, &child.Name, &version); err != nil {
			rows.Close()
			return preview, err
		}
		hasAssets = true
		if p.Allows("asset.view", All) {
			preview.Children = append(preview.Children, child)
		}
		versions = append(versions, version)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return preview, err
	}
	if hasAssets {
		preview.RequiredPermissions = append(preview.RequiredPermissions, "asset.archive")
		preview.checkChildGrants(p, "asset")
	}
	if len(preview.Blockers) != 0 {
		return preview, nil
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
		if preview.missingChildGrant {
			return ErrForbidden
		}
		if len(preview.Blockers) != 0 {
			return ErrConflict
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
			if err = auditLifecycle(ctx, tx, a, p, child.Kind, child.ID, "archive", before, map[string]any{"customer_cascade": id, "primary_cleared": child.Primary}); err != nil {
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
