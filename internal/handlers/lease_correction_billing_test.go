package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseFactCorrectionPendingMonthEntitlement(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	year := time.Now().UTC().Year() + 1
	january, february, march := fmt.Sprintf("%d-01-01", year), fmt.Sprintf("%d-02-01", year), fmt.Sprintf("%d-03-01", year)
	_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '%s'::date $$`, january))
	historyCheck(t, err)
	id, _ := populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	base, err := svc.DraftLeaseBase(ctx, actor, id)
	historyCheck(t, err)
	before := historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations", "lease_finalizations"})
	historyCheck(t, svc.CorrectLeaseFacts(ctx, actor, id, authorization.LeaseFactInput{ExpectedRevision: base.Revision, StartsOn: february, EndsOn: base.EndsOn, PremisesID: *base.PremisesID, Factual: true, Reason: "Commencement transcription", OperationKey: "corrected-month-entitlement-0001"}))
	historyEqual(t, "generated originals unchanged", historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations", "lease_finalizations"}), before)
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '%s'::date $$`, february))
	historyCheck(t, err)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var valid bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM lease_billing_obligations WHERE lease_id=$1 AND kind='monthly' AND due_on=$2::date)
 AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.kind='monthly' AND o.due_on=$2::date)
 AND EXISTS(SELECT 1 FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.kind='monthly' AND o.due_on=$3::date)
 AND EXISTS(SELECT 1 FROM lease_invoice_review_flags f JOIN lease_billing_source_changes c ON c.id=f.change_id WHERE c.lease_id=$1)`, id, february, march).Scan(&valid))
	if !valid {
		t.Fatal("corrected commencement did not retain pending identity, stop unowed month, flag generated invoice and generate next eligible month")
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	// Worker projections may be absent during recovery; effective entitlement
	// must still come from corrected agreement facts rather than this cache.
	_, err = pool.Exec(ctx, `DELETE FROM lease_billing_period_dispositions WHERE lease_id=$1`, id)
	historyCheck(t, err)
	body := requestBody(t, web, cookie, "GET", "/leases/report?as_of="+february, 200)
	assertContains(t, body, "monthly due "+february+": Monthly rent begins in the month after corrected commencement")
	assertNotContains(t, body, "monthly due "+february+": ungenerated")
	assertContains(t, requestBody(t, web, cookie, "GET", "/leases/report?as_of="+february+"&billing=ungenerated", 200), "0 matching Leases")
}
