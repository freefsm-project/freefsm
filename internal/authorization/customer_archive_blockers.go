package authorization

import (
	"context"
	"errors"
	"strconv"
)

// Restricted blockers deliberately omit identity, title, count and navigation.
// RequiredPermission describes the view needed to inspect the dependency, not
// authority to resolve it. Resolution always uses its own independent command.
type CustomerArchiveBlocker struct {
	Kind               string
	ID                 string
	Name               string
	Reason             string
	RequiredPermission string
}

func (v *CustomerArchivePreview) checkChildGrants(p Principal, kind string) {
	for _, action := range []string{"view", "archive"} {
		permission := kind + "." + action
		if !p.Allows(permission, All) {
			v.missingChildGrant = true
			v.Blockers = append(v.Blockers, CustomerArchiveBlocker{Kind: kind, Reason: "Included records require independent All Records authority", RequiredPermission: permission})
		}
	}
}

func customerArchiveBlockers(ctx context.Context, q queryer, p Principal, customer int64) ([]CustomerArchiveBlocker, error) {
	// Include child dependencies independently of their current Customer. This
	// also fails safely on legacy cross-Customer links without rewriting them.
	rows, err := q.Query(ctx, `WITH contacts AS (
 SELECT id FROM customer_contacts WHERE customer_id=$1 AND company_id=$2 AND deleted_at IS NULL
), places AS (
 SELECT id FROM locations WHERE object_type='customer' AND object_id=$1 AND company_id=$2 AND deleted_at IS NULL
), equipment AS (
 SELECT id FROM assets WHERE customer_id=$1 AND company_id=$2 AND ownership_class='customer_owned' AND deleted_at IS NULL
)
SELECT 'job',j.id::text,j.job_type,'Unarchived Job must be archived separately' FROM jobs j LEFT JOIN statuses s ON s.id=j.status_id
 WHERE j.deleted_at IS NULL AND (j.customer_id=$1 OR (coalesce(s.category_key,'') NOT IN ('job:completed','job:canceled') AND
 (j.customer_contact_id IN (SELECT id FROM contacts) OR j.location_id IN (SELECT id FROM places)
 OR EXISTS(SELECT 1 FROM job_contacts x WHERE x.job_id=j.id AND x.contact_id IN (SELECT id FROM contacts))
 OR EXISTS(SELECT 1 FROM job_asset_links x WHERE x.job_id=j.id AND x.operational AND x.unlinked_at IS NULL AND x.asset_id IN (SELECT id FROM equipment)))))
UNION
SELECT 'project',p.id::text,p.name,'Unarchived Project must be archived separately' FROM projects p LEFT JOIN statuses s ON s.id=p.status_id
 WHERE p.deleted_at IS NULL AND (p.customer_id=$1 OR (p.location_id IN (SELECT id FROM places) AND coalesce(s.category_key,'') NOT IN ('project:completed','project:canceled')))
UNION
SELECT 'estimate',id::text,coalesce(title,''),'Operational Estimate must be archived separately' FROM estimates
 WHERE customer_id=$1 AND deleted_at IS NULL AND conversion_hidden_at IS NULL
UNION
SELECT 'invoice',i.id::text,coalesce(nullif(i.number_label,''),i.invoice_number::text,i.id::text),
 CASE WHEN coalesce(s.category_key,'invoice:draft')='invoice:draft' THEN 'Draft Invoice must be resolved, including zero-total Drafts' ELSE 'Outstanding Invoice must be settled or resolved' END
 FROM invoices i LEFT JOIN statuses s ON s.id=i.status_id WHERE i.customer_id=$1 AND i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL
 AND (coalesce(s.category_key,'invoice:draft')='invoice:draft' OR (coalesce(s.category_key,'')<>'invoice:void' AND i.settlement_state<>'paid'))
UNION
SELECT 'asset',id::text,name,'Company-owned placement requires explicit return or transfer' FROM assets WHERE customer_id=$1 AND ownership_class='company_owned'
UNION
SELECT 'lease',l.lease_id::text,'','Unarchived Lease must be explicitly resolved; archive never terminates a Lease' FROM lease_master_dependencies l
 WHERE NOT l.archived AND NOT lease_has_ended(l.lease_id) AND (l.customer_id=$1 OR l.premises_id IN (SELECT id FROM places)
 OR (l.lifecycle IN ('scheduled','active','month_to_month') AND EXISTS(SELECT 1 FROM lease_asset_dependencies x WHERE x.lease_id=l.lease_id AND x.asset_id IN (SELECT id FROM equipment) AND (x.ends_on IS NULL OR x.ends_on>=lease_dependency_today(l.company_id)))))
UNION
SELECT 'credit',c.id::text,'','Available Customer Credit or retainer must be applied or refunded' FROM customer_credits c WHERE c.customer_id=$1
 AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=c.source_payment_id)
 AND c.original_amount_cents
 -coalesce((SELECT sum(a.amount_cents) FROM credit_applications a WHERE a.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)),0)
 -coalesce((SELECT sum(a.amount_cents) FROM credit_refund_allocations a WHERE a.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_refund' AND r.operation_id=a.refund_id)),0)>0
ORDER BY 1,2,4`, customer, p.CompanyID)
	if err != nil {
		return nil, err
	}
	var candidates []CustomerArchiveBlocker
	for rows.Next() {
		var b CustomerArchiveBlocker
		if err = rows.Scan(&b.Kind, &b.ID, &b.Name, &b.Reason); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []CustomerArchiveBlocker{}
	restricted := map[string]bool{}
	for _, b := range candidates {
		b.RequiredPermission = b.Kind + ".view"
		visible := false
		switch b.Kind {
		case "credit":
			visible = p.Allows("credit.view", All)
		case "lease":
			var company int64
			if err := q.QueryRow(ctx, `SELECT company_id FROM lease_master_dependencies WHERE lease_id=$1`, b.ID).Scan(&company); err != nil {
				return nil, err
			}
			visible = company == p.CompanyID && p.Allows("lease.view", All)
		default:
			id, err := strconv.ParseInt(b.ID, 10, 64)
			if err != nil {
				return nil, err
			}
			_, err = parentAccess(ctx, q, p, b.Kind, id, "", false)
			if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrForbidden) {
				return nil, err
			}
			visible = err == nil
		}
		if !visible {
			b.ID, b.Name = "", ""
			b.Reason = "A dependency must be resolved before Customer archive"
			key := b.Kind
			if restricted[key] {
				continue
			}
			restricted[key] = true
		}
		out = append(out, b)
	}
	return out, nil
}
