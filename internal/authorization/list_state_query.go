package authorization

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CheckCollectionListQuery applies the same domain authority checks used by an
// atomic preference commit to a dedicated read. Parsing remains route-owned.
func (s *Service) CheckCollectionListQuery(ctx context.Context, actor Actor, key string, values url.Values) error {
	return s.withinListState(ctx, actor, key, func(tx pgx.Tx, p Principal) error {
		return ValidateCollectionListQuery(ctx, tx, p, key, values)
	})
}

// ValidateCollectionListQuery is transaction-bound: it neither opens another
// transaction nor trusts navigation's cached principal. Its caller supplies
// strictly parsed route values. Domain-owned predicates are shared with reads.
func ValidateCollectionListQuery(ctx context.Context, tx pgx.Tx, p Principal, key string, values url.Values) error {
	if !listStateKey(key) || key == "search" {
		return fmt.Errorf("%w: unknown collection", ErrConflict)
	}
	ids := map[string]int64{}
	for _, field := range []string{"customer_id", "status_id", "asset_type_id", "asset_status_id", "customer", "asset", "premises"} {
		raw := values.Get(field)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 0 {
			return fmt.Errorf("%w: invalid relationship filter", ErrConflict)
		}
		ids[field] = id
	}
	kind := strings.TrimSuffix(key, "s")
	search := values.Get("search")
	if !values.Has("search") {
		search = values.Get("q")
	}
	if key == "leases" {
		if values.Get("facet") == "base" && !p.Allows("lease.view", All) {
			return ErrForbidden
		}
		if values.Get("facet") == "billing" && !p.Allows("lease.billing.view", All) {
			return ErrForbidden
		}
		workspace := LeaseWorkspaceQuery{
			Lifecycle: values.Get("lifecycle"), Queue: values.Get("queue"),
			Customer: ids["customer"], Asset: ids["asset"], Premises: ids["premises"],
			EndingFrom: values.Get("ending_from"), EndingThrough: values.Get("ending_through"),
			Placement: values.Get("placement"), Billing: values.Get("billing"),
		}
		if err := workspace.validate(values.Get("facet") == "billing" || !p.Allows("lease.view", All), p.Allows("lease.billing.view", All)); err != nil {
			return err
		}
		return ValidateDiscoveryQuery(ctx, tx, p, DiscoveryQuery{Types: []string{"lease"}, State: values.Get("state"), Lease: workspace})
	}
	if key == "jobs" || key == "estimates" || key == "invoices" {
		scope := Scope(values.Get("scope"))
		if scope == "" {
			scope = Assigned
			if p.Allows(kind+".view", All) {
				scope = All
			}
		}
		if key == "jobs" {
			return CheckJobOperationalFilter(ctx, tx, p, JobOperationalFilter{
				Search: search, StatusID: ids["status_id"], CustomerID: ids["customer_id"], Sort: values.Get("sort"),
			}, scope)
		}
		return AuthorizeDocumentListFilter(ctx, tx, p, kind, DocumentListFilter{
			Search: search, StatusID: ids["status_id"], CustomerID: ids["customer_id"],
			Sort: values.Get("sort"), Valid: values.Get("valid"),
		}, scope, false)
	}
	if !p.Allows(kind+".view", All) {
		// Preserve the existing child read boundary's non-disclosure contract.
		if key == "contacts" || key == "locations" {
			return ErrNotFound
		}
		return ErrForbidden
	}
	if values.Get("archived") == "1" && !p.Allows(kind+".archived.view", All) {
		return ErrForbidden
	}
	if key == "assets" {
		if customer := ids["customer_id"]; customer != 0 {
			if !p.Allows("customer.view", All) {
				return ErrForbidden
			}
			var visible bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customers WHERE id=$1 AND company_id=$2 AND (deleted_at IS NULL OR $3))`,
				customer, p.CompanyID, p.Allows("customer.archived.view", All)).Scan(&visible); err != nil {
				return err
			}
			if !visible {
				return ErrForbidden
			}
		}
		for _, vocabulary := range []struct{ field, table string }{{"asset_type_id", "asset_types"}, {"asset_status_id", "asset_statuses"}} {
			if id := ids[vocabulary.field]; id != 0 {
				var visible bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+vocabulary.table+` WHERE id=$1 AND (company_id=$2 OR company_id IS NULL))`, id, p.CompanyID).Scan(&visible); err != nil {
					return err
				}
				if !visible {
					return ErrForbidden
				}
			}
		}
	}
	if key == "projects" && ids["status_id"] != 0 {
		var visible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.id=$1 AND w.company_id=$2 AND w.object_type='project')`, ids["status_id"], p.CompanyID).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return ErrForbidden
		}
	}
	return nil
}
