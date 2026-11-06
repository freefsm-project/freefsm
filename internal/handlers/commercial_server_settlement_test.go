package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
)

func TestCommercialServerSettlementLocksAPI(t *testing.T) {
	for _, source := range []string{"allocation", "credit application"} {
		t.Run(source, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Locked customer").SaveX(ctx)
			other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other customer").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Locked job").SaveX(ctx)
			otherJob := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Other job").SaveX(ctx)
			assignFieldDocumentCreator(t, pool, j.ID, admin)
			token, err := sessions.CreateMobile(ctx, admin, "Settlement matrix")
			historyCheck(t, err)
			api := apiv1.NewRouter(pool, client, sessions)
			created := matrixState(t, lifecycleCall(t, api, token, "POST", "/invoices", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": j.ID, "customer_id": c.ID, "title": "Locked invoice", "line_items": []map[string]any{{"title": "Work", "quantity": "1", "unit_price": "100.00"}}}), 200))
			path := fmt.Sprintf("/invoices/%d", created.ID)
			read := func() string { return lifecycleCall(t, api, token, "GET", path, "", 200) }
			matrixTransition(t, api, token, path, j.ID, matrixState(t, read()), "invoice:invoiced", "finalize", 200)
			svc := settlement.New(pool)
			date := settlement.Date(time.Now().Format("2006-01-02"))
			actor := settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}
			if source == "allocation" {
				_, err = svc.RecordPayment(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: created.ID, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: date})
			} else {
				_, err = svc.RecordCustomerPayment(ctx, authorization.Actor{UserID: admin, Channel: "web"}, c.ID, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: date})
				historyCheck(t, err)
				var credit uuid.UUID
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE customer_id=$1`, c.ID).Scan(&credit))
				_, err = svc.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: created.ID, CreditID: credit, RequestedCents: 2500, EffectiveDate: date})
			}
			historyCheck(t, err)
			before := read()
			state := matrixState(t, before)
			if !state.Locked || state.Manual != "invoice:invoiced" || state.Effective != "invoice:partially_paid" {
				t.Fatalf("missing settlement lock: %+v", state)
			}
			var field int64
			historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type) VALUES($1,'invoice','Locked field','text') RETURNING id`, company).Scan(&field))
			for _, change := range []map[string]any{
				{"title": "Blocked"}, {"notes": "Blocked"}, {"terms": "Blocked"},
				{"invoice_date": "2026-01-01"}, {"due_date": "2026-02-01"},
				{"customer_id": other.ID}, {"job_id": otherJob.ID},
				{"line_items": []any{}}, {"tax_rate": "1"},
				{"custom_fields": []map[string]any{{"definition_id": field, "value": "Blocked"}}},
			} {
				change["operation_id"], change["revision"] = newOperationKey(), state.Revision
				want := 422
				if _, ok := change["customer_id"]; ok {
					want = 409
				}
				if _, ok := change["job_id"]; ok {
					want = 409
				}
				if _, ok := change["job_id"]; !ok {
					change["job_id"] = j.ID
				}
				lifecycleCall(t, api, token, "PATCH", path, matrixBody(t, change), want)
				if read() != before {
					t.Fatal("locked edit changed document")
				}
			}
			for _, target := range []string{"invoice:draft", "invoice:invoiced", "invoice:sent", "invoice:void"} {
				matrixTransition(t, api, token, path, j.ID, state, target, "status", 409)
			}
			matrixTransition(t, api, token, path, j.ID, state, "invoice:invoiced", "finalize", 409)
			cookie := sessionCookie(t, ctx, sessions, admin)
			documentTransactionRequest(t, web, cookie, "POST", path+"/comments", url.Values{"content": {"Allowed while settled"}}, 200)
			commercialUpload(t, web, cookie, "invoice", created.ID)
			w := serviceAPIRequest(api, token, "GET", path+"/pdf/source", nil)
			if w.Code != 200 {
				t.Fatalf("settlement blocked PDF review: %d %s", w.Code, w.Body.String())
			}
			var pdfReview struct {
				Data struct {
					Token string `json:"source_token"`
				} `json:"data"`
			}
			historyCheck(t, json.Unmarshal(w.Body.Bytes(), &pdfReview))
			w = serviceAPIRequest(api, token, "POST", path+"/pdf/generate", map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "source_token": pdfReview.Data.Token, "depends_on": []string{}})
			if w.Code != 200 {
				t.Fatalf("settlement blocked PDF generation: %d %s", w.Code, w.Body.String())
			}
			var pdfReceipt struct {
				Data struct {
					ID int64 `json:"version_id"`
				} `json:"data"`
			}
			historyCheck(t, json.Unmarshal(w.Body.Bytes(), &pdfReceipt))
			versionID := pdfReceipt.Data.ID
			w = serviceAPIRequest(api, token, "GET", fmt.Sprintf("%s/pdf/versions/%d", path, versionID), nil)
			if w.Code != 200 {
				t.Fatalf("settlement blocked PDF download: %d %s", w.Code, w.Body.String())
			}
			_, err = pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
			historyCheck(t, err)
			draft := map[string]any{"pdf_version_id": versionID, "confirm_stale_pdf": false, "to": []string{"recipient@example.test"}, "cc": []string{}, "bcc": []string{}, "subject": "Settled invoice", "text_body": "Receipt", "html_body": "", "extra_file_ids": []int64{}}
			w = serviceAPIRequest(api, token, "POST", path+"/deliveries/review", draft)
			if w.Code != 200 {
				t.Fatalf("settlement blocked send review: %d %s", w.Code, w.Body.String())
			}
			var review struct {
				Data struct {
					Token string `json:"review_token"`
				} `json:"data"`
			}
			historyCheck(t, json.Unmarshal(w.Body.Bytes(), &review))
			draft["operation_id"], draft["job_id"], draft["review_token"] = newOperationKey(), j.ID, review.Data.Token
			w = serviceAPIRequest(api, token, "POST", path+"/deliveries", draft)
			if w.Code != 200 {
				t.Fatalf("settlement blocked send: %d %s", w.Code, w.Body.String())
			}
			if after := matrixState(t, read()); after.Revision != state.Revision || after.Manual != state.Manual {
				t.Fatal("collaboration/PDF/queued send changed commercial revision/status")
			}
			var reversals int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM settlement_reversals`).Scan(&reversals))
			if reversals != 0 {
				t.Fatal("commercial action automatically unwound settlement")
			}
		})
	}
}

func TestCommercialServerSettlementCommitOrdersAPI(t *testing.T) {
	for _, source := range []string{"allocation", "credit application"} {
		for _, action := range []string{"edit", "void"} {
			for _, settlementFirst := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/settlementFirst=%v", source, action, settlementFirst), func(t *testing.T) {
					pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Settlement contender").SaveX(ctx)
					j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Settlement contender").SaveX(ctx)
					assignFieldDocumentCreator(t, pool, j.ID, admin)
					token, err := sessions.CreateMobile(ctx, admin, "Settlement contender")
					historyCheck(t, err)
					api := apiv1.NewRouter(pool, client, sessions)
					created := matrixState(t, lifecycleCall(t, api, token, "POST", "/invoices", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": j.ID, "customer_id": c.ID, "title": "Before race", "line_items": []map[string]any{{"title": "Work", "quantity": "1", "unit_price": "100.00"}}}), 200))
					path := fmt.Sprintf("/invoices/%d", created.ID)
					read := func() commercialMatrixState {
						return matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200))
					}
					matrixTransition(t, api, token, path, j.ID, read(), "invoice:invoiced", "finalize", 200)
					state := read()
					intent := map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "revision": state.Revision}
					method, target := "PATCH", path
					if action == "edit" {
						intent["notes"] = "Accepted lower total"
						intent["line_items"] = []map[string]any{{"title": "Work", "quantity": "1", "unit_price": "10.00"}}
					} else {
						method, target = "POST", path+"/status"
						for _, choice := range state.Workflow {
							if choice.Category == "invoice:void" {
								intent["status_id"], intent["meaning_revision"] = choice.ID, choice.Meaning
							}
						}
					}
					date := settlement.Date(time.Now().Format("2006-01-02"))
					svc := settlement.New(pool)
					var credit uuid.UUID
					if source == "credit application" {
						_, err := svc.RecordCustomerPayment(ctx, authorization.Actor{UserID: admin, Channel: "web"}, c.ID, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: date})
						historyCheck(t, err)
						historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE customer_id=$1`, c.ID).Scan(&credit))
					}
					paid := make(chan bool, 1)
					edited := make(chan *httptest.ResponseRecorder, 1)
					pay := func() {
						if source == "allocation" {
							w := serviceAPIRequest(api, token, "POST", path+"/payments", map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "amount": "25.00", "method": "cash", "date": string(date)})
							paid <- w.Code == 200
						} else {
							_, err := svc.ApplyCredit(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: created.ID, CreditID: credit, RequestedCents: 2500, EffectiveDate: date})
							paid <- err == nil
						}
					}
					edit := func() { edited <- serviceAPIRequest(api, token, method, target, intent) }
					barrier, err := pool.Begin(ctx)
					historyCheck(t, err)
					defer barrier.Rollback(context.Background())
					var holder, first int
					historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
					if settlementFirst {
						go pay()
					} else {
						go edit()
					}
					commercialWaiters(t, ctx, pool, holder, 1)
					historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&first))
					if settlementFirst {
						go edit()
					} else {
						go pay()
					}
					commercialWaiters(t, ctx, pool, first, 1)
					historyCheck(t, barrier.Commit(ctx))
					payment, w := <-paid, <-edited
					want := 200
					if settlementFirst {
						want = 409
					}
					if payment != settlementFirst || w.Code != want {
						t.Fatalf("incompatible order: settlement=%v edit=%d %s", payment, w.Code, w.Body.String())
					}
					var notes, total string
					var applied int64
					historyCheck(t, pool.QueryRow(ctx, `SELECT notes,financial::jsonb->'result'->>'total_cents',(SELECT coalesce(sum(amount_cents),0) FROM payment_invoice_allocations WHERE invoice_id=$1)+(SELECT coalesce(sum(amount_cents),0) FROM credit_applications WHERE invoice_id=$1) FROM invoices WHERE id=$1`, created.ID).Scan(&notes, &total, &applied))
					wantTotal, wantNotes, wantApplied := "10000", "", int64(2500)
					if !settlementFirst {
						wantApplied = 0
						if action == "edit" {
							wantTotal, wantNotes = "1000", "Accepted lower total"
						}
					}
					if total != wantTotal || notes != wantNotes || applied != wantApplied {
						t.Fatalf("partial race: total=%s notes=%q applied=%d", total, notes, applied)
					}
				})
			}
		}
	}
}
