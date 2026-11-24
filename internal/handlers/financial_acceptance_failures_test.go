package handlers

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/exports"
)

func TestFinancialAcceptanceAtomicPublicationAndRecoveryHTTP(t *testing.T) {
	for _, output := range financialAcceptanceOutputs {
		for _, fault := range []string{"second-record", "generated-audit"} {
			t.Run(output.view+"/"+fault, func(t *testing.T) {
				pool, _, web, sessions, _, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				cookie := sessionCookie(t, ctx, sessions, admin)
				values := url.Values{"operation_id": {newOperationKey()}}
				path := financialAcceptancePost(t, web, cookie, output.export, values, 303)
				table, condition := "export_records", "NEW.ordinal=1" // even Current Credit has its typed total after the header
				if fault == "generated-audit" {
					table, condition = "authorization_audit", "NEW.target_type='export' AND NEW.detail->>'event'='generated'"
				}
				_, err := pool.Exec(ctx, `CREATE FUNCTION fail_financial_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RAISE EXCEPTION 'private financial storage fault' USING ERRCODE='58030'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_financial_publication BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION fail_financial_publication()`)
				historyCheck(t, err)
				if _, err = exports.Product(pool).ProcessOne(ctx); err == nil {
					t.Fatal("injected publication failure succeeded")
				}
				var state string
				var records, audits int
				historyCheck(t, pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM export_records WHERE export_id=$1),(SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='generated') FROM export_jobs WHERE id=$1`, strings.TrimPrefix(path, "/exports/")).Scan(&state, &records, &audits))
				if state != "Failed" || records != 0 || audits != 0 {
					t.Fatalf("partial publication: %s records=%d audits=%d", state, records, audits)
				}
				body := requestBody(t, web, cookie, "GET", path, 200)
				assertContains(t, body, "Failed")
				assertNotContains(t, body, "private financial storage fault")
				requestBody(t, web, cookie, "GET", path+"/download", 409)
				_, err = pool.Exec(ctx, `DROP TRIGGER fail_financial_publication ON `+table)
				historyCheck(t, err)
				values.Set("operation_id", newOperationKey())
				retry := financialAcceptancePost(t, web, cookie, output.export, values, 303)
				if retry == path {
					t.Fatal("failed intent reused")
				}
				_, err = exports.Product(pool).ProcessOne(ctx)
				historyCheck(t, err)
				if len(timeExportCSV(t, requestBody(t, web, cookie, "GET", retry+"/download", 200))) == 0 {
					t.Fatal("recovered zero snapshot omitted totals")
				}
			})
		}
	}
}

func TestFinancialAcceptanceQueryFailureRetainsFiltersWithoutFalseZero(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	client.Customer.Create().SetCompanyID(company).SetDisplayName("Query source").SaveX(ctx)
	values := url.Values{"from": {"2020-01-01"}, "to": {"2020-12-31"}}
	requestBody(t, web, cookie, "GET", "/reports/payments-credits?"+values.Encode(), 200)
	_, err := pool.Exec(ctx, `ALTER TABLE invoice_payments RENAME TO unavailable_financial_source`)
	historyCheck(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), `ALTER TABLE unavailable_financial_source RENAME TO invoice_payments`)
	}()
	body := requestBody(t, web, cookie, "GET", "/reports/payments-credits?"+values.Encode(), 503)
	assertContains(t, body, "Report unavailable")
	assertContains(t, body, "2020-01-01")
	assertContains(t, body, "2020-12-31")
	assertNotContains(t, body, "No matching records")
	assertNotContains(t, body, "$0.00")
	assertNotContains(t, body, "does not exist")
	_, err = pool.Exec(ctx, `ALTER TABLE unavailable_financial_source RENAME TO invoice_payments`)
	historyCheck(t, err)
	requestBody(t, web, cookie, "GET", "/reports/payments-credits?"+values.Encode(), 200)
}

func TestFinancialAcceptanceIterationFailureAfterFirstSource(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, name := range []string{"First financial source", "Second financial source"} {
		client.Customer.Create().SetCompanyID(company).SetDisplayName(name).SaveX(ctx)
	}
	path := financialAcceptancePost(t, web, cookie, "/reports/customer-financials/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	var projection string
	historyCheck(t, pool.QueryRow(ctx, `SELECT string_agg(CASE WHEN attname='display_name' THEN format('fail_financial_row(%I) AS %I',attname,attname) ELSE quote_ident(attname) END,',' ORDER BY attnum) FROM pg_attribute WHERE attrelid='customers'::regclass AND attnum>0 AND NOT attisdropped`).Scan(&projection))
	_, err := pool.Exec(ctx, `CREATE SEQUENCE financial_rows_seen; CREATE FUNCTION fail_financial_row(value text) RETURNS text LANGUAGE plpgsql VOLATILE AS $$ BEGIN IF nextval('financial_rows_seen')=2 THEN RAISE EXCEPTION 'private financial iteration fault' USING ERRCODE='58030'; END IF; RETURN value; END $$; ALTER TABLE customers RENAME TO financial_iteration_source; CREATE VIEW customers AS SELECT `+projection+` FROM financial_iteration_source`)
	historyCheck(t, err)
	defer func() {
		_, _ = pool.Exec(context.Background(), `DROP VIEW IF EXISTS customers; ALTER TABLE financial_iteration_source RENAME TO customers`)
	}()
	if _, err := exports.Product(pool).ProcessOne(ctx); err == nil {
		t.Fatal("partial source iteration published success")
	}
	var seen, records, audits int
	var state string
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT last_value FROM financial_rows_seen),(SELECT count(*) FROM export_records),(SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='generated'),state FROM export_jobs WHERE id=$1`, strings.TrimPrefix(path, "/exports/")).Scan(&seen, &records, &audits, &state))
	if seen != 2 || records != 0 || audits != 0 || state != "Failed" {
		t.Fatalf("iteration witness=%d records=%d audits=%d state=%s", seen, records, audits, state)
	}
	t.Logf("Real selector evaluated first source and failed on second: sequence=%d; no published rows or generated audit", seen)
	body := requestBody(t, web, cookie, "GET", path, 200)
	assertContains(t, body, "Failed")
	assertNotContains(t, body, "private financial iteration fault")
}
