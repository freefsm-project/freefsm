package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The last installed source before migration 113, including its native Lease
// writers and initializer. Never turn today's schema into a predecessor by
// deleting ledger entries while leaving later columns and effects behind.
const leaseBillingPredecessor = "0f91c0a329a57033bcc05feefeaf6aee5185cb97"

func populatedLeasePredecessor(t *testing.T) (*pgxpool.Pool, int64) {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	dir := t.TempDir()
	run := func(cwd string, env []string, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env = cwd, append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		t.Logf("historical %s output:\n%s", name, out)
		historyCheck(t, err)
	}
	archive := filepath.Join(dir, "predecessor.tar")
	run("../..", nil, "git", "archive", "--format=tar", "--output="+archive, leaseBillingPredecessor)
	data, err := os.ReadFile(archive)
	historyCheck(t, err)
	t.Logf("historical production commit=%s archive_sha256=%x", leaseBillingPredecessor, sha256.Sum256(data))
	source := filepath.Join(dir, "source")
	historyCheck(t, os.Mkdir(source, 0700))
	run(dir, nil, "tar", "-xf", archive, "-C", source)
	producer, err := os.ReadFile("testdata/lease-history/producer.go")
	historyCheck(t, err)
	historyCheck(t, os.WriteFile(filepath.Join(source, "internal/handlers/lease_upgrade_producer_test.go"), producer, 0600))
	run(source, []string{"FREEFSM_LEASE_HISTORY_OUTPUT=" + dir}, "go", "test", "-v", "-race", "-count=1", "-timeout=10m", "./internal/handlers", "-run", "^TestLeaseUpgradeProducer$")
	var capture struct {
		Schema string
		Lease  int64
	}
	data, err = os.ReadFile(filepath.Join(dir, "capture.json"))
	historyCheck(t, err)
	historyCheck(t, json.Unmarshal(data, &capture))
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() {
		_, err := root.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{capture.Schema}.Sanitize()+" CASCADE")
		historyCheck(t, err)
		root.Close()
	})
	// The historical test has normally cleaned its schema; restore its fresh,
	// test-owned dump without rewriting any captured business rows or identities.
	run(dir, nil, "pg_restore", "--exit-on-error", "--no-owner", "--no-acl", "--dbname", dsn, filepath.Join(dir, "history.dump"))
	scoped, err := dsnWithSearchPath(dsn, capture.Schema)
	historyCheck(t, err)
	pool, err := pgxpool.New(ctx, scoped)
	historyCheck(t, err)
	t.Cleanup(pool.Close)
	var predecessor bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM schema_migrations WHERE name>='113_') AND EXISTS(SELECT 1 FROM schema_migrations WHERE name='112_retained_lease_placement_timeline') AND NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='online_save_actions' AND column_name='execution_token')`).Scan(&predecessor))
	historyEqual(t, "actual pre-113 ledger and schema", predecessor, true)
	return pool, capture.Lease
}

func TestLeaseBillingPopulatedNativeUpgrade(t *testing.T) {
	pool, lease := populatedLeasePredecessor(t)
	ctx := context.Background()
	var adjustments int
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
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-02'::date $$`)
	historyCheck(t, err)
	historyCheck(t, authorization.New(pool).GenerateLeaseDrafts(ctx))
	var generated bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_invoice_generations WHERE obligation_id=$1) AND (SELECT count(*) FROM lease_invoice_generations)=2`, pending).Scan(&generated))
	historyEqual(t, "upgrade resumes original pending ID and keeps Void consumed", generated, true)
}
