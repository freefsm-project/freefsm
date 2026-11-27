package authorization_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type leaseExportProjectionFixture struct {
	commercialReportFixture
	lease, asset, premises int64
	today                  time.Time
}

func newLeaseExportProjectionFixture(t *testing.T) leaseExportProjectionFixture {
	return newLeaseExportProjectionFixtureSchedule(t, -10, -10)
}

func newLeaseExportProjectionFixtureSchedule(t *testing.T, startOffset, dueOffset int) leaseExportProjectionFixture {
	t.Helper()
	f := leaseExportProjectionFixture{commercialReportFixture: newCommercialReportFixture(t)}
	f.actor.Channel = "web"
	ctx := context.Background()
	financeCheck(t, f.pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, f.company).Scan(&f.today))
	financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO locations(company_id,object_type,object_id,title) VALUES($1,'customer',$2,'Lease premises') RETURNING id`, f.company, f.customer).Scan(&f.premises))
	var typ int64
	financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO asset_types(company_id,name) VALUES($1,'Lease equipment') RETURNING id`, f.company).Scan(&typ))
	financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO assets(company_id,asset_type_id,ownership_class,name) VALUES($1,$2,'company_owned','Lease asset') RETURNING id`, f.company, typ).Scan(&f.asset))
	lease, err := f.svc.RegisterDraftLease(ctx, f.actor, f.customer)
	financeCheck(t, err)
	f.lease = lease.ID
	start, end := f.today.AddDate(0, 0, startOffset).Format(time.DateOnly), f.today.AddDate(1, 0, 0).Format(time.DateOnly)
	financeCheck(t, f.svc.SaveDraftLeaseDetails(ctx, f.actor, f.lease, authorization.DraftLeaseDetailsInput{Title: "Projection Lease", StartsOn: start, EndsOn: end, PremisesID: &f.premises, AssetIDs: []int64{f.asset}}))
	initial, rent := int64(0), int64(12345)
	financeCheck(t, f.svc.SaveDraftLeaseBilling(ctx, f.actor, f.lease, 0, f.today.AddDate(0, 0, dueOffset).Format(time.DateOnly), authorization.DraftLeaseTerms{AssetID: f.asset, InitialCents: &initial, RentCents: &rent, TaxRate: "5"}, 0))
	financeCheck(t, f.svc.FinalizeLease(ctx, f.actor, f.lease, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: f.today.Format(time.DateOnly)}))
	return f
}

func leaseProjectionSnapshot(f leaseExportProjectionFixture, a authorization.Actor, mode authorization.LeaseExportMode, filter authorization.LeaseExportFilter) (authorization.LeaseExportSummary, []authorization.LeaseExportEntry, error) {
	var summary authorization.LeaseExportSummary
	var entries []authorization.LeaseExportEntry
	ctx := context.Background()
	err := f.svc.WithinSnapshotPermission(ctx, a, "lease.export", authorization.All, func(tx pgx.Tx, p authorization.Principal) error {
		var err error
		summary, err = authorization.LeaseExportSnapshot(ctx, tx, p, mode, filter, func(_ authorization.LeaseExportMeta, e authorization.LeaseExportEntry) error {
			entries = append(entries, e)
			return nil
		})
		return err
	})
	return summary, entries, err
}

func leaseProjectionRefCheck(f leaseExportProjectionFixture, a authorization.Actor, r authorization.LeaseExportReference) error {
	ctx := context.Background()
	return f.svc.WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		return authorization.CheckLeaseExportReference(ctx, tx, p, r.Mode, r)
	})
}

func leaseProjectionGrants(extra ...authorization.Grant) []authorization.Grant {
	return append([]authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.export", Scope: authorization.All}}, extra...)
}

func TestLeaseExportOperationalPrivilegedProjection(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	// A broken financial-only source must not even be queried by operational mode.
	_, err := f.pool.Exec(ctx, `ALTER TABLE lease_billing_revisions RENAME TO lease_export_unavailable_revisions`)
	financeCheck(t, err)
	t.Cleanup(func() {
		_, err := f.pool.Exec(ctx, `ALTER TABLE lease_export_unavailable_revisions RENAME TO lease_billing_revisions`)
		financeCheck(t, err)
	})
	summary, entries, err := leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportOperational, authorization.LeaseExportFilter{})
	financeCheck(t, err)
	if summary.LeaseCount != 1 || summary.RentCents != 0 || len(entries) != 1 {
		t.Fatalf("operational snapshot: %+v %+v", summary, entries)
	}
	e := entries[0]
	if e.Lease.RentCents != 0 || e.RentTerms != nil || e.GeneratedInvoices != nil || e.Periods != nil || e.FailureHistory != "" || e.Reference.CompleteGeneration {
		t.Fatalf("operational financial projection: %+v", e)
	}
	financeCheck(t, leaseProjectionRefCheck(f, f.actor, summary.Reference))
	_, _, err = leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportBilling, authorization.LeaseExportFilter{})
	if err == nil {
		t.Fatal("Billing did not query required financial source")
	}
	_, _, err = leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportOperational, authorization.LeaseExportFilter{Billing: "review"})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("privileged operational Billing predicate: %v", err)
	}
}

func TestLeaseExportHistoricalFailureUnavailableProjection(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	yesterday := f.today.AddDate(0, 0, -1).Format(time.DateOnly)
	_, entries, err := leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportBilling, authorization.LeaseExportFilter{AsOf: yesterday})
	financeCheck(t, err)
	if len(entries) != 1 || entries[0].FailureHistory != "unavailable" {
		t.Fatalf("failure history: %+v", entries)
	}
	for _, p := range entries[0].Periods {
		if p.State == "pending" || p.State == "failed" {
			t.Fatalf("invented historical queue: %+v", p)
		}
	}
	for _, filter := range []string{"failed", "pending"} {
		_, _, err = leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportBilling, authorization.LeaseExportFilter{AsOf: yesterday, Billing: filter})
		if !errors.Is(err, authorization.ErrConflict) {
			t.Fatalf("historical %s accepted: %v", filter, err)
		}
	}
	// Selection/recheck is time independent; only fresh acceptance uses the clock.
	financeCheck(t, f.svc.WithinActor(ctx, f.actor, func(tx pgx.Tx, p authorization.Principal) error {
		filter := authorization.LeaseExportFilter{AsOf: yesterday, Billing: "pending", Sort: "number"}
		if err := authorization.AuthorizeLeaseExportSelection(ctx, tx, p, authorization.LeaseExportBilling, filter); err != nil {
			return err
		}
		if err := authorization.ValidateLeaseExportFreshSelection(ctx, tx, p, authorization.LeaseExportBilling, filter); !errors.Is(err, authorization.ErrConflict) {
			t.Fatalf("fresh historic queue: %v", err)
		}
		return authorization.CheckLeaseExportReference(ctx, tx, p, authorization.LeaseExportBilling, authorization.LeaseExportReference{Version: 1, Mode: authorization.LeaseExportBilling, Summary: true, Filter: filter, CompleteGeneration: true, Selection: emptyLeaseProjectionSelection(), Leases: []authorization.LeaseExportLeaseReference{}})
	}))
}

func TestLeaseExportEmptySelectionAndSummaryReferencesProjection(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	reader := financeReader(t, f.commercialReportFixture, "Lease selected customer", leaseProjectionGrants(authorization.Grant{Permission: "customer.view", Scope: authorization.All}))
	summary, entries, err := leaseProjectionSnapshot(f, reader, authorization.LeaseExportOperational, authorization.LeaseExportFilter{Customer: f.customer, Search: "no such lease"})
	financeCheck(t, err)
	if len(entries) != 0 || summary.LeaseCount != 0 || summary.Reference.Leases == nil {
		t.Fatal("empty snapshot lost mandatory reference")
	}
	financeCheck(t, leaseProjectionRefCheck(f, reader, summary.Reference))
	_, err = f.pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=(SELECT authorization_role_id FROM users WHERE id=$1) AND permission='customer.view'`, reader.UserID)
	financeCheck(t, err)
	financeDenied(t, leaseProjectionRefCheck(f, reader, summary.Reference))
	_, _, err = leaseProjectionSnapshot(f, reader, authorization.LeaseExportOperational, authorization.LeaseExportFilter{Customer: f.customer, Search: "no such lease"})
	financeDenied(t, err)
	_, _, err = leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportOperational, authorization.LeaseExportFilter{Asset: 9223372036854770000, Search: "no such lease"})
	if !errors.Is(err, authorization.ErrNotFound) {
		t.Fatalf("invisible selected input: %v", err)
	}
	full, _, err := leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportOperational, authorization.LeaseExportFilter{})
	financeCheck(t, err)
	if len(full.Reference.Leases) != 1 || len(full.Reference.Leases[0].Assets) != 1 || len(full.Reference.Leases[0].Premises) != 1 {
		t.Fatalf("summary omitted contributors: %+v", full.Reference)
	}
}

func TestLeaseExportOriginalAndCurrentAssignedAnchorsProjection(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	invoice := financeInvoice(t, f.commercialReportFixture, 10000, "draft", "", 100, f.job)
	reader := financeReader(t, f.commercialReportFixture, "Lease Assigned", leaseProjectionGrants(authorization.Grant{Permission: "customer.view", Scope: authorization.All}, authorization.Grant{Permission: "customer.financial.view", Scope: authorization.All}, authorization.Grant{Permission: "lease.billing.view", Scope: authorization.All}, authorization.Grant{Permission: "invoice.view", Scope: authorization.Assigned}, authorization.Grant{Permission: "pricing.view", Scope: authorization.Assigned}, authorization.Grant{Permission: "settlement.view", Scope: authorization.All}))
	_, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, reader.UserID)
	financeCheck(t, err)
	ref := authorization.LeaseExportReference{Version: 1, Mode: authorization.LeaseExportBilling, Filter: authorization.LeaseExportFilter{Sort: "number"}, Leases: []authorization.LeaseExportLeaseReference{{ID: f.lease, Customers: []authorization.LeaseExportRelatedReference{}, Premises: []authorization.LeaseExportRelatedReference{}, Assets: []authorization.LeaseExportRelatedReference{}, Actors: []int64{}, Invoices: []authorization.LeaseExportInvoiceReference{{ID: invoice, JobID: f.job, Settlement: true}}}}}
	ref.Selection = emptyLeaseProjectionSelection()
	financeCheck(t, leaseProjectionRefCheck(f, reader, ref))
	_, err = f.pool.Exec(ctx, `UPDATE invoices SET job_id=$1 WHERE id=$2`, f.otherJob, invoice)
	financeCheck(t, err)
	financeDenied(t, leaseProjectionRefCheck(f, reader, ref))
	_, err = f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.otherJob, reader.UserID)
	financeCheck(t, err)
	financeCheck(t, leaseProjectionRefCheck(f, reader, ref))
	_, err = f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, f.job, reader.UserID)
	financeCheck(t, err)
	financeDenied(t, leaseProjectionRefCheck(f, reader, ref))
	financeCheck(t, leaseProjectionRefCheck(f, f.actor, ref)) // All Records supersedes both assignments.
}

func TestLeaseExportMalformedRequiredReferencesProjection(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	summary, _, err := leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportOperational, authorization.LeaseExportFilter{})
	financeCheck(t, err)
	for name, mutate := range map[string]func(*authorization.LeaseExportReference){
		"version":        func(r *authorization.LeaseExportReference) { r.Version = 0 },
		"mode":           func(r *authorization.LeaseExportReference) { r.Mode = "unknown" },
		"missing leases": func(r *authorization.LeaseExportReference) { r.Leases = nil },
		"empty non summary": func(r *authorization.LeaseExportReference) {
			r.Summary = false
			r.Leases = []authorization.LeaseExportLeaseReference{}
		},
		"missing actors":      func(r *authorization.LeaseExportReference) { r.Leases[0].Actors = nil },
		"invalid lease":       func(r *authorization.LeaseExportReference) { r.Leases[0].ID = -1 },
		"invalid asset":       func(r *authorization.LeaseExportReference) { r.Leases[0].Assets[0].ID = 0 },
		"duplicate lease":     func(r *authorization.LeaseExportReference) { r.Leases = append(r.Leases, r.Leases[0]) },
		"operational finance": func(r *authorization.LeaseExportReference) { r.CompleteGeneration = true },
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(summary.Reference)
			financeCheck(t, err)
			var ref authorization.LeaseExportReference
			financeCheck(t, json.Unmarshal(raw, &ref))
			mutate(&ref)
			if err := leaseProjectionRefCheck(f, f.actor, ref); !errors.Is(err, authorization.ErrForbidden) {
				t.Fatalf("malformed ref accepted: %v", err)
			}
		})
	}
}

func TestLeaseExportGrantGainRetainsReferenceProjection(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	reader := financeReader(t, f.commercialReportFixture, "Lease minimal", leaseProjectionGrants())
	summary, entries, err := leaseProjectionSnapshot(f, reader, authorization.LeaseExportOperational, authorization.LeaseExportFilter{})
	financeCheck(t, err)
	before, err := json.Marshal(entries)
	financeCheck(t, err)
	if len(summary.Reference.Leases[0].Assets) != 0 || entries[0].Lease.CustomerID != 0 {
		t.Fatal("hidden identity escaped")
	}
	var role, revision int64
	financeCheck(t, f.pool.QueryRow(ctx, `SELECT r.id,r.revision FROM authorization_roles r JOIN users u ON u.authorization_role_id=r.id WHERE u.id=$1`, reader.UserID).Scan(&role, &revision))
	_, err = f.svc.SaveRole(ctx, f.actor, role, "Lease minimal", leaseProjectionGrants(authorization.Grant{Permission: "customer.view", Scope: authorization.All}, authorization.Grant{Permission: "asset.view", Scope: authorization.All}), revision)
	financeCheck(t, err)
	financeCheck(t, leaseProjectionRefCheck(f, reader, summary.Reference))
	after, err := json.Marshal(entries)
	financeCheck(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reference recheck enriched immutable projection")
	}
	_, fresh, err := leaseProjectionSnapshot(f, reader, authorization.LeaseExportOperational, authorization.LeaseExportFilter{})
	financeCheck(t, err)
	if fresh[0].Lease.CustomerID != f.customer || len(fresh[0].Lease.Assets) != 1 {
		t.Fatal("new authority not applied to fresh snapshot")
	}
}

func TestLeaseExportAgreementPositionAndRevisionProjection(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	financeCheck(t, f.svc.ReviseLeaseBilling(ctx, f.actor, f.lease, authorization.LeaseBillingRevisionInput{EffectiveOn: f.today.Format(time.DateOnly), Reason: "Correct approved rent", OperationKey: uuid.NewString(), Changes: []authorization.LeaseBillingChange{{AssetID: f.asset, RentCents: 20001, TaxRate: "7"}}}))
	summary, entries, err := leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportBilling, authorization.LeaseExportFilter{})
	financeCheck(t, err)
	if len(entries) != 1 || !summary.RentAvailable || summary.RentCents != 20001 || entries[0].Lease.Lifecycle != "active" || len(entries[0].Lease.Assets) != 1 || len(entries[0].RentTerms) != 1 || entries[0].RentTerms[0].TaxRate != "7" {
		t.Fatalf("incoherent position: %+v %+v", summary, entries)
	}
	changes := entries[0].BillingRevisions
	if len(changes) != 1 || len(changes[0].Changes) != 1 || changes[0].Changes[0].BeforeRentCents != 12345 || changes[0].Changes[0].AfterRentCents != 20001 || changes[0].Changes[0].Ordinal != 1 {
		t.Fatalf("retained revision: %+v", changes)
	}
	financeCheck(t, leaseProjectionRefCheck(f, f.actor, summary.Reference))
}

func TestLeaseExportUnavailableContributorBeyondPageProjection(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	for range 25 {
		_, err := f.svc.RegisterDraftLease(ctx, f.actor, f.customer)
		financeCheck(t, err)
	}
	imported, err := f.svc.RegisterDraftLease(ctx, f.actor, f.customer)
	financeCheck(t, err)
	financeCheck(t, f.svc.SaveDraftLeaseDetails(ctx, f.actor, imported.ID, authorization.DraftLeaseDetailsInput{Title: "Imported termination without effective facts", StartsOn: "2024-01-01", EndsOn: "2024-01-30", PremisesID: &f.premises}))
	financeCheck(t, f.svc.WithinActor(ctx, f.actor, func(tx pgx.Tx, _ authorization.Principal) error {
		if err := f.svc.WriteLeaseDependencies(ctx, tx, f.actor, authorization.LeaseDependencies{LeaseID: imported.ID, CustomerID: f.customer, PremisesID: &f.premises, Lifecycle: "terminated"}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE leases SET lifecycle='terminated' WHERE id=$1`, imported.ID)
		return err
	}))
	report, err := f.svc.LeaseReport(ctx, f.actor, authorization.LeaseReportQuery{PerPage: 25})
	financeCheck(t, err)
	if report.Total != 27 || len(report.Rows) != 25 || report.RentAvailable {
		t.Fatalf("page concealed unknown total: %+v", report)
	}
	for _, row := range report.Rows {
		if row.ID == imported.ID {
			t.Fatal("unknown contributor must be beyond displayed page")
		}
	}
	summary, entries, err := leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportBilling, authorization.LeaseExportFilter{})
	financeCheck(t, err)
	if summary.LeaseCount != 27 || summary.RentAvailable || len(entries) != 27 {
		t.Fatalf("unknown contributor lost: %+v", summary)
	}
	unknown := entries[len(entries)-1]
	if unknown.Lease.ID != imported.ID || unknown.PositionAvailability != "unavailable" || unknown.Lease.Lifecycle != "unavailable" || len(unknown.Lease.Assets) != 0 || len(unknown.RentTerms) != 0 {
		t.Fatalf("fabricated imported position: %+v", unknown)
	}
	for _, filter := range []authorization.LeaseExportFilter{{Lifecycle: "active"}, {EndingThrough: "2030-01-01"}, {Asset: f.asset}, {Billing: "pending"}} {
		_, _, err := leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportBilling, filter)
		if !errors.Is(err, authorization.ErrConflict) {
			t.Fatalf("unknown membership silently omitted for %+v: %v", filter, err)
		}
	}
}

func TestLeaseExportAllMatchingAndEmitFailureProjection(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	for range 104 {
		_, err := f.svc.RegisterDraftLease(ctx, f.actor, f.customer)
		financeCheck(t, err)
	}
	summary, entries, err := leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportOperational, authorization.LeaseExportFilter{Sort: "number_desc"})
	financeCheck(t, err)
	if summary.LeaseCount != 105 || len(entries) != 105 || len(summary.Reference.Leases) != 105 || entries[104].Lease.ID != f.lease {
		t.Fatal("all matching export truncated or reordered")
	}
	stop := errors.New("consumer failed after first complete Lease")
	count := 0
	err = f.svc.WithinSnapshotPermission(ctx, f.actor, "lease.export", authorization.All, func(tx pgx.Tx, p authorization.Principal) error {
		_, err := authorization.LeaseExportSnapshot(ctx, tx, p, authorization.LeaseExportOperational, authorization.LeaseExportFilter{}, func(_ authorization.LeaseExportMeta, _ authorization.LeaseExportEntry) error { count++; return stop })
		return err
	})
	if !errors.Is(err, stop) || count != 1 {
		t.Fatalf("emit failure swallowed: %d %v", count, err)
	}
}

func TestLeaseExportHistoricalGenerationWindowProjection(t *testing.T) {
	f := newLeaseExportProjectionFixtureSchedule(t, 61, 31)
	ctx := context.Background()
	// The retained initial obligation is beyond the window yesterday, but inside
	// the window on its due day. Neither date can establish past retry state.
	later := f.today.AddDate(0, 0, 40).Format(time.DateOnly)
	_, err := f.pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '%s'::date $$`, later))
	financeCheck(t, err)
	for _, tc := range []struct{ at, state string }{{f.today.Format(time.DateOnly), "not_yet_eligible"}, {f.today.AddDate(0, 0, 31).Format(time.DateOnly), "ungenerated"}} {
		_, entries, err := leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportBilling, authorization.LeaseExportFilter{AsOf: tc.at})
		financeCheck(t, err)
		found := false
		for _, period := range entries[0].Periods {
			if period.Kind == "initial" {
				found = true
				if period.State != tc.state {
					t.Fatalf("initial at %s: %+v", tc.at, period)
				}
			}
		}
		if !found || entries[0].FailureHistory != "unavailable" {
			t.Fatalf("missing retained initial/history: %+v", entries[0])
		}
	}
}

func TestLeaseExportHistoricalScheduledCancellationEntitlementProjection(t *testing.T) {
	f := newLeaseExportProjectionFixtureSchedule(t, 61, 31)
	ctx := context.Background()
	selected := f.today.AddDate(0, 0, 31).Format(time.DateOnly)
	cancelled := f.today.AddDate(0, 0, 40).Format(time.DateOnly)
	_, err := f.pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '%s'::date $$`, cancelled))
	financeCheck(t, err)
	financeCheck(t, f.svc.ChangeLeaseNotice(ctx, f.actor, f.lease, authorization.LeaseNoticeInput{Action: "create", Category: "cancel_before_start", Initiator: "customer", NoticeOn: cancelled, Reason: "Cancel scheduled agreement", OperationKey: uuid.NewString()}))
	var currentCoverage int
	financeCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM lease_asset_dependencies WHERE lease_id=$1`, f.lease).Scan(&currentCoverage))
	if currentCoverage != 0 {
		t.Fatal("fixture did not clear current coverage")
	}
	_, entries, err := leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportBilling, authorization.LeaseExportFilter{AsOf: selected})
	financeCheck(t, err)
	row := entries[0]
	if row.Lease.Lifecycle != "scheduled" || row.Lease.ScheduledRentCents != 12345 || len(row.Lease.Assets) != 0 {
		t.Fatalf("historical agreement: %+v", row)
	}
	found := false
	for _, p := range row.Periods {
		if p.Kind == "initial" {
			found = true
			if p.State != "ungenerated" {
				t.Fatalf("later cancellation erased earlier initial entitlement: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("missing retained initial period")
	}
	_, current, err := leaseProjectionSnapshot(f, f.actor, authorization.LeaseExportBilling, authorization.LeaseExportFilter{})
	financeCheck(t, err)
	if current[0].Lease.Lifecycle != "terminated" || current[0].Lease.ScheduledRentCents != 0 {
		t.Fatalf("cancelled current position: %+v", current[0])
	}
	for _, p := range current[0].Periods {
		if p.Kind == "initial" && p.State != "not_owed" {
			t.Fatalf("cancelled initial: %+v", p)
		}
	}
}

func emptyLeaseProjectionSelection() authorization.LeaseExportSelectionReference {
	return authorization.LeaseExportSelectionReference{Customers: []authorization.LeaseExportRelatedReference{}, Premises: []authorization.LeaseExportRelatedReference{}, Assets: []authorization.LeaseExportRelatedReference{}}
}

func TestLeaseExportEmptyArchivedSelectionRetainsOriginalAuthorityProjection(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	ctx := context.Background()
	for _, kind := range []string{"customer", "location", "asset"} {
		t.Run(kind, func(t *testing.T) {
			var id int64
			table := ""
			filter := authorization.LeaseExportFilter{Search: "no matching Lease"}
			switch kind {
			case "customer":
				table = "customers"
				financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Archived selected customer') RETURNING id`, f.company).Scan(&id))
				filter.Customer = id
			case "location":
				table = "locations"
				financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO locations(company_id,object_type,object_id,title) VALUES($1,'customer',$2,'Archived selected premises') RETURNING id`, f.company, f.customer).Scan(&id))
				filter.Premises = id
			case "asset":
				table = "assets"
				financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO assets(company_id,asset_type_id,ownership_class,name) SELECT company_id,asset_type_id,'company_owned','Archived selected asset' FROM assets WHERE id=$1 RETURNING id`, f.asset).Scan(&id))
				filter.Asset = id
			}
			_, err := f.pool.Exec(ctx, `UPDATE `+table+` SET deleted_at=clock_timestamp() WHERE id=$1`, id)
			financeCheck(t, err)
			reader := financeReader(t, f.commercialReportFixture, "Archived selection "+kind, leaseProjectionGrants(authorization.Grant{Permission: "customer.view", Scope: authorization.All}, authorization.Grant{Permission: "location.view", Scope: authorization.All}, authorization.Grant{Permission: "asset.view", Scope: authorization.All}, authorization.Grant{Permission: kind + ".archived.view", Scope: authorization.All}))
			summary, entries, err := leaseProjectionSnapshot(f, reader, authorization.LeaseExportOperational, filter)
			financeCheck(t, err)
			if len(entries) != 0 || summary.LeaseCount != 0 {
				t.Fatal("expected empty selection")
			}
			var selected []authorization.LeaseExportRelatedReference
			switch kind {
			case "customer":
				selected = summary.Reference.Selection.Customers
			case "location":
				selected = summary.Reference.Selection.Premises
			case "asset":
				selected = summary.Reference.Selection.Assets
			}
			if len(selected) != 1 || selected[0].ID != id || !selected[0].Archived {
				t.Fatalf("lost selected archive anchor: %+v", summary.Reference.Selection)
			}
			financeCheck(t, leaseProjectionRefCheck(f, reader, summary.Reference))
			_, err = f.pool.Exec(ctx, `UPDATE `+table+` SET deleted_at=NULL WHERE id=$1`, id)
			financeCheck(t, err)
			financeCheck(t, leaseProjectionRefCheck(f, reader, summary.Reference))
			_, err = f.pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=(SELECT authorization_role_id FROM users WHERE id=$1) AND permission=$2`, reader.UserID, kind+".archived.view")
			financeCheck(t, err)
			financeDenied(t, leaseProjectionRefCheck(f, reader, summary.Reference))
			fresh, _, err := leaseProjectionSnapshot(f, reader, authorization.LeaseExportOperational, filter)
			financeCheck(t, err)
			financeCheck(t, leaseProjectionRefCheck(f, reader, fresh.Reference))
			// A required anchor cannot be omitted, substituted, or appended to a
			// different filter, even when all referenced resources are visible.
			bad := fresh.Reference
			bad.Selection = emptyLeaseProjectionSelection()
			if err := leaseProjectionRefCheck(f, reader, bad); !errors.Is(err, authorization.ErrForbidden) {
				t.Fatalf("missing selected anchor accepted: %v", err)
			}
			bad = fresh.Reference
			bad.Filter.Customer, bad.Filter.Asset, bad.Filter.Premises = 0, 0, 0
			if err := leaseProjectionRefCheck(f, reader, bad); !errors.Is(err, authorization.ErrForbidden) {
				t.Fatalf("extraneous selected anchor accepted: %v", err)
			}
		})
	}
}
