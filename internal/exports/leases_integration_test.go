package exports

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

// This adapter/engine test complements handler acceptance tests with direct
// durable records, complete ordering and malformed private-anchor checks.
func TestLeaseExportDurableAdapterAllMatching(t *testing.T) {
	pool, actor, company := operationalExportDB(t)
	actor.Channel = "web"
	ctx := context.Background()
	var customer int64
	name := "=Customer, \"quoted\"\n日本語"
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,$2) RETURNING id`, company, name).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	var ids []int64
	for i := 0; i < 103; i++ {
		lease, err := auth.RegisterDraftLease(ctx, actor, customer)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, lease.ID)
	}
	for _, mode := range []authorization.LeaseExportMode{authorization.LeaseExportOperational, authorization.LeaseExportBilling} {
		t.Run(string(mode), func(t *testing.T) {
			d := Leases{Mode: mode}
			svc := New(pool, d)
			options := json.RawMessage(fmt.Sprintf(`{"customer":%d,"sort":"number_desc"}`, customer))
			op := uuid.NewString()
			id, err := svc.Accept(ctx, actor, op, d.Kind(), options)
			if err != nil {
				t.Fatal(err)
			}
			if replay, err := svc.Accept(ctx, actor, op, d.Kind(), options); err != nil || replay != id {
				t.Fatalf("replay %d: %v", replay, err)
			}
			if processed, err := svc.ProcessOne(ctx); err != nil || !processed {
				t.Fatalf("generation %v: %v", processed, err)
			}
			svc = New(pool, d)
			status, err := svc.Status(ctx, actor, id)
			// Drafts have no finalized generation source. Billing explicitly emits
			// an unavailable period after each Lease instead of asserting absence.
			rowsPerLease := 1
			if mode == authorization.LeaseExportBilling {
				rowsPerLease = 2
			}
			wantRecords := int64(len(ids)*rowsPerLease + 1)
			if err != nil || status.State != "Ready" || status.Blocked || status.RowCount == nil || *status.RowCount != wantRecords {
				t.Fatalf("status %+v: %v", status, err)
			}
			download, err := svc.PrepareDownload(ctx, actor, id)
			if err != nil {
				t.Fatal(err)
			}
			original, err := io.ReadAll(download)
			_ = download.Close()
			if err != nil {
				t.Fatal(err)
			}
			rows, err := csv.NewReader(strings.NewReader(string(original))).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != int(wantRecords)+1 || !reflect.DeepEqual(rows[0], d.Columns()) {
				t.Fatalf("incomplete contract: %d rows", len(rows))
			}
			for i := range ids {
				row := rows[1+i*rowsPerLease]
				if row[0] != "lease" || row[4] != strconv.FormatInt(ids[len(ids)-1-i], 10) || row[10] != "'"+name {
					t.Fatalf("order/escaping row %d: %v", i, row)
				}
				if rowsPerLease == 2 {
					period := rows[2+i*rowsPerLease]
					if period[0] != "period" || period[4] != row[4] || period[26] != "" || period[27] != "" || period[28] != "unavailable" || period[29] != "" || period[58] != "unavailable" {
						t.Fatalf("Draft generation availability row %d: %v", i, period)
					}
				}
			}
			last := rows[len(rows)-1]
			for i, column := range rows[0] {
				if column == "matching_lease_count" && last[i] != "103" || column == "matching_monthly_rent" && last[i] != "0.00" {
					t.Fatalf("summary %s: %s", column, last[i])
				}
			}
			if last[0] != "summary" {
				t.Fatal("summary not last")
			}
			// Domain label changes cannot enrich or recompute previously prepared bytes.
			if _, err := pool.Exec(ctx, `UPDATE customers SET display_name='Changed after snapshot' WHERE id=$1`, customer); err != nil {
				t.Fatal(err)
			}
			download, err = svc.PrepareDownload(ctx, actor, id)
			if err != nil {
				t.Fatal(err)
			}
			current, err := io.ReadAll(download)
			_ = download.Close()
			if err != nil || string(current) != string(original) {
				t.Fatalf("snapshot bytes changed: %v", err)
			}
			if _, err := pool.Exec(ctx, `UPDATE customers SET display_name=$2 WHERE id=$1`, customer, name); err != nil {
				t.Fatal(err)
			}
			// Missing mandatory false must not become the zero-value false. The CSV
			// and digest remain untouched, so only reference validation can block it.
			if _, err := pool.Exec(ctx, `UPDATE export_records SET reference=reference #- '{leases,0,archived}' WHERE export_id=$1 AND ordinal=1`, id); err != nil {
				t.Fatal(err)
			}
			if leaked, err := svc.PrepareDownload(ctx, actor, id); err == nil {
				_ = leaked.Close()
				t.Fatal("malformed required private anchor produced a download")
			}
		})
	}
}
