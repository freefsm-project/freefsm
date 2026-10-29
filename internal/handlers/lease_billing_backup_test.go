package handlers

import (
	"context"
	"os"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/jackc/pgx/v5"
)

func TestLeaseBillingEncryptedRestoreResumesPending(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "Lease billing source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Lease billing destination")
	web := middleware.Company(services.NewCompanySettingsService(src.client))(New(src.pool, src.client, src.sessions, &config.Config{UploadDir: src.uploadDir, MaxUploadSize: 25 << 20}))
	tx, err := src.pool.Begin(ctx)
	historyCheck(t, err)
	defer tx.Rollback(ctx)
	var workflow int64
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
	var initial, pending, invoice, void, customer, asset, premises int64
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT g.obligation_id,g.invoice_id FROM lease_invoice_generations g`).Scan(&initial, &invoice))
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE kind='monthly'`).Scan(&pending))
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void' AND is_category_default`, src.company).Scan(&void))
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT l.customer_id,l.premises_id,c.asset_id FROM leases l JOIN lease_asset_dependencies c ON c.lease_id=l.id WHERE l.id=$1`, lease).Scan(&customer, &premises, &asset))
	flow := statusflow.New(src.pool)
	actor := statusflow.Actor{ID: src.actor, CompanyID: src.company, Role: "admin"}
	historyCheck(t, flow.FinalizeInvoice(ctx, actor, invoice))
	historyCheck(t, flow.TransitionInvoice(ctx, actor, invoice, void))
	svc := authorization.New(src.pool)
	a := authorization.Actor{UserID: src.actor}
	review, err := svc.ReviewLeaseBillingPeriod(ctx, a, lease, initial)
	historyCheck(t, err)
	_, err = svc.ReplaceLeaseInvoice(ctx, a, lease, initial, authorization.LeaseReplacementInput{InvoiceID: invoice, ReviewHash: review.ReviewHash, OperationKey: review.OperationKey})
	historyCheck(t, err)
	exempt := true
	_, err = svc.SaveCustomer(ctx, a, customer, authorization.CustomerInput{TaxExempt: &exempt})
	historyCheck(t, err)
	end := "2024-05-31"
	historyCheck(t, svc.WithinActor(ctx, a, func(tx pgx.Tx, _ authorization.Principal) error {
		return svc.WriteLeaseDependencies(ctx, tx, a, authorization.LeaseDependencies{LeaseID: lease, CustomerID: customer, PremisesID: &premises, Lifecycle: "scheduled", Coverage: []authorization.LeaseAssetDependency{{AssetID: asset, StartsOn: "2024-01-31", EndsOn: &end}}})
	}))
	_, err = src.pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-06-01'::date $$; CREATE FUNCTION backup_billing_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.invoice.generate' AND EXISTS(SELECT 1 FROM lease_billing_obligations o WHERE o.id=(NEW.detail->>'obligation_id')::bigint AND due_on='2024-02-01') THEN RAISE EXCEPTION 'backup pending period fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER backup_billing_fault BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION backup_billing_fault()`)
	historyCheck(t, err)
	if err = svc.GenerateLeaseDrafts(ctx); err == nil {
		t.Fatal("missing pending failure")
	}
	_, err = src.pool.Exec(ctx, `DROP TRIGGER backup_billing_fault ON authorization_audit; DROP FUNCTION backup_billing_fault()`)
	historyCheck(t, err)
	// Test clocks are not trusted installation schema. Restore the exact shipped
	// function before creating the encrypted release-compatible archive.
	_, err = src.pool.Exec(ctx, originalClock)
	historyCheck(t, err)
	tables := []string{"leases", "lease_finalizations", "lease_billing_obligations", "lease_billing_obligation_revisions", "lease_invoice_generations", "lease_billing_attempts", "lease_billing_worker_state", "lease_billing_source_changes", "lease_invoice_review_flags", "lease_billing_period_failures", "lease_billing_period_dispositions", "lease_billing_retry_receipts", "lease_asset_dependencies", "lease_master_dependencies", "invoices", "issued_document_numbers", "company_settings", "customers", "locations", "assets", "authorization_roles", "authorization_grants"}
	before := leasePreservationRows(t, src.pool, tables)
	var populated bool
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM lease_invoice_generations)=5 AND EXISTS(SELECT 1 FROM lease_invoice_generations WHERE predecessor_invoice_id IS NOT NULL) AND EXISTS(SELECT 1 FROM lease_billing_attempts WHERE obligation_id=$1 AND failure<>'') AND EXISTS(SELECT 1 FROM lease_billing_obligation_revisions WHERE obligation_id=$1) AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations WHERE obligation_id=$1) AND (SELECT count(*) FROM lease_billing_period_dispositions)=2`, pending).Scan(&populated))
	historyEqual(t, "nonempty lineage, revised pending failure and non-owed source", populated, true)
	var through int64
	var audit, sequences string
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT max(id),jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a`).Scan(&through, &audit))
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(sequencename,last_value) ORDER BY sequencename)::text FROM pg_sequences WHERE schemaname='public' AND (sequencename LIKE 'lease_%' OR sequencename='leases_id_seq' OR sequencename='invoices_id_seq')`).Scan(&sequences))
	src.roundTripTo(t, dst, func() {
		// This completion is deliberately AFTER the backup boundary. Restore must
		// neither fabricate its receipt nor claim knowledge of post-backup outcomes.
		historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	})
	historyCheck(t, dst.manager.RefreshConnections())
	historyEqual(t, "encrypted restore preserves exact billing snapshot", leasePreservationRows(t, dst.pool, tables), before)
	var restoredAudit, restoredSequences string
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a WHERE id<=$1`, through).Scan(&restoredAudit))
	historyEqual(t, "prior immutable audit preserved", restoredAudit, audit)
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(sequencename,last_value) ORDER BY sequencename)::text FROM pg_sequences WHERE schemaname='public' AND (sequencename LIKE 'lease_%' OR sequencename='leases_id_seq' OR sequencename='invoices_id_seq')`).Scan(&restoredSequences))
	historyEqual(t, "billing sequence identities preserved", restoredSequences, sequences)
	historyCheck(t, authorization.New(dst.pool).GenerateLeaseDrafts(ctx))
	historyCheck(t, authorization.New(dst.pool).GenerateLeaseDrafts(ctx))
	var count int
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations WHERE obligation_id=$1`, pending).Scan(&count))
	historyEqual(t, "restored pending intent generates once", count, 1)
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations WHERE obligation_id=$1`, initial).Scan(&count))
	historyEqual(t, "restored Void/replacement lineage is not regenerated", count, 2)
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_obligation_revisions WHERE obligation_id=$1`, pending).Scan(&count))
	historyEqual(t, "restore retry retains reconciled revision", count, 1)
	t.Log("Archive is a point-in-time boundary: source completed the pending Draft afterward; restored history correctly retains only the archive's pending intent. No post-backup completion or external outcome is inferred.")
}
