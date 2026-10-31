package handlers

import (
	"context"
	"os"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
)

func TestLeaseCoverageEncryptedRestore(t *testing.T) {
	leaseCoverageEncryptedRestore(t, false)
}

func TestLeaseNoticeEncryptedRestore(t *testing.T) {
	leaseCoverageEncryptedRestore(t, true)
}

func TestLeaseFactCorrectionEncryptedRestore(t *testing.T) {
	leaseCoverageEncryptedRestore(t, true, true)
}

func TestLeaseBillingRevisionEncryptedRestore(t *testing.T) {
	leaseCoverageEncryptedRestore(t, true, true, true)
}

func leaseCoverageEncryptedRestore(t *testing.T, withNotice bool, withFacts ...bool) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "Coverage source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Coverage destination")
	web := middleware.Company(services.NewCompanySettingsService(src.client))(New(src.pool, src.client, src.sessions, &config.Config{UploadDir: src.uploadDir, MaxUploadSize: 25 << 20}))
	var workflow int64
	tx, err := src.pool.Begin(ctx)
	historyCheck(t, err)
	defer tx.Rollback(ctx)
	historyCheck(t, tx.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Invoices','invoice') RETURNING id`, src.company).Scan(&workflow))
	for _, category := range []string{"draft", "invoiced", "sent", "partially_paid", "paid", "void"} {
		_, err = tx.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES($1,$2,$3,$4,1,true)`, src.company, workflow, category, "invoice:"+category)
		historyCheck(t, err)
	}
	historyCheck(t, tx.Commit(ctx))
	var originalClock string
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT pg_get_functiondef('lease_dependency_today(bigint)'::regprocedure)`).Scan(&originalClock))
	_, err = src.pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	lease, _ := populateFinalizedLease(t, src.pool, src.client, web, sessionCookie(t, ctx, src.sessions, src.actor), src.company)
	var outgoing, typ int64
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT a.id,a.asset_type_id FROM assets a JOIN lease_asset_dependencies d ON d.asset_id=a.id WHERE d.lease_id=$1`, lease).Scan(&outgoing, &typ))
	incoming := src.client.Asset.Create().SetCompanyID(src.company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Replacement retained").SaveX(ctx)
	svc := authorization.New(src.pool)
	actor := authorization.Actor{UserID: src.actor}
	in := authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "replace", EffectiveOn: "2024-02-10", OutgoingID: outgoing, IncomingID: incoming.ID}, Action: "create", Reason: "Retained replacement", OperationKey: "restored-coverage-event-0001"}
	event, err := svc.ChangeLeaseCoverage(ctx, actor, lease, in)
	historyCheck(t, err)
	notice := authorization.LeaseNoticeInput{Action: "create", Category: "mutual_early", Initiator: "mutual", NoticeOn: "2024-01-01", EffectiveOn: "2024-06-30", Reason: "Retained ending agreement", OperationKey: "restored-notice-event-0001"}
	if withNotice {
		_, err = src.pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-02-01'::date $$`)
		historyCheck(t, err)
		historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease, notice))
	}
	var correction authorization.LeaseFactInput
	if len(withFacts) > 0 && withFacts[0] {
		base, err := svc.DraftLeaseBase(ctx, actor, lease)
		historyCheck(t, err)
		correction = authorization.LeaseFactInput{ExpectedRevision: base.Revision, StartsOn: "2024-02-01", EndsOn: base.EndsOn, PremisesID: *base.PremisesID, Factual: true, Reason: "Retained commencement correction", OperationKey: "restored-fact-correction-0001"}
		historyCheck(t, svc.CorrectLeaseFacts(ctx, actor, lease, correction))
	}
	var billingRevision authorization.LeaseBillingRevisionInput
	if len(withFacts) > 1 && withFacts[1] {
		billingRevision = authorization.LeaseBillingRevisionInput{EffectiveOn: "2024-03-01", Reason: "Retained future rent", OperationKey: "restored-billing-revision-0001", Changes: []authorization.LeaseBillingChange{{AssetID: incoming.ID, RentCents: 6700, TaxRate: "5"}}}
		historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, lease, billingRevision))
	}
	_, err = src.pool.Exec(ctx, originalClock)
	historyCheck(t, err)
	tables := []string{"leases", "lease_finalizations", "lease_coverage_events", "lease_coverage_revisions", "lease_asset_dependencies", "lease_master_dependencies", "lease_billing_source_changes", "lease_invoice_review_flags", "lease_billing_obligations", "lease_invoice_generations", "invoices", "authorization_roles", "authorization_grants", "assets", "customers", "locations", "issued_document_numbers", "company_settings"}
	tables = append(tables, "lease_notice_revisions", "lease_fact_corrections", "lease_billing_revisions")
	before := leasePreservationRows(t, src.pool, tables)
	src.roundTripTo(t, dst, func() {})
	historyCheck(t, dst.manager.RefreshConnections())
	historyEqual(t, "encrypted restore retains coverage membership, immutable intent and financial provenance", leasePreservationRows(t, dst.pool, tables), before)
	replay, err := authorization.New(dst.pool).ChangeLeaseCoverage(ctx, actor, lease, in)
	historyCheck(t, err)
	historyEqual(t, "restored exact submission returns original event", replay, event)
	if withNotice {
		historyCheck(t, authorization.New(dst.pool).ChangeLeaseNotice(ctx, actor, lease, notice))
	}
	if len(withFacts) > 0 && withFacts[0] {
		historyCheck(t, authorization.New(dst.pool).CorrectLeaseFacts(ctx, actor, lease, correction))
	}
	if len(withFacts) > 1 && withFacts[1] {
		historyCheck(t, authorization.New(dst.pool).ReviseLeaseBilling(ctx, actor, lease, billingRevision))
	}
	historyEqual(t, "restored replay creates no effects", leasePreservationRows(t, dst.pool, tables), before)
}
