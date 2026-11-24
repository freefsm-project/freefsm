package services

import (
	"context"
	"errors"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

func financialAgingBucket(due string, asOf time.Time, loc *time.Location) (int, error) {
	if due == "" || due == "0001-01-01" {
		return 5, nil
	}
	d, err := time.Parse(time.DateOnly, due)
	if err != nil {
		return 0, err
	}
	now := asOf.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	days := int(today.Sub(d).Hours() / 24)
	switch {
	case days <= 0:
		return 0, nil
	case days <= 30:
		return 1, nil
	case days <= 60:
		return 2, nil
	case days <= 90:
		return 3, nil
	default:
		return 4, nil
	}
}

func readFinancialReceivables(ctx context.Context, tx pgx.Tx, p authorization.Principal, out *FinancialReport, loc *time.Location) error {
	// Fetch candidate parents first, then close rows before nested tx queries.
	rows, err := tx.Query(ctx, `SELECT id,display_name,deleted_at IS NOT NULL FROM customers WHERE company_id=$1 AND ($2::bigint=0 OR id=$2) AND ($3 OR deleted_at IS NULL) ORDER BY lower(display_name),id`, p.CompanyID, out.Filter.CustomerID, out.Filter.IncludeArchived)
	if err != nil {
		return err
	}
	var customers []FinancialCustomerRow
	for rows.Next() {
		var c FinancialCustomerRow
		if err := rows.Scan(&c.ID, &c.Name, &c.Archived); err != nil {
			rows.Close()
			return err
		}
		c.Reference = authorization.FinancialReference{Facet: out.Kind, CustomerID: c.ID, Archived: c.Archived}
		customers = append(customers, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range customers {
		rows, err := tx.Query(ctx, `SELECT i.id,coalesce(i.job_id,0),coalesce(i.invoice_number,0),coalesce(nullif(i.number_label,''),i.invoice_number::text,i.id::text),i.title,coalesce(i.due_date::date::text,''),i.financial,i.line_items::text,i.tax_rate::text,
 coalesce((SELECT sum(a.amount_cents) FROM payment_invoice_allocations a JOIN invoice_payments p ON p.id=a.payment_id WHERE a.invoice_id=i.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=p.id)),0)
 +coalesce((SELECT sum(a.amount_cents) FROM credit_applications a WHERE a.invoice_id=i.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)),0)
 FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE i.company_id=$1 AND i.customer_id=$2 AND i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL AND s.category_key IN ('invoice:invoiced','invoice:sent') ORDER BY i.invoice_number,i.id`, p.CompanyID, c.ID)
		if err != nil {
			return err
		}
		type candidate struct {
			row                   FinancialInvoiceRow
			financial, lines, tax string
		}
		var candidates []candidate
		for rows.Next() {
			var v candidate
			if err := rows.Scan(&v.row.ID, &v.row.JobID, &v.row.InvoiceNumber, &v.row.Number, &v.row.Title, &v.row.DueDate, &v.financial, &v.lines, &v.tax, &v.row.SettledCents); err != nil {
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
			r := v.row
			r.CustomerID, r.Customer = c.ID, c.Name
			r.Reference = authorization.FinancialReference{Facet: out.Kind, CustomerID: c.ID, Archived: c.Archived, Invoices: []authorization.FinancialInvoiceReference{{ID: r.ID, JobID: r.JobID}}}
			if err := authorization.CheckFinancialReference(ctx, tx, p, r.Reference); err != nil {
				if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) {
					continue
				}
				return err
			}
			total, err := FinancialTotals(v.financial, v.lines, v.tax)
			if err != nil {
				return err
			}
			r.TotalCents = total.Total.MinorUnits()
			if r.TotalCents < 0 || r.SettledCents < 0 || r.SettledCents > r.TotalCents {
				return errors.New("inconsistent Invoice settlement amounts")
			}
			r.DueCents = r.TotalCents - r.SettledCents
			if r.DueCents <= 0 {
				continue
			}
			bucket, err := financialAgingBucket(r.DueDate, out.AsOf, loc)
			if err != nil {
				return err
			}
			if bucket == 5 {
				r.DueDate = ""
			}
			r.Bucket = FinancialAgingBuckets[bucket]
			if out.Filter.Bucket != "" && out.Filter.Bucket != r.Bucket {
				continue
			}
			for _, dst := range []*int64{&out.Totals.ReceivableCents, &out.Totals.Buckets[bucket], &c.ReceivableCents, &c.Buckets[bucket]} {
				if err := financialAdd(dst, r.DueCents); err != nil {
					return err
				}
			}
			out.Totals.InvoiceCount++
			bt := &out.Totals.BucketTotals[bucket]
			bt.InvoiceCount++
			for _, dst := range []*int64{&out.Totals.TotalCents, &c.TotalCents, &bt.TotalCents} {
				if err := financialAdd(dst, r.TotalCents); err != nil {
					return err
				}
			}
			for _, dst := range []*int64{&out.Totals.SettledCents, &c.SettledCents, &bt.SettledCents} {
				if err := financialAdd(dst, r.SettledCents); err != nil {
					return err
				}
			}
			if err := financialAdd(&bt.ReceivableCents, r.DueCents); err != nil {
				return err
			}
			c.InvoiceCount++
			c.Reference.Invoices = append(c.Reference.Invoices, r.Reference.Invoices...)
			if out.Kind == authorization.FinancialAging {
				out.Invoices = append(out.Invoices, r)
				out.References = append(out.References, r.Reference)
			}
		}
		if out.Kind == authorization.FinancialCustomers {
			out.Customers = append(out.Customers, c)
			out.References = append(out.References, c.Reference)
		}
	}
	return nil
}
