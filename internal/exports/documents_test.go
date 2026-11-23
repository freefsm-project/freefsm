package exports

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
)

func TestDocumentOperationalDurableFixedColumns(t *testing.T) {
	pool, actor, company := operationalExportDB(t)
	ctx := context.Background()
	var customer int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `INSERT INTO `+kind+`s(company_id,customer_id,title,number_label) VALUES($1,$2,$3,$4)`, company, customer, "=formula,\nquoted \"title\"", "DOC-98"); err != nil {
				t.Fatal(err)
			}
			svc := Product(pool)
			id, err := svc.Accept(ctx, actor, uuid.NewString(), kind+"-list-A-v1", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			d, err := svc.PrepareDownload(ctx, actor, id)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := csv.NewReader(d).ReadAll()
			_ = d.Close()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 || !reflect.DeepEqual(rows[0], (Documents{}).Columns()) {
				t.Fatalf("columns/rows: %v", rows)
			}
			if rows[1][3] == "" || rows[1][4] != "'=formula,\nquoted \"title\"" {
				t.Fatalf("escaping: %v", rows[1])
			}
			for _, name := range rows[0] {
				if strings.Contains(name, "amount") || strings.Contains(name, "financial") || strings.Contains(name, "price") {
					t.Fatalf("financial column: %s", name)
				}
			}
			if kind == "invoice" {
				var invoice, status int64
				if err := pool.QueryRow(ctx, `SELECT i.id,s.id FROM invoices i CROSS JOIN statuses s WHERE s.company_id=$1 AND s.category_key='invoice:void' LIMIT 1`, company).Scan(&invoice, &status); err != nil {
					t.Fatal(err)
				}
				if err := statusflow.New(pool).TransitionInvoice(ctx, statusflow.Actor{ID: actor.UserID, CompanyID: company, Role: "admin"}, invoice, status); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.Exec(ctx, `UPDATE `+kind+`s SET deleted_at=clock_timestamp()`); err != nil {
				t.Fatal(err)
			}
			if d, err := svc.PrepareDownload(ctx, actor, id); !errors.Is(err, ErrUnavailable) || d != nil {
				if d != nil {
					_ = d.Close()
				}
				t.Fatalf("archived document recheck: %v", err)
			}
		})
	}
}

func TestOperationalExportFilterContracts(t *testing.T) {
	for _, d := range []Definition{Jobs{ExportScope: "A"}, Workload{ExportScope: "A"}, Documents{DocumentType: "estimate", ExportScope: "A"}, Documents{DocumentType: "invoice", ExportScope: "A"}, EstimateCohorts{ExportScope: "A"}} {
		for _, raw := range []string{`null`, `[]`, `{} {}`, `{"unknown":true}`, `{"sort":"not-a-sort"}`} {
			if err := d.Validate(json.RawMessage(raw)); !errors.Is(err, ErrConflict) {
				t.Errorf("%s accepted %s: %v", d.Kind(), raw, err)
			}
		}
	}
}
