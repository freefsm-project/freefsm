package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseTerminationCorrectionRestorationGuards(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	cookie := sessionCookie(t, ctx, sessions, admin)
	id, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	base, err := svc.DraftLeaseBase(ctx, actor, id)
	historyCheck(t, err)
	var today string
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)::text`, company).Scan(&today))
	n := authorization.LeaseNoticeInput{Action: "create", Category: "cancel_before_start", Initiator: "mutual", NoticeOn: today, Reason: "Recorded cancellation", OperationKey: "correction-cancel-original-0001"}
	historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, id, n))
	review, err := svc.ReviewLeaseNotice(ctx, actor, id)
	historyCheck(t, err)
	n.Action = "withdraw"
	n.ExpectedRevision = review.Revision
	n.Factual = true
	n.Reason = "Wrong agreement selected"
	n.OperationKey = "correction-cancel-withdraw-0001"
	_, err = pool.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, base.CustomerID)
	historyCheck(t, err)
	if err = svc.ChangeLeaseNotice(ctx, actor, id, n); err == nil {
		t.Fatal("reactivated archived Customer")
	}
	_, err = pool.Exec(ctx, `UPDATE customers SET deleted_at=NULL WHERE id=$1`, base.CustomerID)
	historyCheck(t, err)
	other, err := svc.RegisterDraftLease(ctx, actor, base.CustomerID)
	historyCheck(t, err)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, other.ID, authorization.DraftLeaseDetailsInput{StartsOn: base.StartsOn, EndsOn: base.EndsOn, PremisesID: base.PremisesID, AssetIDs: base.AssetIDs}))
	zero, rent := int64(0), int64(100)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, other.ID, 0, base.StartsOn, authorization.DraftLeaseTerms{AssetID: base.AssetIDs[0], InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, other.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: base.StartsOn}))
	tables := []string{"leases", "lease_notice_revisions", "lease_asset_dependencies", "lease_master_dependencies", "lease_billing_source_changes", "lease_invoice_review_flags", "invoices", "assets", "authorization_audit"}
	before := historyDurableRows(t, pool, tables)
	path := fmt.Sprintf("/leases/%d/termination-correction", id)
	values := url.Values{"expected_revision": {fmt.Sprint(n.ExpectedRevision)}, "action": {n.Action}, "category": {n.Category}, "initiator": {n.Initiator}, "notice_on": {n.NoticeOn}, "reason": {n.Reason}, "operation_key": {n.OperationKey}, "factual": {"true"}}
	response := fieldPost(t, web, cookie, path, values, 409)
	assertContains(t, response.Body.String(), "transaction was rolled back")
	assertContains(t, response.Body.String(), n.Reason)
	historyEqual(t, "overlap rejects whole correction", historyDurableRows(t, pool, tables), before)
	historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, other.ID, authorization.LeaseNoticeInput{Action: "create", Category: "cancel_before_start", Initiator: "mutual", NoticeOn: today, Reason: "Release reservation", OperationKey: "correction-cancel-other-0001"}))
	fieldPost(t, web, cookie, path, values, 303)
	current, err := svc.DraftLeaseBase(ctx, actor, id)
	historyCheck(t, err)
	historyEqual(t, "factual correction restores Scheduled", current.Lifecycle, "scheduled")
	historyEqual(t, "restored original Asset", current.Coverage[0].AssetID, base.AssetIDs[0])
}
