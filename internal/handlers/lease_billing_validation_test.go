package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseBillingRevisionGeneratedCoverageValidation(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	clock := func(date string) {
		t.Helper()
		_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '`+date+`'::date $$`)
		historyCheck(t, err)
	}
	clock("2023-12-02") // The shared HTTP fixture starts thirty days later: January 1.
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	var a, typ int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT a.id,a.asset_type_id FROM assets a JOIN lease_asset_dependencies d ON d.asset_id=a.id WHERE d.lease_id=$1`, lease).Scan(&a, &typ))
	b := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("January addition").SaveX(ctx)
	c := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Valid future addition").SaveX(ctx)
	path := fmt.Sprintf("/leases/%d/coverage", lease)
	add := func(asset int64, date, rate, key string) url.Values {
		t.Helper()
		review, err := svc.ReviewLeaseCoverage(ctx, actor, lease)
		historyCheck(t, err)
		return url.Values{"action": {"create"}, "kind": {"add"}, "effective_on": {date}, "incoming_id": {fmt.Sprint(asset)}, "rent_cents": {"1000"}, "tax_rate": {rate}, "reason": {"Generated history does not reprice"}, "operation_key": {key}, "expected_revision": {fmt.Sprint(review.Revision)}}
	}
	fieldPost(t, web, cookie, path, add(b.ID, "2024-01-10", "5", "validation-january-add-0001"), 303)
	clock("2024-03-01")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var months int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.kind='monthly' AND o.due_on IN ('2024-02-01','2024-03-01')`, lease).Scan(&months))
	historyEqual(t, "February and March generated", months, 2)
	tables := []string{"invoices", "lease_invoice_generations", "lease_finalizations"}
	generated := historyDurableRows(t, pool, tables)
	historical := func() string {
		t.Helper()
		var rows string
		historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object('invoice',to_jsonb(i),'generation',to_jsonb(g),'obligation',to_jsonb(o)) ORDER BY o.id)::text FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.due_on<'2024-04-01'`, lease).Scan(&rows))
		return rows
	}
	originalHistory := historical()
	revise := func(date, rate, key string, assets ...int64) {
		t.Helper()
		review, err := svc.ReviewLeaseBillingRevisions(ctx, actor, lease)
		historyCheck(t, err)
		values := url.Values{"expected_revision": {fmt.Sprint(review.Revision)}, "effective_on": {date}, "reason": {"Approved effective tax"}, "operation_key": {key}}
		for _, asset := range assets {
			values.Add("asset_id", fmt.Sprint(asset))
			rent := "1000"
			if asset == a {
				rent = "5000"
			}
			values.Set(fmt.Sprintf("rent_cents_%d", asset), rent)
			values.Set(fmt.Sprintf("tax_rate_%d", asset), rate)
		}
		fieldPost(t, web, cookie, fmt.Sprintf("/leases/%d/billing/revisions", lease), values, 303)
	}
	revise("2024-04-01", "9", "validation-april-batch-0001", a, b.ID)
	revise("2024-02-01", "7", "validation-retroactive-0001", a)
	historyEqual(t, "revisions retain generated snapshots", historyDurableRows(t, pool, tables), generated)
	valid := add(c.ID, "2024-05-10", "9", "validation-may-add-0001")
	fieldPost(t, web, cookie, path, valid, 303)
	historyEqual(t, "future coverage retains generated snapshots", historyDurableRows(t, pool, tables), generated)
	all := []string{"lease_coverage_events", "lease_coverage_revisions", "lease_asset_dependencies", "lease_master_dependencies", "lease_billing_revisions", "lease_billing_source_changes", "lease_invoice_review_flags", "authorization_audit", "invoices", "lease_invoice_generations"}
	accepted := historyDurableRows(t, pool, all)
	fieldPost(t, web, cookie, path, valid, 303)
	historyEqual(t, "exact coverage replay", historyDurableRows(t, pool, all), accepted)
	d := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Conflicting pending addition").SaveX(ctx)
	invalid := add(d.ID, "2024-05-20", "7", "validation-pending-conflict-0001")
	for range 2 {
		body := fieldPost(t, web, cookie, path, invalid, 409).Body.String()
		assertContains(t, body, "common nonzero tax rate")
		historyEqual(t, "invalid pending change is atomic", historyDurableRows(t, pool, all), accepted)
	}
	clock("2024-05-02")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var total string
	var lines, revised int
	historyCheck(t, pool.QueryRow(ctx, `SELECT i.financial::jsonb->'result'->>'total_cents',jsonb_array_length(i.line_items),(SELECT count(*) FROM jsonb_array_elements(i.line_items) l WHERE (l->>'lease_billing_revision_id')::bigint=(SELECT id FROM lease_billing_revisions WHERE operation_key='validation-april-batch-0001')) FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.kind='monthly' AND o.due_on='2024-06-01'`, lease).Scan(&total, &lines, &revised))
	historyEqual(t, "full future rent at nine percent", total, "7630")
	historyEqual(t, "three covered Asset lines", lines, 3)
	historyEqual(t, "batch revision provenance on A and B", revised, 2)
	historyEqual(t, "original generated commercial and provenance facts survive future generation", historical(), originalHistory)
	var assets []int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT array_agg((l->>'asset_id')::bigint ORDER BY (l->>'asset_id')::bigint) FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id CROSS JOIN LATERAL jsonb_array_elements(i.line_items) l WHERE o.lease_id=$1 AND o.kind='monthly' AND o.due_on='2024-06-01' AND (l->>'lease_id')::bigint=$1 AND l->>'period'='2024-06-01'`, lease).Scan(&assets))
	historyEqual(t, "future Lease period Asset provenance", assets, []int64{a, b.ID, c.ID})
	final := historyDurableRows(t, pool, tables)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyEqual(t, "generation replay", historyDurableRows(t, pool, tables), final)
}

func TestLeaseCoverageGeneratedOptionalChargeValidation(t *testing.T) {
	for _, generated := range []bool{false, true} {
		t.Run(fmt.Sprintf("generated=%t", generated), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			svc := authorization.New(pool)
			actor := authorization.Actor{UserID: admin}
			_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2023-12-02'::date $$`)
			historyCheck(t, err)
			cookie := sessionCookie(t, ctx, sessions, admin)
			lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
			_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
			historyCheck(t, err)
			// January is the last in-force month. Neither Add has an owed recurring
			// period, so only their grouped optional charge can be incompatible.
			historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease, authorization.LeaseNoticeInput{Action: "create", Category: "mutual_early", Initiator: "mutual", NoticeOn: "2024-01-01", EffectiveOn: "2024-01-31", Reason: "Last month equipment", OperationKey: "validation-optional-end-0001"}))
			var typ int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT a.asset_type_id FROM assets a JOIN lease_asset_dependencies d ON d.asset_id=a.id WHERE d.lease_id=$1`, lease).Scan(&typ))
			b := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("First optional charge").SaveX(ctx)
			c := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Second optional charge").SaveX(ctx)
			path := fmt.Sprintf("/leases/%d/coverage", lease)
			values := url.Values{"action": {"create"}, "kind": {"add"}, "effective_on": {"2024-01-10"}, "incoming_id": {fmt.Sprint(b.ID)}, "rent_cents": {"1000"}, "charge_cents": {"2000"}, "tax_rate": {"5"}, "reason": {"Optional charge"}, "operation_key": {"validation-optional-first-0001"}, "expected_revision": {"0"}}
			fieldPost(t, web, cookie, path, values, 303)
			if generated {
				historyCheck(t, svc.GenerateLeaseDrafts(ctx))
				var total string
				historyCheck(t, pool.QueryRow(ctx, `SELECT i.financial::jsonb->'result'->>'total_cents' FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.kind='coverage' AND o.due_on='2024-01-10'`, lease).Scan(&total))
				historyEqual(t, "optional charge generated at five percent", total, "2100")
			}
			review, err := svc.ReviewLeaseCoverage(ctx, actor, lease)
			historyCheck(t, err)
			values.Set("expected_revision", fmt.Sprint(review.Revision))
			values.Set("incoming_id", fmt.Sprint(c.ID))
			values.Set("tax_rate", "7")
			values.Set("operation_key", "validation-optional-second-0001")
			tables := []string{"lease_coverage_events", "lease_coverage_revisions", "lease_asset_dependencies", "lease_master_dependencies", "lease_billing_revisions", "lease_billing_source_changes", "lease_invoice_review_flags", "authorization_audit", "lease_billing_obligations", "lease_billing_obligation_revisions", "invoices", "lease_invoice_generations"}
			before := leasePreservationRows(t, pool, tables)
			original := historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations"})
			if !generated {
				for range 2 {
					assertContains(t, fieldPost(t, web, cookie, path, values, 409).Body.String(), "common nonzero tax rate")
					historyEqual(t, "ungenerated optional conflict is atomic", leasePreservationRows(t, pool, tables), before)
				}
				values.Set("tax_rate", "5")
			}
			fieldPost(t, web, cookie, path, values, 303)
			accepted := leasePreservationRows(t, pool, tables)
			fieldPost(t, web, cookie, path, values, 303)
			historyEqual(t, "optional coverage exact replay", leasePreservationRows(t, pool, tables), accepted)
			historyCheck(t, svc.GenerateLeaseDrafts(ctx))
			if generated {
				historyEqual(t, "consumed optional snapshot is never repriced or regenerated", historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations"}), original)
				var flagged bool
				historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_invoice_review_flags r JOIN lease_invoice_generations g ON g.invoice_id=r.invoice_id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.kind='coverage')`, lease).Scan(&flagged))
				historyEqual(t, "generated optional charge requires manual review", flagged, true)
			} else {
				var total string
				historyCheck(t, pool.QueryRow(ctx, `SELECT i.financial::jsonb->'result'->>'total_cents' FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.kind='coverage'`, lease).Scan(&total))
				historyEqual(t, "valid pending grouped charges generate", total, "4200")
			}
			// Even an exempt Customer and a consumed obligation cannot hide malformed
			// raw Add terms when no recurring month remains before termination.
			var customer int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT customer_id FROM leases WHERE id=$1`, lease).Scan(&customer))
			client.Customer.UpdateOneID(customer).SetTaxExempt(true).SaveX(ctx)
			d := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Invalid raw terms").SaveX(ctx)
			review, err = svc.ReviewLeaseCoverage(ctx, actor, lease)
			historyCheck(t, err)
			values.Set("expected_revision", fmt.Sprint(review.Revision))
			values.Set("incoming_id", fmt.Sprint(d.ID))
			values.Set("operation_key", "validation-optional-invalid-0001")
			before = leasePreservationRows(t, pool, tables)
			for _, invalid := range []struct{ rent, charge, tax string }{{"1000", "2000", "101"}, {"-1", "2000", "5"}, {"1000", "-1", "5"}} {
				values.Set("rent_cents", invalid.rent)
				values.Set("charge_cents", invalid.charge)
				values.Set("tax_rate", invalid.tax)
				fieldPost(t, web, cookie, path, values, 409)
				historyEqual(t, "raw invalidity rolls back despite generated exemption", leasePreservationRows(t, pool, tables), before)
			}
			var revisions int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_revisions WHERE lease_id=$1`, lease).Scan(&revisions))
			historyEqual(t, "no artificial billing revisions needed", revisions, 0)
		})
	}
}
