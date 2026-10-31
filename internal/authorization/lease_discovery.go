package authorization

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type LeaseListRow struct {
	Lifecycle     string
	ID            int64
	Number, Title string
	Archived      bool
}
type LeaseList struct {
	Rows                                  []LeaseListRow
	Billing, CanRegister, CanViewArchived bool
	Page, PerPage, Total                  int
	Search, State                         string
}

func (s *Service) LeaseList(ctx context.Context, a Actor, billing bool, search, state string, page, size int) (LeaseList, error) {
	out := LeaseList{Search: search, State: state, Page: page, PerPage: size}
	if out.Page < 1 {
		out.Page = 1
	}
	if out.Page > 1000000 {
		return out, ErrConflict
	}
	if size != 25 && size != 50 && size != 100 {
		out.PerPage = 25
	}
	if state != "archived" && state != "all" {
		out.State = "active"
	}
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		out.Billing = billing || !p.Allows("lease.view", All)
		permission := "lease.view"
		if out.Billing {
			permission = "lease.billing.view"
		}
		if a.Channel == "api" || !p.Allows(permission, All) {
			return ErrForbidden
		}
		out.CanViewArchived = p.Allows("lease.archived.view", All)
		if out.State != "active" && !out.CanViewArchived {
			return ErrForbidden
		}
		out.CanRegister = !out.Billing && p.Allows("lease.manage", All) && p.Allows("customer.view", All)
		title, searchExpr := "title", "number_label||' '||title||' '||reference"
		if out.Billing {
			title, searchExpr = "''::text", "number_label"
		}
		where := ` FROM leases l WHERE company_id=$1 AND ($2='all' OR ($2='archived')=(deleted_at IS NOT NULL)) AND strpos(lower(` + searchExpr + `),lower($3))>0`
		if err := tx.QueryRow(ctx, `SELECT count(*)`+where, p.CompanyID, out.State, search).Scan(&out.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,number_label,`+title+`,deleted_at IS NOT NULL,`+leaseNoticeCalendarColumns+where+` ORDER BY document_number,id LIMIT $4 OFFSET $5`, p.CompanyID, out.State, search, out.PerPage, (out.Page-1)*out.PerPage)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row LeaseListRow
			var state, start, end string
			var today time.Time
			if err := rows.Scan(&row.ID, &row.Number, &row.Title, &row.Archived, &state, &start, &end, &today); err != nil {
				return err
			}
			calendar, err := leaseCalendar(state, start, end, today)
			if err != nil {
				return err
			}
			row.Lifecycle = calendar.Lifecycle
			out.Rows = append(out.Rows, row)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) LeaseRegistrationCustomers(ctx context.Context, a Actor) ([]LeaseChoice, error) {
	var out []LeaseChoice
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.view", All) || !p.Allows("lease.manage", All) || !p.Allows("customer.view", All) {
			return ErrForbidden
		}
		rows, err := tx.Query(ctx, `SELECT id,display_name FROM customers WHERE company_id=$1 AND deleted_at IS NULL ORDER BY display_name,id`, p.CompanyID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c LeaseChoice
			if err := rows.Scan(&c.ID, &c.Label); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

type LeaseHistoryEvent struct {
	Action           string
	At               time.Time
	ActorID          int64
	Before, After    string
	AgreementChanged bool
}
type LeaseHistory struct {
	Lifecycle string
	ID        int64
	Number    string
	Billing   bool
	Events    []LeaseHistoryEvent
}

// History is an allowlisted projection of captured immutable events. It neither
// reconstructs missing history nor exposes unrelated protected audit metadata.
func (s *Service) LeaseHistory(ctx context.Context, a Actor, id int64, billing bool) (LeaseHistory, error) {
	out := LeaseHistory{ID: id, Billing: billing}
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		permission := "lease.view"
		if billing {
			permission = "lease.billing.view"
		}
		if a.Channel == "api" || !p.Allows(permission, All) {
			return ErrNotFound
		}
		var archived bool
		err := tx.QueryRow(ctx, `SELECT number_label,deleted_at IS NOT NULL FROM leases WHERE id=$1 AND company_id=$2`, id, p.CompanyID).Scan(&out.Number, &archived)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if archived && !p.Allows("lease.archived.view", All) {
			return ErrNotFound
		}
		calendar, err := readLeaseCalendar(ctx, tx, id)
		if err != nil {
			return err
		}
		out.Lifecycle = calendar.Lifecycle
		actions := []string{"lease.create", "lease.details.change", "lease.finalize", "lease.archive", "lease.restore"}
		if billing {
			actions = []string{"lease.billing.change", "lease.billing.finalize", "lease.billing.replace", "lease.billing.retry", "lease.billing.retry_failed", "lease.billing.source_change", "lease.archive", "lease.restore"}
		}
		rows, err := tx.Query(ctx, `SELECT action,recorded_at,coalesce(actor_id,0),detail FROM authorization_audit WHERE target_type='lease' AND target_id=$1 AND action=ANY($2::text[]) ORDER BY id`, id, actions)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event LeaseHistoryEvent
			var detail json.RawMessage
			if err := rows.Scan(&event.Action, &event.At, &event.ActorID, &detail); err != nil {
				return err
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(detail, &envelope); err != nil {
				return err
			}
			before, after := envelope["before"], envelope["after"]
			if event.Action == "lease.create" {
				after = detail
			}
			keys := []string{"ID", "CustomerID", "Number", "Title", "Reference", "ExecutionDate", "StartsOn", "EndsOn", "PremisesID", "AssetIDs", "Coverage", "CustomFields"}
			if billing {
				keys = []string{"ID", "Number", "InitialDueOn", "Terms", "ManagedBillingStart"}
				if event.Action == "lease.billing.replace" || event.Action == "lease.billing.retry" || event.Action == "lease.billing.retry_failed" || event.Action == "lease.billing.source_change" {
					after = detail
					keys = []string{"obligation_id", "previous_invoice_id", "invoice_id", "operation_key", "change_id", "starts_on", "ends_on", "reason"}
				}
			}
			if event.Action == "lease.archive" || event.Action == "lease.restore" {
				keys = []string{"archived"}
			}
			project := func(raw json.RawMessage) (string, error) {
				if len(raw) == 0 {
					return "", nil
				}
				var captured map[string]json.RawMessage
				if err := json.Unmarshal(raw, &captured); err != nil {
					return "", err
				}
				safe := map[string]json.RawMessage{}
				for _, key := range keys {
					if value, ok := captured[key]; ok {
						safe[key] = value
					}
				}
				data, err := json.MarshalIndent(safe, "", "  ")
				return string(data), err
			}
			if event.Before, err = project(before); err != nil {
				return err
			}
			if event.After, err = project(after); err != nil {
				return err
			}
			if event.Action == "lease.details.change" {
				var old, next struct{ AgreementFileID *int64 }
				if err := json.Unmarshal(before, &old); err != nil {
					return err
				}
				if err := json.Unmarshal(after, &next); err != nil {
					return err
				}
				event.AgreementChanged = old.AgreementFileID == nil && next.AgreementFileID != nil || old.AgreementFileID != nil && (next.AgreementFileID == nil || *old.AgreementFileID != *next.AgreementFileID)
			}
			out.Events = append(out.Events, event)
		}
		return rows.Err()
	})
	return out, err
}
