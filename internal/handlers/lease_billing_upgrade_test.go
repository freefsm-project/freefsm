package handlers

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
)

func TestLeaseBillingPopulatedNativeUpgrade(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	lease, values := populateFinalizedLease(t, pool, client, web, cookie, company)
	voidLeaseInvoiceAndReplay(t, pool, web, cookie, company, admin, lease, values)
	// This predecessor predates manual adjustments. Remove only verified-empty
	// new structures; retained commercial facts must never be reconstructed.
	var adjustments int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_manual_adjustments`).Scan(&adjustments))
	historyEqual(t, "predecessor has no manual adjustments", adjustments, 0)
	_, err = pool.Exec(ctx, `DROP TRIGGER lease_adjustment_customer_guard ON invoices; DROP FUNCTION lease_adjustment_customer_guard(); DROP TABLE lease_manual_adjustments`)
	historyCheck(t, err)
	removeEmptyCoverageSchema(t, pool)
	// Capture real native finalization/ordinary Void effects. Remove only empty
	// post-112 technical structures to form the previous installed schema; no
	// agreement, coverage, Invoice, approval, audit, permission or ID is invented.
	var empty bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM lease_billing_worker_state) AND NOT EXISTS(SELECT 1 FROM lease_billing_attempts) AND NOT EXISTS(SELECT 1 FROM lease_billing_source_changes) AND NOT EXISTS(SELECT 1 FROM lease_billing_retry_receipts) AND NOT EXISTS(SELECT 1 FROM lease_billing_period_dispositions) AND NOT EXISTS(SELECT 1 FROM lease_billing_obligation_revisions)`).Scan(&empty))
	historyEqual(t, "fixture contains no later billing effects", empty, true)
	_, err = pool.Exec(ctx, `DROP FUNCTION lease_generation_revision_guard() CASCADE;
 DROP FUNCTION lease_pending_revision_guard() CASCADE;
 DROP FUNCTION lease_billing_exemption_review() CASCADE;
 DROP FUNCTION lease_billing_coverage_review() CASCADE;
 DROP FUNCTION lease_billing_agreement_review() CASCADE;
 DROP FUNCTION record_lease_billing_source_change(bigint,date,date,text) CASCADE;
 DROP FUNCTION lease_generation_chain_guard() CASCADE;
 DROP TRIGGER lease_generation_fence ON lease_invoice_generations;
 DROP VIEW lease_billing_obligation_current;
 DROP TABLE lease_billing_worker_state,lease_billing_attempts,lease_billing_retry_receipts,lease_invoice_review_flags,lease_billing_source_changes,lease_billing_period_dispositions,lease_billing_period_failures,lease_billing_obligation_revisions CASCADE;
 ALTER TABLE lease_invoice_generations DROP COLUMN obligation_revision_id,DROP COLUMN predecessor_invoice_id,DROP COLUMN operation_key,DROP COLUMN review_hash,DROP COLUMN actor_id;
 ALTER TABLE lease_invoice_generations DROP CONSTRAINT lease_invoice_generations_pkey;
 ALTER TABLE lease_invoice_generations ADD PRIMARY KEY(obligation_id);
 DELETE FROM schema_migrations WHERE name>='113_';`)
	historyCheck(t, err)
	attribution, err := fs.ReadFile(database.MigrationFS(), "074_device_pairing.up.sql")
	historyCheck(t, err)
	_, err = pool.Exec(ctx, string(attribution[strings.Index(string(attribution), "ALTER TABLE authorization_audit DROP"):]))
	historyCheck(t, err)
	tables := []string{"leases", "lease_finalizations", "lease_billing_obligations", "lease_asset_dependencies", "lease_master_dependencies", "invoices", "issued_document_numbers", "company_settings", "authorization_audit", "authorization_roles", "authorization_grants"}
	before := historyDurableRows(t, pool, tables)
	var generations, sequences string
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g) ORDER BY invoice_id)::text FROM lease_invoice_generations g`).Scan(&generations))
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(sequencename,last_value) ORDER BY sequencename)::text FROM pg_sequences WHERE schemaname=current_schema()`).Scan(&sequences))
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "populated native upgrade preserves source and grants", historyDurableRows(t, pool, tables), before)
	var afterGenerations, afterSequences string
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g)-ARRAY['predecessor_invoice_id','operation_key','review_hash','actor_id','obligation_revision_id'] ORDER BY invoice_id)::text FROM lease_invoice_generations g`).Scan(&afterGenerations))
	historyEqual(t, "old generation IDs and recorded history survive", afterGenerations, generations)
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(sequencename,last_value) ORDER BY sequencename)::text FROM pg_sequences WHERE schemaname=current_schema() AND sequencename NOT IN('lease_billing_source_changes_id_seq','lease_billing_obligation_revisions_id_seq','lease_coverage_events_id_seq','lease_coverage_revisions_id_seq','lease_notice_revisions_id_seq','lease_fact_corrections_id_seq','lease_billing_revisions_id_seq','lease_manual_adjustments_id_seq')`).Scan(&afterSequences))
	historyEqual(t, "all prior sequence values preserved", afterSequences, sequences)
	var unusedAdjustmentSequence bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT last_value=1 AND NOT is_called FROM lease_manual_adjustments_id_seq`).Scan(&unusedAdjustmentSequence))
	historyEqual(t, "new manual adjustment sequence remains unused", unusedAdjustmentSequence, true)
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_manual_adjustments`).Scan(&adjustments))
	historyEqual(t, "upgrade invents no manual adjustments", adjustments, 0)
	var newFacts bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_invoice_generations WHERE predecessor_invoice_id IS NOT NULL OR obligation_revision_id IS NOT NULL) OR EXISTS(SELECT 1 FROM lease_billing_source_changes) OR EXISTS(SELECT 1 FROM lease_billing_obligation_revisions)`).Scan(&newFacts))
	historyEqual(t, "upgrade infers no later billing facts", newFacts, false)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "migration replay preserves original business facts", historyDurableRows(t, pool, tables), before)
	var pending int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE lease_id=$1 AND kind='monthly'`, lease).Scan(&pending))
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-02'::date $$`)
	historyCheck(t, err)
	historyCheck(t, authorization.New(pool).GenerateLeaseDrafts(ctx))
	var generated bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_invoice_generations WHERE obligation_id=$1) AND (SELECT count(*) FROM lease_invoice_generations)=2`, pending).Scan(&generated))
	historyEqual(t, "upgrade resumes original pending ID and keeps Void consumed", generated, true)
}
