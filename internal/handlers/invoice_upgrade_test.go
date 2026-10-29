package handlers

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Preserve all installed columns. The single schema-only key introduced by 110
// is compared separately as NULL, never stripped if it contains a relationship.
// Do not use this projection for current-schema or Lease-generated Invoices.
func preLeaseUpgradeRows(t *testing.T, pool *pgxpool.Pool, tables []string) map[string]string {
	t.Helper()
	rows := historyDurableRows(t, pool, tables)
	if _, present := rows["invoices"]; present {
		var invented bool
		historyCheck(t, pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM invoices i WHERE to_jsonb(i)->'lease_id' IS NOT NULL AND to_jsonb(i)->'lease_id'<>'null'::jsonb)`).Scan(&invented))
		if invented {
			t.Fatal("legacy Invoice acquired invented Lease provenance")
		}
		var invoiceRows string
		historyCheck(t, pool.QueryRow(context.Background(), `SELECT jsonb_agg(to_jsonb(i)-'lease_id' ORDER BY (to_jsonb(i)-'lease_id')::text)::text FROM invoices i`).Scan(&invoiceRows))
		rows["invoices"] = invoiceRows
	}
	return rows
}
