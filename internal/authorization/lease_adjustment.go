package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/jackc/pgx/v5"
)

type LeaseAdjustmentInput struct {
	InvoiceID       int64  `json:"invoice_id"`
	AssetID         int64  `json:"asset_id"`
	Kind            string `json:"kind"`
	AmountCents     int64  `json:"amount_cents"`
	StartsOn        string `json:"starts_on"`
	EndsOn          string `json:"ends_on"`
	Reason          string `json:"reason"`
	OperationKey    string `json:"operation_key"`
	InvoiceRevision string `json:"invoice_revision"`
	Taxable         bool   `json:"taxable"`
}
type LeaseAdjustmentInvoice struct {
	ID                                 int64
	Number, Revision, Lines, Financial string
}
type LeaseAdjustmentRecord struct {
	ID, AssetID, InvoiceID, ActorID, AmountCents              int64
	Kind, StartsOn, EndsOn, Reason, RecordedAt, Before, After string
}
type LeaseAdjustmentReview struct {
	ID        int64
	Number    string
	CanManage bool
	Assets    []int64
	Invoices  []LeaseAdjustmentInvoice
	Selected  LeaseAdjustmentInvoice
	History   []LeaseAdjustmentRecord
}

// Invoice visibility, captured pricing and Edit remain independent of Billing.
func leaseAdjustmentInvoiceAccess(ctx context.Context, tx pgx.Tx, p Principal, id int64, edit bool) (Scope, error) {
	permission := ""
	if edit {
		permission = "invoice.edit"
	}
	scope, err := parentAccess(ctx, tx, p, "invoice", id, permission, true)
	if err != nil {
		return "", err
	}
	if err = parentFacet(ctx, tx, p, "invoice", id, "pricing.view", true); err != nil {
		return "", err
	}
	return scope, nil
}

func (s *Service) ReviewLeaseAdjustments(ctx context.Context, a Actor, id, selected int64) (LeaseAdjustmentReview, error) {
	out := LeaseAdjustmentReview{ID: id}
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.billing.view", All) {
			return ErrNotFound
		}
		billing, err := readLeaseBilling(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if !billing.Finalized {
			return ErrConflict
		}
		out.Number = billing.Number
		out.CanManage = !billing.Archived && p.Allows("lease.billing.manage", All)
		rows, err := tx.Query(ctx, `SELECT DISTINCT asset_id FROM lease_asset_dependencies WHERE lease_id=$1 ORDER BY asset_id`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var asset int64
			if err = rows.Scan(&asset); err != nil {
				rows.Close()
				return err
			}
			out.Assets = append(out.Assets, asset)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT i.id,i.number_label FROM invoices i JOIN leases l ON l.customer_id=i.customer_id LEFT JOIN statuses s ON s.id=i.status_id WHERE l.id=$1 AND i.company_id=$2 AND i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL AND coalesce(s.category_key,'')<>'invoice:void' AND NOT invoice_has_active_settlement(i.id) ORDER BY i.id`, id, p.CompanyID)
		if err != nil {
			return err
		}
		var candidates []LeaseAdjustmentInvoice
		for rows.Next() {
			var v LeaseAdjustmentInvoice
			if err = rows.Scan(&v.ID, &v.Number); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, v := range candidates {
			if _, err = leaseAdjustmentInvoiceAccess(ctx, tx, p, v.ID, true); err != nil {
				if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
					continue
				}
				return err
			}
			out.Invoices = append(out.Invoices, v)
		}
		if selected != 0 {
			if _, err = leaseAdjustmentInvoiceAccess(ctx, tx, p, selected, true); err != nil {
				return err
			}
			var same bool
			if err = tx.QueryRow(ctx, `SELECT i.customer_id=l.customer_id FROM invoices i CROSS JOIN leases l WHERE i.id=$1 AND l.id=$2`, selected, id).Scan(&same); err != nil {
				return err
			}
			if !same {
				return ErrNotFound
			}
			out.Selected.ID = selected
			if err = tx.QueryRow(ctx, `SELECT number_label,line_items::text,financial FROM invoices WHERE id=$1`, selected).Scan(&out.Selected.Number, &out.Selected.Lines, &out.Selected.Financial); err != nil {
				return err
			}
			out.Selected.Revision, err = saveaction.Revision(ctx, tx, p.UserID, fmt.Sprintf("/invoices/%d", selected))
			if err != nil {
				return err
			}
		}
		rows, err = tx.Query(ctx, `SELECT id,asset_id,invoice_id,actor_id,amount_cents,kind,starts_on::text,ends_on::text,reason,recorded_at::text,before_value::text,after_value::text FROM lease_manual_adjustments WHERE lease_id=$1 ORDER BY id`, id)
		if err != nil {
			return err
		}
		var history []LeaseAdjustmentRecord
		for rows.Next() {
			var v LeaseAdjustmentRecord
			if err = rows.Scan(&v.ID, &v.AssetID, &v.InvoiceID, &v.ActorID, &v.AmountCents, &v.Kind, &v.StartsOn, &v.EndsOn, &v.Reason, &v.RecordedAt, &v.Before, &v.After); err != nil {
				rows.Close()
				return err
			}
			history = append(history, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, v := range history {
			if _, err = leaseAdjustmentInvoiceAccess(ctx, tx, p, v.InvoiceID, false); err != nil {
				if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
					continue
				}
				return err
			}
			out.History = append(out.History, v)
		}
		return nil
	})
	return out, err
}

func (s *Service) ApplyLeaseAdjustment(ctx context.Context, a Actor, id int64, in LeaseAdjustmentInput) error {
	return s.transactOutcome(ctx, a, false, false, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.billing.view", All) || !p.Allows("lease.billing.manage", All) {
			return ErrForbidden
		}
		billing, err := readLeaseBilling(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if billing.Archived || !billing.Finalized {
			return ErrConflict
		}
		scope, err := leaseAdjustmentInvoiceAccess(ctx, tx, p, in.InvoiceID, true)
		if err != nil {
			return err
		}
		invalid := func(m string) error { return &LeaseValidationError{Message: m} }
		if in.Kind != "abatement" && in.Kind != "fee" && in.Kind != "damages" {
			return invalid("Select abatement, fee or damages")
		}
		if in.AmountCents <= 0 || strings.TrimSpace(in.Reason) == "" || len(in.OperationKey) < 16 || len(in.OperationKey) > 128 {
			return invalid("Enter a positive explicit USD cent amount, reason and submission identity")
		}
		start, e1 := time.Parse(time.DateOnly, in.StartsOn)
		end, e2 := time.Parse(time.DateOnly, in.EndsOn)
		if e1 != nil || e2 != nil || end.Before(start) {
			return invalid("Enter ordered relevant dates; abatements require the outage dates")
		}
		intent, err := json.Marshal(in)
		if err != nil {
			return err
		}
		var same bool
		err = tx.QueryRow(ctx, `SELECT actor_id=$2 AND lease_id=$3 AND intent=$4::jsonb FROM lease_manual_adjustments WHERE operation_key=$1`, in.OperationKey, p.UserID, id, intent).Scan(&same)
		if err == nil {
			if !same {
				return ErrConflict
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		var eligible, exempt bool
		var lines, financial, rate string
		err = tx.QueryRow(ctx, `SELECT i.customer_id=l.customer_id AND c.deleted_at IS NULL AND i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL AND coalesce(s.category_key,'')<>'invoice:void' AND NOT invoice_has_active_settlement(i.id),c.tax_exempt,i.line_items::text,i.financial,i.tax_rate FROM invoices i JOIN customers c ON c.id=i.customer_id LEFT JOIN statuses s ON s.id=i.status_id CROSS JOIN leases l WHERE i.id=$1 AND l.id=$2 FOR UPDATE OF i,c`, in.InvoiceID, id).Scan(&eligible, &exempt, &lines, &financial, &rate)
		if err != nil {
			return err
		}
		if !eligible {
			return ErrConflict
		}
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_asset_dependencies d JOIN assets a ON a.id=d.asset_id WHERE d.lease_id=$1 AND d.asset_id=$2 AND a.company_id=$3 AND a.deleted_at IS NULL)`, id, in.AssetID, p.CompanyID).Scan(&eligible)
		if err != nil {
			return err
		}
		if !eligible {
			return invalid("Select an available Asset from this Lease's retained coverage")
		}
		revision, err := saveaction.Revision(ctx, tx, p.UserID, fmt.Sprintf("/invoices/%d", in.InvoiceID))
		if err != nil {
			return err
		}
		if in.InvoiceRevision == "" || revision != in.InvoiceRevision {
			return ErrConflict
		}
		var input commercial.Input
		if financial == "" {
			input, err = commercial.CapturedInput(lines, rate, nil, nil)
		} else {
			var snapshot commercial.Snapshot
			snapshot, err = commercial.Read(financial)
			input = snapshot.Input
		}
		if err != nil {
			return err
		}
		input.TaxExempt = exempt
		amount := in.AmountCents
		if in.Kind == "abatement" {
			amount = -amount
		}
		price := commercial.FormatCents(amount)
		input.Lines = append(input.Lines, commercial.Line{Quantity: "1", UnitPrice: price, Taxable: in.Taxable})
		accepted, err := commercial.Accept(input)
		if err != nil {
			return invalid("The adjustment must leave valid nonnegative Invoice pretax and final totals")
		}
		var captures []json.RawMessage
		if err = json.Unmarshal([]byte(lines), &captures); err != nil {
			return err
		}
		line, err := json.Marshal(map[string]any{"title": fmt.Sprintf("%s — %s — Asset %d", billing.Number, in.Kind, in.AssetID), "description": in.Reason, "quantity": 1, "quantity_decimal": "1", "unit_price": json.Number(price), "unit_price_decimal": price, "taxable": in.Taxable, "lease_id": id, "asset_id": in.AssetID, "charge_kind": "manual_" + in.Kind, "starts_on": in.StartsOn, "ends_on": in.EndsOn, "lease_adjustment_operation": in.OperationKey})
		if err != nil {
			return err
		}
		captures = append(captures, line)
		encoded, err := json.Marshal(captures)
		if err != nil {
			return err
		}
		before, err := json.Marshal(map[string]any{"line_items": json.RawMessage(lines), "financial": financial})
		if err != nil {
			return err
		}
		after, err := json.Marshal(map[string]any{"line_items": json.RawMessage(encoded), "financial": accepted})
		if err != nil {
			return err
		}
		snapshot, err := commercial.Read(accepted)
		if err != nil {
			return err
		}
		state := "unpaid"
		if snapshot.Result.TotalCents == 0 {
			state = "paid"
		}
		if _, err = tx.Exec(ctx, `UPDATE invoices SET line_items=$2::jsonb,financial=$3,settlement_state=$4,updated_at=clock_timestamp() WHERE id=$1`, in.InvoiceID, encoded, accepted, state); err != nil {
			return err
		}
		var adjustment int64
		err = tx.QueryRow(ctx, `INSERT INTO lease_manual_adjustments(lease_id,asset_id,invoice_id,actor_id,kind,amount_cents,starts_on,ends_on,reason,operation_key,intent,before_value,after_value) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`, id, in.AssetID, in.InvoiceID, p.UserID, in.Kind, in.AmountCents, in.StartsOn, in.EndsOn, in.Reason, in.OperationKey, intent, before, after).Scan(&adjustment)
		if err != nil {
			return err
		}
		detail := map[string]any{"lease_adjustment_id": adjustment, "lease_id": id, "asset_id": in.AssetID, "invoice_id": in.InvoiceID, "before": json.RawMessage(before), "after": json.RawMessage(after), "reason": in.Reason, "starts_on": in.StartsOn, "ends_on": in.EndsOn}
		if err = auditOperationalEvent(ctx, tx, a, p, "invoice.edit", scope, "invoice", in.InvoiceID, "updated", detail); err != nil {
			return err
		}
		delete(detail, "operational_event")
		return audit(ctx, tx, a, p, "lease.billing.adjust", All, "lease", id, detail)
	})
}
