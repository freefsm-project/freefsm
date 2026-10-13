package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (s *Service) ProjectLegacyReview(ctx context.Context, a Actor, id int64) ([]string, error) {
	if err := s.ParentAccess(ctx, a, "project", id, ""); err != nil {
		return nil, err
	}
	var reasons []string
	err := s.Pool.QueryRow(ctx, `SELECT review_reasons FROM project_legacy_review WHERE project_id=$1`, id).Scan(&reasons)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return reasons, err
}

// ProjectInputs runs inside the details transaction, after its authority fence.
// Existing ownership is an invariant, not an implicit Customer View grant.
func (s *Service) ProjectInputs(ctx context.Context, tx pgx.Tx, a Actor, id, customerID, locationID int64, selectingLocation bool) error {
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return err
	}
	if id == 0 && !p.Allows("customer.view", All) {
		return ErrNotFound
	}
	active, err := customerLifecycleState(ctx, tx, p, customerID, true)
	if err != nil {
		return err
	}
	if !active {
		return ErrConflict
	}
	if locationID == 0 {
		return nil
	}
	if selectingLocation && !p.Allows("location.view", All) {
		return ErrNotFound
	}
	var locationActive bool
	err = tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM locations WHERE id=$1 AND company_id=$2 AND object_type='customer' AND object_id=$3 FOR UPDATE`, locationID, p.CompanyID, customerID).Scan(&locationActive)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !locationActive && selectingLocation {
		return ErrConflict
	}
	return nil
}

// ProjectStatusDependencies preserves historical Location links while finished;
// reopening requires an explicit repair before selecting an unfinished status.
func ProjectStatusDependencies(ctx context.Context, tx pgx.Tx, companyID, id int64, category string) error {
	var active, locationActive bool
	err := tx.QueryRow(ctx, `SELECT c.deleted_at IS NULL,
 p.location_id IS NULL OR EXISTS(SELECT 1 FROM locations l WHERE l.id=p.location_id AND l.company_id=p.company_id AND l.object_type='customer' AND l.object_id=p.customer_id AND l.deleted_at IS NULL)
 FROM projects p JOIN customers c ON c.id=p.customer_id AND c.company_id=p.company_id WHERE p.id=$1 AND p.company_id=$2 FOR UPDATE OF c`, id, companyID).Scan(&active, &locationActive)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !active || category != "project:completed" && category != "project:canceled" && !locationActive {
		return ErrConflict
	}
	return nil
}

func (s *Service) ProjectLifecycle(ctx context.Context, a Actor, id int64, restore bool) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := parentAccess(ctx, tx, p, "project", id, "", true); err != nil {
			return err
		}
		permission := "project.archive"
		if restore {
			permission = "project.restore"
		}
		if !p.Allows(permission, All) {
			return ErrForbidden
		}
		var customerID int64
		var archived bool
		var category string
		if err := tx.QueryRow(ctx, `SELECT p.customer_id,p.deleted_at IS NOT NULL,coalesce(s.category_key,'') FROM projects p LEFT JOIN statuses s ON s.id=p.status_id WHERE p.id=$1`, id).Scan(&customerID, &archived, &category); err != nil {
			return err
		}
		if restore {
			active, err := customerLifecycleState(ctx, tx, p, customerID, true)
			if err != nil {
				return err
			}
			if !active {
				return ErrConflict
			}
		} else if category != "project:completed" && category != "project:canceled" {
			return ErrConflict
		}
		if archived == !restore {
			return nil
		}
		value := "clock_timestamp()"
		if restore {
			value = "NULL"
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET deleted_at=`+value+`,updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, permission, All, "project", id, map[string]any{"category": category})
	})
}
