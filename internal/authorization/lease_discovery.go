package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type LeaseListRow struct {
	StartsOn, EndsOn, NoticeEndsOn string
	Lifecycle                      string
	ID                             int64
	Number, Title                  string
	Archived                       bool
}
type LeaseList struct {
	Query                                 LeaseWorkspaceQuery
	Queues                                []LeaseWorkspaceQueue
	CanViewBilling                        bool
	Rows                                  []LeaseListRow
	Billing, CanRegister, CanViewArchived bool
	Page, PerPage, Total                  int
	Search, State                         string
}

func (s *Service) LeaseList(ctx context.Context, a Actor, billing bool, search, state string, page, size int, queries ...LeaseWorkspaceQuery) (LeaseList, error) {
	out := LeaseList{Search: search, State: state, Page: page, PerPage: size}
	if len(queries) > 0 {
		out.Query = queries[0]
	}
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
		out.CanViewBilling = p.Allows("lease.billing.view", All)
		if err := out.Query.validate(out.Billing, out.CanViewBilling); err != nil {
			return err
		}
		args := []any{p.CompanyID, out.State, search}
		bind := func(value any) string { args = append(args, value); return fmt.Sprintf("$%d", len(args)) }
		title, searchExpr := "l.title", "l.number_label||' '||l.title||' '||l.reference"
		// Related search never expands access to an archived target. Its bounded
		// source reference may still be shown on details, but is not discovery.
		if p.Allows("customer.view", All) {
			searchExpr += `||' '||coalesce((SELECT display_name FROM customers c WHERE c.id=l.customer_id AND (c.deleted_at IS NULL OR ` + bind(p.Allows("customer.archived.view", All)) + `)), '')`
		}
		if p.Allows("location.view", All) {
			searchExpr += `||' '||coalesce((SELECT concat_ws(' ',title,address_1,address_2,city,state,zip_code) FROM locations x WHERE x.id=l.premises_id AND (x.deleted_at IS NULL OR ` + bind(p.Allows("location.archived.view", All)) + `)), '')`
		}
		if p.Allows("asset.view", All) {
			searchExpr += `||' '||coalesce((SELECT string_agg(x.number,' ') FROM assets x WHERE (x.deleted_at IS NULL OR ` + bind(p.Allows("asset.archived.view", All)) + `) AND (EXISTS(SELECT 1 FROM lease_intended_assets i WHERE i.lease_id=l.id AND i.asset_id=x.id) OR EXISTS(SELECT 1 FROM lease_asset_dependencies d WHERE d.lease_id=l.id AND d.asset_id=x.id))), '')`
		}
		if out.Billing {
			title, searchExpr = "''::text", "number_label"
			args = args[:3]
		}
		where := ` FROM leases l WHERE company_id=$1 AND ($2='all' OR ($2='archived')=(deleted_at IS NOT NULL)) AND strpos(lower(` + searchExpr + `),lower($3))>0`
		where += out.Query.where(bind)
		for _, queue := range leaseWorkspaceQueues {
			if queue.Billing && !out.CanViewBilling || !queue.Billing && out.Billing {
				continue
			}
			if err := tx.QueryRow(ctx, `SELECT count(*)`+where+` AND (`+queue.predicate+`)`, args...).Scan(&queue.Count); err != nil {
				return err
			}
			out.Queues = append(out.Queues, queue)
		}
		if out.Query.Queue != "" {
			for _, queue := range leaseWorkspaceQueues {
				if queue.Key == out.Query.Queue {
					where += ` AND (` + queue.predicate + `)`
				}
			}
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)`+where, args...).Scan(&out.Total); err != nil {
			return err
		}
		limit, offset := bind(out.PerPage), bind((out.Page-1)*out.PerPage)
		rows, err := tx.Query(ctx, `SELECT id,number_label,`+title+`,deleted_at IS NOT NULL,`+leaseNoticeCalendarColumns+where+` ORDER BY document_number,id LIMIT `+limit+` OFFSET `+offset, args...)
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
			if !out.Billing {
				row.StartsOn = start
				row.EndsOn = end
			}
			out.Rows = append(out.Rows, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !out.Billing {
			for i := range out.Rows {
				if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT effective_on::text FROM lease_notice_current WHERE lease_id=$1 AND action<>'withdraw'),'')`, out.Rows[i].ID).Scan(&out.Rows[i].NoticeEndsOn); err != nil {
					return err
				}
			}
		}
		return nil
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
			actions = []string{"lease.billing.change", "lease.billing.finalize", "lease.billing.replace", "lease.billing.retry", "lease.billing.retry_failed", "lease.billing.source_change", "lease.billing.revise", "lease.archive", "lease.restore"}
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
				if event.Action == "lease.billing.revise" {
					before, after = nil, detail
					keys = []string{"effective_on", "reason", "before", "after"}
				}
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
