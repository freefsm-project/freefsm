package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/freefsm-project/freefsm/internal/commercial"
	invoiceStatus "github.com/freefsm-project/freefsm/internal/statusflow/invoice"
	"github.com/jackc/pgx/v5"
)

type LeaseReportInvoice struct {
	ID                                       int64
	Number, Kind, DueOn, GeneratedAt, Status string
	ManualStatus                             string
	RecordedCents                            int64
	Review                                   bool
	SettlementVisible                        bool
	Settlements                              []LeaseReportSettlement
	Assets                                   []LeaseReportAsset
}
type LeaseReportSettlement struct {
	ID, Kind, EffectiveOn, ReversedOn string
	AmountCents                       int64
}
type LeaseReportPeriod struct{ Kind, DueOn, State string }

func readLeaseReportBilling(ctx context.Context, tx pgx.Tx, p Principal, out *LeaseReportRow, at string) error {
	var cutoff time.Time
	if err := tx.QueryRow(ctx, `SELECT ($1::date+1)::timestamp AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=$2),'UTC')`, at, p.CompanyID).Scan(&cutoff); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT i.id,i.number_label,o.kind,o.due_on::text,g.recorded_at::text,coalesce(r.financial,o.financial),
 coalesce((SELECT a.detail->>'category_key' FROM authorization_audit a WHERE a.target_type='invoice' AND a.target_id=i.id AND a.detail ? 'category_key' AND a.recorded_at<$2 ORDER BY a.id DESC LIMIT 1),'invoice:draft'),
 EXISTS(SELECT 1 FROM lease_invoice_review_flags f JOIN lease_billing_source_changes c ON c.id=f.change_id WHERE f.invoice_id=i.id AND c.recorded_at<$2)
 FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id JOIN invoices i ON i.id=g.invoice_id LEFT JOIN lease_billing_obligation_revisions r ON r.id=g.obligation_revision_id WHERE o.lease_id=$1 AND g.recorded_at<$2 ORDER BY o.due_on,g.invoice_id`, out.ID, cutoff)
	if err != nil {
		return err
	}
	type candidate struct {
		row       LeaseReportInvoice
		financial string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.row.ID, &c.row.Number, &c.row.Kind, &c.row.DueOn, &c.row.GeneratedAt, &c.financial, &c.row.Status, &c.row.Review); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		if _, err = leaseAdjustmentInvoiceAccess(ctx, tx, p, c.row.ID, false); err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
				continue
			}
			return err
		}
		snapshot, err := commercial.Read(c.financial)
		if err != nil {
			return err
		}
		c.row.RecordedCents = snapshot.Result.TotalCents
		c.row.ManualStatus = c.row.Status
		if p.Allows("asset.view", All) {
			assetRows, err := tx.Query(ctx, `SELECT a.id,a.number,a.name FROM assets a WHERE a.company_id=$2 AND (a.deleted_at IS NULL OR $3) AND a.id IN (
 SELECT (line->>'asset_id')::bigint FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id LEFT JOIN lease_billing_obligation_revisions r ON r.id=g.obligation_revision_id CROSS JOIN LATERAL jsonb_array_elements(coalesce(r.line_items,o.line_items)) line WHERE g.invoice_id=$1) ORDER BY a.number,a.id`, c.row.ID, p.CompanyID, p.Allows("asset.archived.view", All))
			if err != nil {
				return err
			}
			for assetRows.Next() {
				var asset LeaseReportAsset
				if err = assetRows.Scan(&asset.ID, &asset.Number, &asset.Name); err != nil {
					assetRows.Close()
					return err
				}
				c.row.Assets = append(c.row.Assets, asset)
			}
			err = assetRows.Err()
			assetRows.Close()
			if err != nil {
				return err
			}
		}
		err = parentFacet(ctx, tx, p, "invoice", c.row.ID, "settlement.view", true)
		if err == nil {
			c.row.SettlementVisible = true
			c.row.Settlements, err = readLeaseReportSettlement(ctx, tx, c.row.ID, at)
			if err != nil {
				return err
			}
			// Commercial edits have their own immutable before/after history.
			// Generation remains the fallback and is displayed separately.
			var financial string
			err = tx.QueryRow(ctx, `SELECT coalesce((SELECT detail->'after'->>'financial' FROM authorization_audit WHERE target_type='invoice' AND target_id=$1 AND recorded_at<$2 AND detail->'after' ? 'financial' ORDER BY id DESC LIMIT 1),$3)`, c.row.ID, cutoff, c.financial).Scan(&financial)
			if err != nil {
				return err
			}
			position, err := commercial.Read(financial)
			if err != nil {
				return err
			}
			var settled int64
			for _, entry := range c.row.Settlements {
				if entry.ReversedOn == "" {
					settled += entry.AmountCents
				}
			}
			state := "unpaid"
			if position.Result.TotalCents == 0 || settled >= position.Result.TotalCents {
				state = "paid"
			} else if settled > 0 {
				state = "partially_paid"
			}
			c.row.Status = invoiceStatus.EffectiveCategory(c.row.ManualStatus, state, settled > 0)
		} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrForbidden) {
			return err
		} else {
			c.row.Status = ""
		}
		out.Invoices = append(out.Invoices, c.row)
	}
	// A missing Invoice is never a zero Invoice or an invented receivable. Only
	// managed schedule periods due by the effective date are listed as ungenerated.
	// Absence is knowable only with complete current authority. Do not inspect
	// hidden generations to decide whether a period exists or matches a filter.
	out.GenerationStatusUnavailable = !p.Allows("invoice.view", All) || !p.Allows("pricing.view", All) || !p.Allows("invoice.archived.view", All)
	if !out.GenerationStatusUnavailable {
		rows, err = tx.Query(ctx, `WITH agreement AS (
 SELECT l.starts_on,f.initial_due_on,f.managed_billing_start FROM leases l JOIN lease_finalizations f ON f.lease_id=l.id WHERE l.id=$1
 ), keys AS (
  SELECT kind,due_on FROM lease_billing_obligations WHERE lease_id=$1
 UNION SELECT kind,due_on FROM lease_billing_period_failures WHERE lease_id=$1
 UNION SELECT 'initial',initial_due_on FROM agreement WHERE initial_due_on>=managed_billing_start
 UNION SELECT 'coverage',(c.payload->>'effective_on')::date FROM lease_coverage_current c CROSS JOIN agreement a WHERE c.lease_id=$1 AND c.action<>'withdraw' AND c.payload->>'kind'='add' AND (c.payload->>'charge_cents')::bigint>0 AND (c.payload->>'effective_on')::date>=a.managed_billing_start
 UNION SELECT 'monthly',day::date FROM agreement a CROSS JOIN LATERAL generate_series(date_trunc('month',greatest(a.starts_on,a.managed_billing_start))::date,$2::date,interval '1 month') day WHERE day::date>a.starts_on AND day::date>=a.managed_billing_start
		) SELECT k.kind,k.due_on::text,'ungenerated'
 FROM keys k WHERE k.due_on<=$2::date AND NOT EXISTS(SELECT 1 FROM lease_billing_obligations o JOIN lease_invoice_generations g ON g.obligation_id=o.id WHERE o.lease_id=$1 AND o.kind=k.kind AND o.due_on=k.due_on AND g.recorded_at<$3) ORDER BY k.due_on,k.kind`, out.ID, at, cutoff)
		if err != nil {
			return err
		}
		for rows.Next() {
			var period LeaseReportPeriod
			if err = rows.Scan(&period.Kind, &period.DueOn, &period.State); err != nil {
				rows.Close()
				return err
			}
			out.Periods = append(out.Periods, period)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(out.Periods) > 0 {
			var raw []byte
			if err = tx.QueryRow(ctx, `SELECT terms FROM lease_finalizations WHERE lease_id=$1`, out.ID).Scan(&raw); err != nil {
				return err
			}
			var initial []DraftLeaseTerms
			if err = json.Unmarshal(raw, &initial); err != nil {
				return err
			}
			for i := range out.Periods {
				period := &out.Periods[i]
				due, err := time.Parse(time.DateOnly, period.DueOn)
				if err != nil {
					return err
				}
				_, _, disposition, err := effectiveLeasePeriodTerms(ctx, tx, out.ID, initial, due, period.Kind, at)
				if err != nil {
					return err
				}
				if disposition != "" {
					period.State = disposition
				}
			}
		}
	}
	rows, err = tx.Query(ctx, `SELECT id,asset_id,invoice_id,actor_id,amount_cents,kind,starts_on::text,ends_on::text,reason,recorded_at::text FROM lease_manual_adjustments WHERE lease_id=$1 AND recorded_at<$2 ORDER BY id`, out.ID, cutoff)
	if err != nil {
		return err
	}
	var adjustments []LeaseAdjustmentRecord
	for rows.Next() {
		var v LeaseAdjustmentRecord
		if err = rows.Scan(&v.ID, &v.AssetID, &v.InvoiceID, &v.ActorID, &v.AmountCents, &v.Kind, &v.StartsOn, &v.EndsOn, &v.Reason, &v.RecordedAt); err != nil {
			rows.Close()
			return err
		}
		adjustments = append(adjustments, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range adjustments {
		if _, err = leaseAdjustmentInvoiceAccess(ctx, tx, p, v.InvoiceID, false); err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
				continue
			}
			return err
		}
		// Do not reveal an inaccessible related Asset or actor through provenance.
		var visible bool
		if err = tx.QueryRow(ctx, `SELECT $2 AND EXISTS(SELECT 1 FROM assets WHERE id=$1 AND (deleted_at IS NULL OR $3))`, v.AssetID, p.Allows("asset.view", All), p.Allows("asset.archived.view", All)).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			v.AssetID = 0
		}
		if !p.Allows("directory.view", All) {
			v.ActorID = 0
		}
		out.Adjustments = append(out.Adjustments, v)
	}
	return nil
}

func readLeaseReportSettlement(ctx context.Context, tx pgx.Tx, invoice int64, at string) ([]LeaseReportSettlement, error) {
	rows, err := tx.Query(ctx, `WITH events AS (
 SELECT p.id,'Payment' kind,p.received_date effective_on,a.amount_cents,'payment' operation FROM invoice_payments p JOIN payment_invoice_allocations a ON a.payment_id=p.id WHERE a.invoice_id=$1
 UNION ALL SELECT id,'Payment-funded Credit application',effective_date,amount_cents,'credit_application' FROM credit_applications WHERE invoice_id=$1)
 SELECT e.id::text,e.kind,e.effective_on::text,e.amount_cents,coalesce(r.effective_date::text,'') FROM events e LEFT JOIN settlement_reversals r ON r.operation_type=e.operation AND r.operation_id=e.id AND r.effective_date<=$2::date WHERE e.effective_on<=$2::date ORDER BY e.effective_on,e.id`, invoice, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeaseReportSettlement
	for rows.Next() {
		var v LeaseReportSettlement
		if err = rows.Scan(&v.ID, &v.Kind, &v.EffectiveOn, &v.AmountCents, &v.ReversedOn); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
