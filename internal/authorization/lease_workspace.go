package authorization

import "time"

// LeaseWorkspaceQuery contains only allowlisted discovery inputs. Lifecycle is
// derived at read time, just as it is on the agreement and billing pages.
type LeaseWorkspaceQuery struct {
	Lifecycle                 string
	Queue, Placement, Billing string
	Customer, Asset, Premises int64
	EndingFrom, EndingThrough string
}

type LeaseWorkspaceQueue struct {
	Key, Label string
	Billing    bool
	Count      int
	predicate  string
}

const leaseWorkspaceUnplaced = `EXISTS(SELECT 1 FROM lease_asset_dependencies d JOIN assets a ON a.id=d.asset_id WHERE d.lease_id=l.id AND (d.ends_on IS NULL OR d.ends_on>=lease_dependency_today(l.company_id)) AND a.customer_id IS NULL)`
const leaseWorkspaceWrongPremises = `EXISTS(SELECT 1 FROM lease_asset_dependencies d JOIN assets a ON a.id=d.asset_id WHERE d.lease_id=l.id AND (d.ends_on IS NULL OR d.ends_on>=lease_dependency_today(l.company_id)) AND a.customer_id=l.customer_id AND a.location_id IS DISTINCT FROM l.premises_id)`
const leaseWorkspaceReview = `EXISTS(SELECT 1 FROM lease_billing_obligations o JOIN lease_invoice_generations g ON g.obligation_id=o.id JOIN lease_invoice_review_flags f ON f.invoice_id=g.invoice_id WHERE o.lease_id=l.id)`

// Same unresolved-failure semantics as the owning billing history: a generated
// period or a disposition for the current source revision is not a failure.
const leaseWorkspaceFailed = `EXISTS(SELECT 1 FROM lease_billing_period_failures f WHERE f.lease_id=l.id AND f.failure<>''
 AND NOT EXISTS(SELECT 1 FROM lease_billing_obligations o JOIN lease_invoice_generations g ON g.obligation_id=o.id WHERE o.lease_id=l.id AND o.kind=f.kind AND o.due_on=f.due_on)
 AND NOT EXISTS(SELECT 1 FROM lease_billing_period_dispositions d WHERE d.lease_id=l.id AND d.kind=f.kind AND d.due_on=f.due_on AND d.source_revision=(SELECT coalesce(max(id),0) FROM lease_billing_source_changes WHERE lease_id=l.id)))`

var leaseWorkspaceQueues = []LeaseWorkspaceQueue{
	{Key: "starts", Label: "Upcoming starts", predicate: `l.deleted_at IS NULL AND (` + leaseWorkspaceLifecycle + `)='scheduled'`},
	{Key: "renewals", Label: "Entering Month-to-Month (next 30 days)", predicate: `l.deleted_at IS NULL AND (` + leaseWorkspaceLifecycle + `) IN ('scheduled','active') AND l.ends_on<lease_dependency_today(l.company_id)+30 AND NOT EXISTS(SELECT 1 FROM lease_notice_current n WHERE n.lease_id=l.id AND n.action<>'withdraw')`},
	{Key: "termination", Label: "Pending terminations", predicate: `l.deleted_at IS NULL AND NOT lease_has_ended(l.id) AND EXISTS(SELECT 1 FROM lease_notice_current n WHERE n.lease_id=l.id AND n.action<>'withdraw')`},
	{Key: "placement", Label: "Placement exceptions", predicate: `l.deleted_at IS NULL AND NOT lease_has_ended(l.id) AND (` + leaseWorkspaceUnplaced + ` OR ` + leaseWorkspaceWrongPremises + `)`},
	{Key: "monthly", Label: "Monthly billing", Billing: true, predicate: `l.deleted_at IS NULL AND NOT lease_has_ended(l.id) AND l.lifecycle<>'draft'`},
	{Key: "failed", Label: "Generation failures", Billing: true, predicate: leaseWorkspaceFailed},
	{Key: "review", Label: "Manual review — generated-Invoice exceptions", Billing: true, predicate: leaseWorkspaceReview},
}

func (q LeaseWorkspaceQuery) validate(billingOnly, canBilling bool) error {
	if q.Lifecycle != "" && q.Lifecycle != "draft" && q.Lifecycle != "scheduled" && q.Lifecycle != "active" && q.Lifecycle != "month_to_month" && q.Lifecycle != "terminated" {
		return ErrConflict
	}
	if q.Queue != "" {
		found := false
		for _, queue := range leaseWorkspaceQueues {
			if queue.Key == q.Queue {
				found = true
				if queue.Billing && !canBilling || !queue.Billing && billingOnly {
					return ErrForbidden
				}
			}
		}
		if !found {
			return ErrConflict
		}
	}
	if q.Billing != "" && !canBilling {
		return ErrForbidden
	}
	if q.Billing != "" && q.Billing != "failed" && q.Billing != "review" {
		return ErrConflict
	}
	if q.Placement != "" && q.Placement != "unplaced" && q.Placement != "wrong_premises" {
		return ErrConflict
	}
	if billingOnly && (q.Customer != 0 || q.Asset != 0 || q.Premises != 0 || q.Placement != "" || q.EndingFrom != "" || q.EndingThrough != "") {
		return ErrForbidden
	}
	if q.Customer < 0 || q.Asset < 0 || q.Premises < 0 {
		return ErrConflict
	}
	for _, date := range []string{q.EndingFrom, q.EndingThrough} {
		if date != "" {
			if _, err := time.Parse(time.DateOnly, date); err != nil {
				return ErrConflict
			}
		}
	}
	if q.EndingFrom != "" && q.EndingThrough != "" && q.EndingFrom > q.EndingThrough {
		return ErrConflict
	}
	return nil
}

func (q LeaseWorkspaceQuery) where(bind func(any) string) string {
	where := ""
	if q.Lifecycle != "" {
		where += ` AND (` + leaseWorkspaceLifecycle + `)=` + bind(q.Lifecycle)
	}
	if q.Customer > 0 {
		where += ` AND l.customer_id=` + bind(q.Customer)
	}
	if q.Premises > 0 {
		where += ` AND l.premises_id=` + bind(q.Premises)
	}
	if q.Asset > 0 {
		id := bind(q.Asset)
		where += ` AND (EXISTS(SELECT 1 FROM lease_intended_assets i WHERE i.lease_id=l.id AND i.asset_id=` + id + `) OR EXISTS(SELECT 1 FROM lease_asset_dependencies d WHERE d.lease_id=l.id AND d.asset_id=` + id + `))`
	}
	ending := `coalesce((SELECT effective_on FROM lease_notice_current n WHERE n.lease_id=l.id AND n.action<>'withdraw'),l.ends_on)`
	if q.EndingFrom != "" {
		where += ` AND ` + ending + `>=` + bind(q.EndingFrom) + `::date`
	}
	if q.EndingThrough != "" {
		where += ` AND ` + ending + `<=` + bind(q.EndingThrough) + `::date`
	}
	if q.Placement == "unplaced" {
		where += ` AND ` + leaseWorkspaceUnplaced
	}
	if q.Placement == "wrong_premises" {
		where += ` AND ` + leaseWorkspaceWrongPremises
	}
	if q.Billing == "failed" {
		where += ` AND ` + leaseWorkspaceFailed
	}
	if q.Billing == "review" {
		where += ` AND ` + leaseWorkspaceReview
	}
	return where
}

const leaseWorkspaceLifecycle = `CASE
 WHEN lease_has_ended(l.id) THEN 'terminated'
 WHEN l.lifecycle='draft' THEN 'draft'
 WHEN l.starts_on>lease_dependency_today(l.company_id) THEN 'scheduled'
 WHEN l.ends_on<lease_dependency_today(l.company_id) THEN 'month_to_month'
 ELSE 'active' END`
