package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/jackc/pgx/v5"
)

type LeaseBillingPeriod struct {
	LeaseID, ObligationID, InvoiceID               int64
	Kind, DueOn, Failure, ReviewHash, OperationKey string
	TotalCents                                     int64
	Void, CanReplace, CanRetry                     bool
	ReviewFlags                                    []string
	Disposition, TaxRate                           string
	TaxExempt                                      bool
	Lines                                          []LeaseBillingPeriodLine
	EligibilityMessage                             string
	OriginalTotalCents                             int64
	Revisions                                      []LeaseBillingRevision
	ListQuery                                      LeaseBillingQuery
}

type LeaseBillingRevision struct {
	ID, TotalCents int64
	RecordedAt     string
}

type LeaseBillingPeriodLine struct {
	AssetID int64  `json:"asset_id"`
	Amount  string `json:"unit_price_decimal"`
	Taxable bool   `json:"taxable"`
}

func billingReviewHash(ctx context.Context, tx pgx.Tx, obligation, invoice int64) (string, error) {
	var source []byte
	err := tx.QueryRow(ctx, `SELECT jsonb_build_object('obligation',to_jsonb(o),'approval',to_jsonb(f),'agreement',to_jsonb(l),'dependency',to_jsonb(d),'coverage',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.asset_id,c.starts_on) FROM lease_asset_dependencies c WHERE c.lease_id=l.id),'customer',to_jsonb(c),'premises',to_jsonb(p),'assets',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM assets a WHERE a.id IN(SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=l.id)),'invoice',(SELECT to_jsonb(i) FROM invoices i WHERE i.id=$2)) FROM lease_billing_obligations o JOIN lease_finalizations f ON f.lease_id=o.lease_id JOIN leases l ON l.id=o.lease_id JOIN lease_master_dependencies d ON d.lease_id=l.id JOIN customers c ON c.id=l.customer_id LEFT JOIN locations p ON p.id=l.premises_id WHERE o.id=$1`, obligation, invoice).Scan(&source)
	if err != nil {
		return "", err
	}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(c.id),0) FROM lease_billing_source_changes c JOIN lease_billing_obligations o ON o.lease_id=c.lease_id WHERE o.id=$1`, obligation).Scan(&revision); err != nil {
		return "", err
	}
	hash := sha256.Sum256(append(source, []byte(fmt.Sprint(revision))...))
	return hex.EncodeToString(hash[:]), nil
}

func readLeaseBillingPeriod(ctx context.Context, tx pgx.Tx, p Principal, lease, obligation int64) (LeaseBillingPeriod, error) {
	out := LeaseBillingPeriod{LeaseID: lease, ObligationID: obligation}
	var financial string
	err := tx.QueryRow(ctx, `SELECT o.kind,o.due_on::text,o.financial,coalesce(g.invoice_id,0),coalesce(s.category_key='invoice:void',false),CASE WHEN g.invoice_id IS NULL THEN coalesce(a.failure,'') ELSE '' END FROM lease_billing_obligation_current o JOIN leases l ON l.id=o.lease_id LEFT JOIN lease_invoice_generations g ON g.obligation_id=o.id AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations n WHERE n.predecessor_invoice_id=g.invoice_id) LEFT JOIN invoices i ON i.id=g.invoice_id LEFT JOIN statuses s ON s.id=i.status_id LEFT JOIN lease_billing_attempts a ON a.obligation_id=o.id WHERE o.id=$1 AND o.lease_id=$2 AND l.company_id=$3 AND l.deleted_at IS NULL`, obligation, lease, p.CompanyID).Scan(&out.Kind, &out.DueOn, &financial, &out.InvoiceID, &out.Void, &out.Failure)
	if err == pgx.ErrNoRows {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	var lines []byte
	if err := tx.QueryRow(ctx, `SELECT line_items FROM lease_billing_obligation_current WHERE id=$1`, obligation).Scan(&lines); err != nil {
		return out, err
	}
	// A review previews the whole current approved source without writing it.
	// The authorized retry then captures that exact source under the same fence.
	if out.InvoiceID == 0 {
		preview, err := approvedLeasePeriod(ctx, tx, p, lease, obligation)
		if err == nil {
			financial = preview.Financial
			lines = []byte(preview.LineItems)
		} else if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			out.EligibilityMessage = "Current sources do not permit generation yet; review dates, coverage and active masters. The original obligation is retained."
		} else {
			return out, err
		}
	}
	snapshot, err := commercial.Read(financial)
	if err != nil {
		return out, err
	}
	out.TotalCents = snapshot.Result.TotalCents
	out.TaxRate = snapshot.Input.TaxRate
	out.TaxExempt = snapshot.Input.TaxExempt
	var original string
	if err := tx.QueryRow(ctx, `SELECT financial FROM lease_billing_obligations WHERE id=$1`, obligation).Scan(&original); err != nil {
		return out, err
	}
	originalSnapshot, err := commercial.Read(original)
	if err != nil {
		return out, err
	}
	out.OriginalTotalCents = originalSnapshot.Result.TotalCents
	rows, err := tx.Query(ctx, `SELECT id,recorded_at::text,financial FROM lease_billing_obligation_revisions WHERE obligation_id=$1 ORDER BY id`, obligation)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var revision LeaseBillingRevision
		var value string
		if err := rows.Scan(&revision.ID, &revision.RecordedAt, &value); err != nil {
			rows.Close()
			return out, err
		}
		snapshot, err := commercial.Read(value)
		if err != nil {
			rows.Close()
			return out, err
		}
		revision.TotalCents = snapshot.Result.TotalCents
		out.Revisions = append(out.Revisions, revision)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(lines, &out.Lines); err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT d.reason FROM lease_billing_period_dispositions d JOIN lease_billing_obligations o ON o.lease_id=d.lease_id AND o.kind=d.kind AND o.due_on=d.due_on WHERE o.id=$1 AND d.source_revision=(SELECT coalesce(max(id),0) FROM lease_billing_source_changes WHERE lease_id=d.lease_id)),'')`, obligation).Scan(&out.Disposition); err != nil {
		return out, err
	}
	if out.Disposition != "" {
		out.Failure = ""
	}
	if err := tx.QueryRow(ctx, `SELECT ARRAY(SELECT 'Invoice '||r.invoice_id||': '||c.reason FROM lease_invoice_review_flags r JOIN lease_billing_source_changes c ON c.id=r.change_id JOIN lease_invoice_generations g ON g.invoice_id=r.invoice_id WHERE g.obligation_id=$1 ORDER BY c.id,r.invoice_id)`, obligation).Scan(&out.ReviewFlags); err != nil {
		return out, err
	}
	out.ReviewHash, err = billingReviewHash(ctx, tx, obligation, out.InvoiceID)
	if err != nil {
		return out, err
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("lease-replacement:%d:%d:%d:%s", p.UserID, obligation, out.InvoiceID, out.ReviewHash)))
	out.OperationKey = hex.EncodeToString(hash[:])
	out.CanReplace = out.Void && p.Allows("lease.manage", All) && p.Allows("lease.billing.manage", All)
	out.CanRetry = out.InvoiceID == 0 && out.Disposition == "" && out.EligibilityMessage == "" && p.Allows("lease.manage", All) && p.Allows("lease.billing.manage", All)
	return out, nil
}

func (s *Service) ReviewLeaseBillingPeriod(ctx context.Context, a Actor, lease, obligation int64, options ...LeaseBillingQuery) (LeaseBillingPeriod, error) {
	var out LeaseBillingPeriod
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.billing.view", All) {
			return ErrNotFound
		}
		var err error
		out, err = readLeaseBillingPeriod(ctx, tx, p, lease, obligation)
		if err != nil {
			return err
		}
		query := LeaseBillingQuery{}
		if len(options) > 0 {
			query = options[0]
		}
		out.ListQuery, err = NormalizeLeaseBillingQuery(query)
		return err
	})
	return out, err
}

// approvedLeasePeriod derives one complete current-source snapshot for an
// ungenerated obligation. Generated snapshots remain fixed and incompatible
// replacements require review rather than silently rewriting their history.
func approvedLeasePeriod(ctx context.Context, tx pgx.Tx, p Principal, lease, obligation int64) (DocumentCreation, error) {
	var out DocumentCreation
	base, err := readLeaseBase(ctx, tx, p, lease)
	if err != nil {
		return out, err
	}
	var terms []DraftLeaseTerms
	var raw []byte
	var start, end, cutoff, due, today time.Time
	var kind, financial, lines string
	var valid, exempt bool
	err = tx.QueryRow(ctx, `SELECT f.terms,f.starts_on,f.fixed_ends_on,f.managed_billing_start,o.kind,o.due_on,o.financial,o.line_items::text,lease_dependency_today(l.company_id),c.tax_exempt,NOT d.archived AND d.lifecycle<>'draft' AND d.customer_id=l.customer_id AND d.premises_id IS NOT DISTINCT FROM l.premises_id AND c.deleted_at IS NULL AND p.deleted_at IS NULL AND p.company_id=l.company_id AND p.object_type='customer' AND p.object_id=l.customer_id FROM lease_billing_obligation_current o JOIN lease_finalizations f ON f.lease_id=o.lease_id JOIN leases l ON l.id=o.lease_id JOIN lease_master_dependencies d ON d.lease_id=l.id JOIN customers c ON c.id=l.customer_id JOIN locations p ON p.id=l.premises_id WHERE o.id=$1 AND o.lease_id=$2 FOR SHARE OF c,p,d`, obligation, lease).Scan(&raw, &start, &end, &cutoff, &kind, &due, &financial, &lines, &today, &exempt, &valid)
	if err != nil {
		return out, err
	}
	if !valid || base.Archived || base.Lifecycle == "draft" || base.StartsOn != start.Format(time.DateOnly) || base.EndsOn != end.Format(time.DateOnly) || due.Before(cutoff) || due.After(today.AddDate(0, 0, 30)) {
		return out, ErrConflict
	}
	if err = json.Unmarshal(raw, &terms); err != nil {
		return out, err
	}
	terms, eventBilling, err := currentCoverageBillingTerms(ctx, tx, lease, terms, due, kind)
	if err != nil {
		return out, err
	}
	at := due.Format(time.DateOnly)
	if kind == "initial" {
		at = start.Format(time.DateOnly)
	}
	covered := map[int64]bool{}
	for _, c := range base.Coverage {
		if base.Lifecycle == "terminated" && c.EndsOn == nil {
			return out, ErrConflict
		}
		if c.StartsOn <= at && (c.EndsOn == nil || *c.EndsOn >= at) {
			covered[c.AssetID] = true
		}
	}
	for i := range terms {
		terms[i].Selected = terms[i].Selected && (eventBilling || covered[terms[i].AssetID])
		if !terms[i].Selected {
			continue
		}
		delete(covered, terms[i].AssetID)
		if err = tx.QueryRow(ctx, `SELECT company_id=$2 AND ownership_class='company_owned' AND deleted_at IS NULL FROM assets WHERE id=$1 FOR SHARE`, terms[i].AssetID, p.CompanyID).Scan(&valid); err != nil {
			return out, err
		}
		if !valid {
			return out, ErrConflict
		}
		if _, err = tx.Exec(ctx, `SELECT lease_asset_timeline_validate($1)`, terms[i].AssetID); err != nil {
			return out, err
		}
	}
	if len(covered) > 0 && !eventBilling {
		return out, ErrConflict
	}
	input, err := leaseObligationInput(terms, kind == "monthly", exempt)
	if err != nil {
		return out, err
	}
	expected, expectedLines, err := leaseObligationSnapshot(base, terms, kind, due, input)
	if err != nil {
		return out, err
	}
	var generated bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_invoice_generations WHERE obligation_id=$1)`, obligation).Scan(&generated); err != nil {
		return out, err
	}
	if !generated {
		financial = expected
		lines = expectedLines
	}
	if expected != financial {
		return out, &LeaseValidationError{Message: "Generated facts differ from current source; manual review required"}
	}
	if err = tx.QueryRow(ctx, `SELECT $1::jsonb=$2::jsonb`, lines, expectedLines).Scan(&valid); err != nil {
		return out, err
	}
	if !valid {
		return out, ErrConflict
	}
	return DocumentCreation{CustomerID: base.CustomerID, Title: base.Number + " " + kind + " " + due.Format(time.DateOnly), TaxRate: input.TaxRate, LineItems: lines, Financial: financial, CustomFields: "[]", InvoiceDate: today, DueDate: due}, nil
}

type LeaseReplacementInput struct {
	InvoiceID                int64
	ReviewHash, OperationKey string
}

func (s *Service) ReplaceLeaseInvoice(ctx context.Context, a Actor, lease, obligation int64, in LeaseReplacementInput) (int64, error) {
	var invoice int64
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.billing.view", All) || !p.Allows("lease.billing.manage", All) || !p.Allows("lease.manage", All) {
			return ErrForbidden
		}
		var original, owner, period int64
		var review string
		err := tx.QueryRow(ctx, `SELECT g.invoice_id,g.predecessor_invoice_id,g.actor_id,g.obligation_id,g.review_hash FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id JOIN leases l ON l.id=o.lease_id WHERE g.operation_key=$1 AND l.company_id=$2 AND l.id=$3`, in.OperationKey, p.CompanyID, lease).Scan(&invoice, &original, &owner, &period, &review)
		if err == nil {
			if owner != p.UserID || period != obligation || original != in.InvoiceID || review != in.ReviewHash {
				return ErrConflict
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		current, err := readLeaseBillingPeriod(ctx, tx, p, lease, obligation)
		if err != nil {
			return err
		}
		if !current.CanReplace || current.InvoiceID != in.InvoiceID || current.ReviewHash != in.ReviewHash || current.OperationKey != in.OperationKey {
			return ErrConflict
		}
		document, err := approvedLeasePeriod(ctx, tx, p, lease, obligation)
		if err != nil {
			return err
		}
		invoice, err = s.createApprovedDocument(ctx, tx, a, p, "invoice", document, func(string) (string, error) { return "unpaid", nil }, obligation, &leaseGenerationCapability{obligation: obligation, company: p.CompanyID, predecessor: in.InvoiceID})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE invoices SET lease_id=$2 WHERE id=$1`, invoice, lease); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO lease_invoice_generations(obligation_id,invoice_id,predecessor_invoice_id,operation_key,review_hash,actor_id) VALUES($1,$2,$3,$4,$5,$6)`, obligation, invoice, in.InvoiceID, in.OperationKey, in.ReviewHash, p.UserID); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "lease.billing.replace", All, "lease", lease, map[string]any{"obligation_id": obligation, "previous_invoice_id": in.InvoiceID, "invoice_id": invoice, "operation_key": in.OperationKey})
	})
	return invoice, err
}
