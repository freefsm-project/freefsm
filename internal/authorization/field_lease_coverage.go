package authorization

import (
	"context"
	"time"
)

// FieldLeaseCoverage is a current observation, not a Lease reference. Clients
// retain the observation and its timestamp offline; dates cannot renew it.
type FieldLeaseCoverage struct {
	Covered bool      `json:"covered"`
	AsOf    time.Time `json:"as_of"`
}

// The caller must first authorize the operational Job-Asset relationship under
// the same authority fence. Coverage uses the Lease domain's current lifecycle
// and inclusive coverage intervals, independently of placement and Lease grants.
func fieldLeaseCoverage(ctx context.Context, q queryer, company, customer, asset int64) (*FieldLeaseCoverage, error) {
	var out FieldLeaseCoverage
	err := q.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM leases l JOIN lease_asset_dependencies d ON d.lease_id=l.id
 WHERE l.company_id=$1 AND l.customer_id=$2 AND d.asset_id=$3
 AND (`+leaseWorkspaceLifecycle+`) IN ('active','month_to_month')
 AND d.starts_on<=lease_dependency_today(l.company_id)
 AND (d.ends_on IS NULL OR d.ends_on>=lease_dependency_today(l.company_id))
),transaction_timestamp()`, company, customer, asset).Scan(&out.Covered, &out.AsOf)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
