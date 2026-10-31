package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestLeaseNoticeProduction(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Notice customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Notice equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Retained equipment").SaveX(ctx)
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	start := today.AddDate(0, -2, 0).Format(time.DateOnly)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{StartsOn: start, EndsOn: today.AddDate(1, 0, 0).Format(time.DateOnly), PremisesID: &l.ID, AssetIDs: []int64{a.ID}}))
	zero, rent := int64(0), int64(3200)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: start}))
	role, err := svc.SaveRole(ctx, actor, 0, "Notice base writer", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.manage", Scope: authorization.All}})
	historyCheck(t, err)
	user := client.User.Create().SetCompanyID(company).SetName("Notice writer").SetEmail("notice69@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, actor, user.ID, role))
	writer := authorization.Actor{UserID: user.ID}
	in := authorization.LeaseNoticeInput{Action: "create", Category: "customer_early", Initiator: "customer", NoticeOn: today.Format(time.DateOnly), EffectiveOn: today.Format(time.DateOnly), Reason: "Negotiated inclusive end", OperationKey: "notice-production-0001"}
	before := historyDurableRows(t, pool, []string{"invoices", "assets", "asset_association_periods"})
	historyCheck(t, svc.ChangeLeaseNotice(ctx, writer, lease.ID, in))
	historyCheck(t, svc.ChangeLeaseNotice(ctx, writer, lease.ID, in))
	review, err := svc.ReviewLeaseNotice(ctx, writer, lease.ID)
	historyCheck(t, err)
	historyEqual(t, "lost-response replay", len(review.History), 1)
	historyEqual(t, "inclusive end remains in force", review.Base.Lifecycle, "active")
	historyEqual(t, "notice preserves Invoice and placement", historyDurableRows(t, pool, []string{"invoices", "assets", "asset_association_periods"}), before)
	if err = svc.LeaseLifecycle(ctx, actor, lease.ID, false); err == nil {
		t.Fatal("pending notice allowed archive")
	}
	in.Action = "withdraw"
	in.ExpectedRevision = review.Revision
	in.OperationKey = "notice-production-0002"
	in.Reason = "Customer withdrew notice"
	historyCheck(t, svc.ChangeLeaseNotice(ctx, writer, lease.ID, in))
	var open int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_asset_dependencies WHERE lease_id=$1 AND ends_on IS NULL`, lease.ID).Scan(&open))
	historyEqual(t, "withdraw restores coverage", open, 1)
	t.Run("fenced grant denial and audit rollback", func(t *testing.T) {
		cookie := sessionCookie(t, ctx, sessions, user.ID)
		review, err := svc.ReviewLeaseNotice(ctx, writer, lease.ID)
		historyCheck(t, err)
		values := url.Values{"action": {"create"}, "category": {"customer_early"}, "initiator": {"customer"}, "notice_on": {today.Format(time.DateOnly)}, "effective_on": {today.AddDate(0, 0, 5).Format(time.DateOnly)}, "reason": {"Private proposal"}, "operation_key": {"notice-fenced-000001"}, "expected_revision": {fmt.Sprint(review.Revision)}}
		tables := []string{"lease_notice_revisions", "lease_asset_dependencies", "lease_master_dependencies", "lease_billing_source_changes", "lease_invoice_review_flags", "invoices", "assets", "authorization_audit"}
		before := historyDurableRows(t, pool, tables)
		for _, key := range []string{"lease.view", "lease.manage"} {
			queuedChildPost(t, pool, web, cookie, fmt.Sprintf("/leases/%d/notice", lease.ID), `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, []any{role, key}, 403, values)
			_, err = pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,$2,'A')`, role, key)
			historyCheck(t, err)
		}
		_, err = pool.Exec(ctx, `CREATE FUNCTION reject_notice_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'lease.notice.%' THEN RAISE EXCEPTION 'injected notice audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_notice_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_notice_audit()`)
		historyCheck(t, err)
		fieldPost(t, web, cookie, fmt.Sprintf("/leases/%d/notice", lease.ID), values, 409)
		_, err = pool.Exec(ctx, `DROP TRIGGER reject_notice_audit ON authorization_audit`)
		historyCheck(t, err)
		historyEqual(t, "denials and audit failure are all-or-zero", historyDurableRows(t, pool, tables), before)
	})
	t.Run("browser recovery", func(t *testing.T) {
		if os.Getenv("BROWSER_TESTS") != "1" {
			t.Skip("BROWSER_TESTS=1 required")
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/test-snapshot", func(w http.ResponseWriter, r *http.Request) {
			var count int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_notice_revisions WHERE lease_id=$1`, lease.ID).Scan(&count))
			fmt.Fprint(w, count)
		})
		mux.HandleFunc("/test-revoke", func(w http.ResponseWriter, r *http.Request) {
			_, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.manage'`, role)
			historyCheck(t, err)
		})
		defer func() {
			_, err := pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'lease.manage','A') ON CONFLICT DO NOTHING`, role)
			historyCheck(t, err)
		}()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
		server := httptest.NewServer(mux)
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
		defer cancel()
		output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-notice.cjs", server.URL, sessionCookie(t, ctx, sessions, user.ID).Value, fmt.Sprint(lease.ID), today.Format(time.DateOnly)).CombinedOutput()
		t.Logf("notice browser: %s", output)
		historyCheck(t, err)
	})
	if err = svc.ChangeLeaseNotice(ctx, authorization.Actor{UserID: admin, Channel: "api"}, lease.ID, in); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("API denial: %v", err)
	}
}

func TestLeaseNoticeHTTPBillingAndCancellation(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	clock := func(day string) {
		t.Helper()
		_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '`+day+`'::date $$`)
		historyCheck(t, err)
	}
	clock("2024-06-01")
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Notice billing").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Notice billing equipment").SaveX(ctx)
	create := func(start, end string) int64 {
		t.Helper()
		a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Equipment").SaveX(ctx)
		lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
		historyCheck(t, err)
		historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{StartsOn: start, EndsOn: end, PremisesID: &l.ID, AssetIDs: []int64{a.ID}}))
		zero, rent := int64(0), int64(3200)
		historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
		historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: start}))
		return lease.ID
	}
	id := create("2024-01-01", "2024-12-31")
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/leases/%d/notice", id)
	assertContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/%d", id), 200), "Notice and termination")
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Current saved facts")
	var july string
	historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(i)::text FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.due_on='2024-07-01'`, id).Scan(&july))
	clock("2024-06-10")
	values := url.Values{"action": {"create"}, "category": {"customer_early"}, "initiator": {"customer"}, "notice_on": {"2024-06-10"}, "effective_on": {"2024-06-30"}, "reason": {"Agreed June ending"}, "operation_key": {"notice-http-billing-0001"}, "expected_revision": {"0"}}
	fieldPost(t, web, cookie, path, values, 303)
	fieldPost(t, web, cookie, path, values, 303)
	var after string
	historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(i)::text FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.due_on='2024-07-01'`, id).Scan(&after))
	historyEqual(t, "July Invoice remains unchanged", after, july)
	var flagged bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_invoice_review_flags f JOIN lease_invoice_generations g ON g.invoice_id=f.invoice_id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.due_on='2024-07-01')`, id).Scan(&flagged))
	historyEqual(t, "July Invoice flagged", flagged, true)
	values.Set("operation_key", "notice-http-stale-0001")
	values.Set("reason", "Retained stale proposal")
	body := fieldPost(t, web, cookie, path, values, 409)
	assertContains(t, body.Body.String(), "Revise against current notice")
	assertContains(t, body.Body.String(), "Retained stale proposal")
	clock("2024-06-30")
	review, err := svc.ReviewLeaseNotice(ctx, actor, id)
	historyCheck(t, err)
	historyEqual(t, "inclusive day", review.Base.Lifecycle, "active")
	clock("2024-07-01")
	review, err = svc.ReviewLeaseNotice(ctx, actor, id)
	historyCheck(t, err)
	historyEqual(t, "following date", review.Base.Lifecycle, "terminated")
	clock("2024-08-01")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var later int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.due_on>'2024-07-01'`, id).Scan(&later))
	historyEqual(t, "later unowed periods stop", later, 0)
	historyCheck(t, svc.LeaseLifecycle(ctx, actor, id, false))
	historyCheck(t, svc.LeaseLifecycle(ctx, actor, id, true))
	clock("2024-06-10")
	cancelled := create("2024-06-20", "2025-06-19")
	before := historyDurableRows(t, pool, []string{"invoices", "assets", "asset_association_periods"})
	cancel := authorization.LeaseNoticeInput{Action: "create", Category: "cancel_before_start", Initiator: "mutual", NoticeOn: "2024-06-10", Reason: "Agreement cancelled before commencement", OperationKey: "notice-cancel-start-0001"}
	historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, cancelled, cancel))
	review, err = svc.ReviewLeaseNotice(ctx, actor, cancelled)
	historyCheck(t, err)
	historyEqual(t, "immediate cancellation", review.Base.Lifecycle, "terminated")
	historyEqual(t, "no commencement coverage", len(review.Base.Coverage), 0)
	historyEqual(t, "cancellation preserves generated and operational facts", historyDurableRows(t, pool, []string{"invoices", "assets", "asset_association_periods"}), before)
	clock("2024-09-01")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var generated int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.kind='monthly' AND o.due_on>'2024-07-01'`, cancelled).Scan(&generated))
	historyEqual(t, "cancellation stops later billing while retaining the advance July Invoice", generated, 0)
	clock("2024-06-10")
	for _, category := range []struct{ category, initiator, notice, end string }{
		{"customer_ordinary", "customer", "2024-05-31", "2024-06-30"},
		{"company_ordinary", "company", "2024-06-10", "2024-07-31"},
		{"customer_early", "customer", "2024-06-10", "2024-06-15"},
		{"company_default", "company", "2024-06-10", "2024-06-15"},
		{"mutual_early", "mutual", "2024-06-10", "2024-06-15"},
		{"other_early", "other", "2024-06-10", "2024-06-15"},
	} {
		t.Run(category.category, func(t *testing.T) {
			id := create("2024-01-01", "2024-06-30")
			input := authorization.LeaseNoticeInput{Action: "create", Category: category.category, Initiator: category.initiator, NoticeOn: category.notice, EffectiveOn: "2024-06-15", Reason: "Documented category", OperationKey: "category-notice-" + category.category}
			historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, id, input))
			review, err := svc.ReviewLeaseNotice(ctx, actor, id)
			historyCheck(t, err)
			historyEqual(t, "eligible inclusive end", review.History[0].EffectiveOn, category.end)
		})
	}
	t.Run("coverage conflict repair and concurrent notices", func(t *testing.T) {
		id := create("2024-01-01", "2024-12-31")
		incoming := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Scheduled addition").SaveX(ctx)
		rent := int64(1200)
		coverage := authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "add", EffectiveOn: "2024-08-01", IncomingID: incoming.ID}, Action: "create", Reason: "Scheduled addition", OperationKey: "notice-later-coverage-0001", RentCents: &rent, TaxRate: "0"}
		_, err := svc.ChangeLeaseCoverage(ctx, actor, id, coverage)
		historyCheck(t, err)
		input := authorization.LeaseNoticeInput{Action: "create", Category: "mutual_early", Initiator: "mutual", NoticeOn: "2024-06-10", EffectiveOn: "2024-06-30", Reason: "End after explicit repair", OperationKey: "notice-conflict-repair-0001"}
		tables := []string{"lease_notice_revisions", "lease_coverage_revisions", "lease_asset_dependencies", "lease_master_dependencies", "lease_billing_source_changes", "lease_invoice_review_flags", "invoices", "authorization_audit"}
		before := historyDurableRows(t, pool, tables)
		if err = svc.ChangeLeaseNotice(ctx, actor, id, input); err == nil {
			t.Fatal("later coverage did not block termination")
		}
		historyEqual(t, "rejected termination is all-or-zero", historyDurableRows(t, pool, tables), before)
		review, err := svc.ReviewLeaseCoverage(ctx, actor, id)
		historyCheck(t, err)
		coverage = review.Events[0].LeaseCoverageInput
		coverage.Action = "withdraw"
		coverage.ExpectedRevision = review.Revision
		coverage.OperationKey = "notice-coverage-repair-0001"
		coverage.Reason = "Withdraw later action before termination"
		_, err = svc.ChangeLeaseCoverage(ctx, actor, id, coverage)
		historyCheck(t, err)
		results := make(chan error, 2)
		for _, key := range []string{"notice-concurrent-writer-01", "notice-concurrent-writer-02"} {
			go func(key string) {
				proposal := input
				proposal.OperationKey = key
				results <- svc.ChangeLeaseNotice(ctx, actor, id, proposal)
			}(key)
		}
		passed, conflicted := 0, 0
		for range 2 {
			err = <-results
			if err == nil {
				passed++
			} else if errors.Is(err, authorization.ErrConflict) {
				conflicted++
			} else {
				t.Fatal(err)
			}
		}
		historyEqual(t, "one concurrent notice wins", passed, 1)
		historyEqual(t, "other notice requires explicit review", conflicted, 1)
	})
}
