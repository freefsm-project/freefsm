package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestLeaseFactCorrectionPersistence(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Correction customer").SaveX(ctx)
	location := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Original premises").SaveX(ctx)
	replacement := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Correct premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Correction equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Retained equipment").SaveX(ctx)
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	start := today.AddDate(0, -2, 0).Format(time.DateOnly)
	end := today.AddDate(1, 0, 0).Format(time.DateOnly)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{StartsOn: start, EndsOn: end, PremisesID: &location.ID, AssetIDs: []int64{asset.ID}}))
	zero, rent := int64(0), int64(3200)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start, authorization.DraftLeaseTerms{AssetID: asset.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: start}))
	before := historyDurableRows(t, pool, []string{"invoices", "assets", "asset_association_periods", "lease_finalizations", "lease_invoice_generations"})
	current, err := svc.DraftLeaseBase(ctx, actor, lease.ID)
	historyCheck(t, err)
	in := authorization.LeaseFactInput{ExpectedRevision: current.Revision, StartsOn: today.AddDate(0, -1, 0).Format(time.DateOnly), EndsOn: end, PremisesID: replacement.ID, Factual: true, Reason: "Transcription error", OperationKey: "correction-persistence-0001"}
	historyCheck(t, svc.CorrectLeaseFacts(ctx, actor, lease.ID, in))
	historyCheck(t, svc.CorrectLeaseFacts(ctx, actor, lease.ID, in))
	base, err := svc.DraftLeaseBase(ctx, actor, lease.ID)
	historyCheck(t, err)
	historyEqual(t, "corrected start", base.StartsOn, in.StartsOn)
	historyEqual(t, "shifted initial coverage", base.Coverage[0].StartsOn, in.StartsOn)
	historyEqual(t, "preserved identity", base.Number, lease.Number)
	historyEqual(t, "physical and financial originals", historyDurableRows(t, pool, []string{"invoices", "assets", "asset_association_periods", "lease_finalizations", "lease_invoice_generations"}), before)
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_fact_corrections WHERE lease_id=$1`, lease.ID).Scan(&count))
	historyEqual(t, "one immutable event", count, 1)
	in.OperationKey = "correction-persistence-0002"
	in.ExpectedRevision = base.Revision
	in.StartsOn = today.AddDate(0, -3, 0).Format(time.DateOnly)
	if err = svc.CorrectLeaseFacts(ctx, actor, lease.ID, in); err == nil {
		t.Fatal("start before initial due accepted")
	}
	t.Run("production HTTP rollback and API denial", func(t *testing.T) {
		cookie := sessionCookie(t, ctx, sessions, admin)
		path := fmt.Sprintf("/leases/%d/correction", lease.ID)
		assertContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/%d", lease.ID), 200), "Correct agreement facts")
		assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Correct premises")
		values := url.Values{"expected_revision": {fmt.Sprint(base.Revision)}, "starts_on": {base.StartsOn}, "ends_on": {base.EndsOn}, "premises_id": {fmt.Sprint(replacement.ID)}, "reason": {"Retained failed correction"}, "factual": {"true"}, "operation_key": {"fact-http-rollback-0001"}}
		tables := []string{"leases", "lease_fact_corrections", "lease_asset_dependencies", "lease_master_dependencies", "lease_billing_source_changes", "lease_invoice_review_flags", "authorization_audit"}
		snapshot := historyDurableRows(t, pool, tables)
		_, err := pool.Exec(ctx, `CREATE FUNCTION reject_fact_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.facts.correct' THEN RAISE EXCEPTION 'private injected failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_fact_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_fact_audit()`)
		historyCheck(t, err)
		response := fieldPost(t, web, cookie, path, values, 409)
		assertContains(t, response.Body.String(), "transaction was rolled back")
		assertContains(t, response.Body.String(), "Retained failed correction")
		assertNotContains(t, response.Body.String(), "private injected failure")
		historyEqual(t, "atomic rollback", historyDurableRows(t, pool, tables), snapshot)
		_, err = pool.Exec(ctx, `DROP TRIGGER reject_fact_audit ON authorization_audit`)
		historyCheck(t, err)
		api := actor
		api.Channel = "api"
		if err = svc.CorrectLeaseFacts(ctx, api, lease.ID, in); err == nil {
			t.Fatal("field API accepted Lease correction")
		}
	})
	t.Run("browser stale compare revise discard", func(t *testing.T) {
		if os.Getenv("BROWSER_TESTS") != "1" {
			t.Skip("BROWSER_TESTS=1 required")
		}
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
		server := httptest.NewServer(mux)
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-correction.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, fmt.Sprint(lease.ID)).CombinedOutput()
		t.Logf("correction browser: %s", output)
		historyCheck(t, err)
	})
	t.Run("ended factual termination restores only validated history", func(t *testing.T) {
		n := authorization.LeaseNoticeInput{Action: "create", Category: "mutual_early", Initiator: "mutual", NoticeOn: base.StartsOn, EffectiveOn: today.AddDate(0, 0, -1).Format(time.DateOnly), Reason: "Recorded end", OperationKey: "fact-ended-termination-0001"}
		historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease.ID, n))
		review, err := svc.ReviewLeaseNotice(ctx, actor, lease.ID)
		historyCheck(t, err)
		historyEqual(t, "ended", review.Base.Lifecycle, "terminated")
		n.Action = "withdraw"
		n.ExpectedRevision = review.Revision
		n.OperationKey = "fact-ended-termination-0002"
		n.Reason = "Termination entered against wrong agreement"
		if err = svc.ChangeLeaseNotice(ctx, actor, lease.ID, n); err == nil {
			t.Fatal("ordinary notice undid termination")
		}
		n.Factual = true
		historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease.ID, n))
		historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease.ID, n))
		review, err = svc.ReviewLeaseNotice(ctx, actor, lease.ID)
		historyCheck(t, err)
		historyEqual(t, "restored lifecycle", review.Base.Lifecycle, "active")
		historyEqual(t, "retained both facts", len(review.History), 2)
		historyEqual(t, "preserved originals", historyDurableRows(t, pool, []string{"invoices", "assets", "asset_association_periods", "lease_finalizations", "lease_invoice_generations"}), before)
	})
	t.Run("base-only authority fenced denial concurrent revisions immutable history", func(t *testing.T) {
		role, err := svc.SaveRole(ctx, actor, 0, "Base correction writer", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.manage", Scope: authorization.All}})
		historyCheck(t, err)
		user := client.User.Create().SetCompanyID(company).SetName("Correction writer").SetEmail("correction70@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
		historyCheck(t, svc.AssignUser(ctx, actor, user.ID, role))
		writer := authorization.Actor{UserID: user.ID}
		current, err := svc.DraftLeaseBase(ctx, writer, lease.ID)
		historyCheck(t, err)
		proposal := authorization.LeaseFactInput{ExpectedRevision: current.Revision, StartsOn: current.StartsOn, EndsOn: current.EndsOn, PremisesID: *current.PremisesID, Factual: true, Reason: "Base-authorized factual correction", OperationKey: "base-facts-concurrency-0001"}
		values := url.Values{"expected_revision": {fmt.Sprint(proposal.ExpectedRevision)}, "starts_on": {proposal.StartsOn}, "ends_on": {proposal.EndsOn}, "premises_id": {fmt.Sprint(proposal.PremisesID)}, "reason": {proposal.Reason}, "factual": {"true"}, "operation_key": {proposal.OperationKey}}
		cookie := sessionCookie(t, ctx, sessions, user.ID)
		queuedChildPost(t, pool, web, cookie, fmt.Sprintf("/leases/%d/correction", lease.ID), `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.manage'`, []any{role}, 403, values)
		_, err = pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'lease.manage','A')`, role)
		historyCheck(t, err)
		results := make(chan error, 2)
		for i := range 2 {
			go func(i int) {
				p := proposal
				p.OperationKey = fmt.Sprintf("base-concurrent-distinct-%d", i)
				results <- svc.CorrectLeaseFacts(ctx, writer, lease.ID, p)
			}(i)
		}
		successes := 0
		for range 2 {
			if <-results == nil {
				successes++
			}
		}
		historyEqual(t, "one winner for stale competing proposals", successes, 1)
		for _, query := range []string{`UPDATE lease_fact_corrections SET reason='rewrite'`, `DELETE FROM lease_fact_corrections`} {
			if _, err = pool.Exec(ctx, query); err == nil {
				t.Fatal("mutable correction history")
			}
		}
		body := requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/%d/correction", lease.ID), 200)
		assertNotContains(t, body, "3200")
		assertNotContains(t, body, "rent_cents")
	})
	t.Run("ordinary notice boundary and later coverage revalidated", func(t *testing.T) {
		current, err := svc.DraftLeaseBase(ctx, actor, lease.ID)
		historyCheck(t, err)
		notices, err := svc.ReviewLeaseNotice(ctx, actor, lease.ID)
		historyCheck(t, err)
		n := authorization.LeaseNoticeInput{ExpectedRevision: notices.Revision, Action: "create", Category: "customer_ordinary", Initiator: "customer", NoticeOn: today.Format(time.DateOnly), Reason: "Ordinary notice", OperationKey: "facts-ordinary-boundary-0001"}
		historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease.ID, n))
		proposal := authorization.LeaseFactInput{ExpectedRevision: current.Revision, StartsOn: current.StartsOn, EndsOn: today.AddDate(1, 0, 1).Format(time.DateOnly), PremisesID: *current.PremisesID, Factual: true, Reason: "Mistyped fixed end", OperationKey: "facts-invalid-boundary-0001"}
		if err = svc.CorrectLeaseFacts(ctx, actor, lease.ID, proposal); err == nil {
			t.Fatal("invalidated ordinary boundary accepted")
		}
		notices, err = svc.ReviewLeaseNotice(ctx, actor, lease.ID)
		historyCheck(t, err)
		n.Action = "withdraw"
		n.ExpectedRevision = notices.Revision
		n.OperationKey = "facts-ordinary-withdraw-0001"
		historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease.ID, n))
		incoming := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Future replacement").SaveX(ctx)
		_, err = svc.ChangeLeaseCoverage(ctx, actor, lease.ID, authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "replace", EffectiveOn: today.AddDate(0, 0, 1).Format(time.DateOnly), OutgoingID: asset.ID, IncomingID: incoming.ID}, Action: "create", Reason: "Future coverage", OperationKey: "facts-future-coverage-0001"})
		historyCheck(t, err)
		proposal.StartsOn = today.AddDate(0, 0, 2).Format(time.DateOnly)
		proposal.EndsOn = current.EndsOn
		proposal.OperationKey = "facts-invalid-timeline-0001"
		if err = svc.CorrectLeaseFacts(ctx, actor, lease.ID, proposal); err == nil {
			t.Fatal("start shifted past coverage event")
		}
	})
}

func TestLeaseFactCorrectionPopulatedUpgrade(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	tables := []string{"leases", "lease_finalizations", "lease_asset_dependencies", "lease_master_dependencies", "lease_billing_obligations", "lease_invoice_generations", "invoices", "assets", "locations", "customers", "authorization_roles", "authorization_grants", "authorization_audit"}
	before := historyDurableRows(t, pool, tables)
	raw, err := fs.ReadFile(database.MigrationFS(), "121_lease_notice_history.up.sql")
	historyCheck(t, err)
	definition := regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION lease_finalized_draft_guard\(.*?\$\$;`).FindString(string(raw))
	if definition == "" {
		t.Fatal("missing predecessor guard")
	}
	_, err = pool.Exec(ctx, definition+`; DROP TABLE lease_fact_corrections; DELETE FROM schema_migrations WHERE name='122_lease_fact_corrections'`)
	historyCheck(t, err)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "upgrade retains installed identities facts and grants", historyDurableRows(t, pool, tables), before)
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_fact_corrections`).Scan(&count))
	historyEqual(t, "no invented corrections", count, 0)
}
