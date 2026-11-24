package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/settlement"
)

func TestFinancialAcceptanceActualGrantDMLAtAcceptanceGenerationDownload(t *testing.T) {
	for _, output := range financialAcceptanceOutputs {
		for _, phase := range []string{"accept", "generate", "download"} {
			t.Run(output.view+"/"+phase, func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				auth := authorization.New(pool)
				actor := authorization.Actor{UserID: admin, Channel: "web"}
				reader := client.User.Create().SetCompanyID(company).SetName("Financial race reader").SetEmail("financial-race@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
				view := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: output.facet, Scope: authorization.All}}
				if output.view == "combined" {
					view = append(view, authorization.Grant{Permission: "credit.view", Scope: authorization.All}, authorization.Grant{Permission: "credit.export", Scope: authorization.All})
				}
				grants := append(append([]authorization.Grant(nil), view...), authorization.Grant{Permission: output.grant, Scope: authorization.All})
				role, err := auth.SaveRole(ctx, actor, 0, "Financial race reader", grants)
				historyCheck(t, err)
				historyCheck(t, auth.AssignUser(ctx, actor, reader.ID, role))
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Private raced financial source").SaveX(ctx)
				var issued int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='invoice:invoiced' LIMIT 1`).Scan(&issued))
				client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetInvoiceNumber(99400).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetStatusID(issued).SetTitle("Private raced Invoice").SetLineItems(`[{"title":"Work","quantity":1,"unit_price":50}]`).SaveX(ctx)
				_, err = settlement.New(pool).RecordCustomerPayment(ctx, actor, c.ID, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 10000, Method: settlement.Cash, ReceivedDate: "2020-01-01"})
				historyCheck(t, err)
				cookie := sessionCookie(t, ctx, sessions, reader.ID)
				values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "operation_id": {newOperationKey()}}
				path := ""
				if phase != "accept" {
					path = financialAcceptancePost(t, web, cookie, output.export, values, 303)
				}
				if phase == "download" {
					_, err = exports.Product(pool).ProcessOne(ctx)
					historyCheck(t, err)
				}
				// Obtain a real CSRF token before opening the witnessed DML barrier.
				browser, request := financialAcceptanceFormRequest(t, web, cookie, output.export, values)
				if phase == "download" {
					request = httptest.NewRequest("GET", path+"/download", nil)
					request.AddCookie(cookie)
				}
				holder, err := pool.Begin(ctx)
				historyCheck(t, err)
				defer holder.Rollback(context.Background())
				var blocker int32
				historyCheck(t, holder.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(100301)`).Scan(&blocker))
				_, err = pool.Exec(ctx, `CREATE FUNCTION hold_financial_revoke() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(100301); RETURN OLD; END $$; CREATE TRIGGER hold_financial_revoke BEFORE DELETE ON authorization_grants FOR EACH ROW WHEN(OLD.permission LIKE '%.export') EXECUTE FUNCTION hold_financial_revoke()`)
				historyCheck(t, err)
				var revision int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
				revoked := make(chan error, 1)
				go func() {
					_, err := auth.SaveRole(ctx, actor, role, "Financial race reader", view, revision)
					revoked <- err
				}()
				writer := timeExportDMLWaiter(t, ctx, pool, blocker, 100301, "DELETE FROM authorization_grants")
				assertForeignAuthorityExcluded(t, ctx, pool, writer)
				done := make(chan error, 1)
				if phase == "generate" {
					go func() { _, err := exports.Product(pool).ProcessOne(ctx); done <- err }()
					waitAuthorityRequest(t, ctx, pool, writer, authoritySnapshotStatement)
				} else {
					go func() {
						w := httptest.NewRecorder()
						browser.ServeHTTP(w, request)
						want := 410
						if phase == "accept" {
							want = 403
						}
						if w.Code != want || strings.Contains(w.Body.String(), "Private raced") {
							done <- fmt.Errorf("response=%d body=%s", w.Code, w.Body.String())
							return
						}
						done <- nil
					}()
					waitAuthorityRequest(t, ctx, pool, writer, authorityWriterStatement)
				}
				historyCheck(t, holder.Commit(ctx))
				historyCheck(t, <-revoked)
				result := <-done
				if phase == "generate" {
					if result == nil {
						t.Fatal("generation ignored committed revocation")
					}
					assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Failed")
				} else {
					historyCheck(t, result)
				}
				var accepted, records, audits int
				historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM export_jobs WHERE operation_id=$1),(SELECT count(*) FROM export_records),(SELECT count(*) FROM authorization_audit WHERE target_type='export' AND detail->>'event'='generated')`, values.Get("operation_id")).Scan(&accepted, &records, &audits))
				if phase == "accept" && accepted != 0 {
					t.Fatal("revoked acceptance committed intent")
				}
				if phase == "generate" && (records != 0 || audits != 0) {
					t.Fatalf("revoked publication leaked records=%d audits=%d", records, audits)
				}
			})
		}
	}
}
