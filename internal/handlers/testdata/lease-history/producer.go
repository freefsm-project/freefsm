// Compiled only inside the exact pre-113 source archive by the upgrade test.
// All population helpers and production writers come from that archive.
package handlers

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLeaseUpgradeProducer(t *testing.T) {
	out := os.Getenv("FREEFSM_LEASE_HISTORY_OUTPUT")
	if out == "" {
		t.Fatal("owned capture directory required")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, values := populateFinalizedLease(t, pool, client, web, cookie, company)
	voidLeaseInvoiceAndReplay(t, pool, web, cookie, company, admin, lease, values)
	var capture struct {
		Schema string
		Lease  int64
	}
	capture.Lease = lease
	historyCheck(t, pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&capture.Schema))
	cmd := exec.Command("pg_dump", "--dbname", os.Getenv("FREEFSM_TEST_DATABASE_URL"), "--schema", capture.Schema, "--format=custom", "--no-owner", "--no-acl", "--file", filepath.Join(out, "history.dump"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dump: %v: %s", err, output)
	}
	data, err := json.Marshal(capture)
	historyCheck(t, err)
	historyCheck(t, os.WriteFile(filepath.Join(out, "capture.json"), data, 0600))
}
