package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The protected client supplies the real commercial request. Queue both owners
// at their shared admission lock, then observe the committed order, not timing.
func commercialProtectedSettlementRace(t *testing.T, pool *pgxpool.Pool, api http.Handler, request *http.Request, token, path string, company, admin, customer, invoice, job int64, creditSource, settlementFirst bool) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	date := settlement.Date(time.Now().Format("2006-01-02"))
	svc := settlement.New(pool)
	var credit uuid.UUID
	if creditSource {
		_, err := svc.RecordCustomerPayment(ctx, authorization.Actor{UserID: admin, Channel: "web"}, customer, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: date})
		historyCheck(t, err)
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE customer_id=$1`, customer).Scan(&credit))
	}
	paid := make(chan bool, 1)
	edited := make(chan *httptest.ResponseRecorder, 1)
	pay := func() {
		if creditSource {
			_, err := svc.ApplyCredit(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, CreditID: credit, RequestedCents: 2500, EffectiveDate: date})
			paid <- err == nil
		} else {
			w := serviceAPIRequest(api, token, "POST", path+"/payments", map[string]any{"operation_id": newOperationKey(), "job_id": job, "amount": "25.00", "method": "cash", "date": string(date)})
			paid <- w.Code == 200
		}
	}
	edit := func() {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, request)
		edited <- w
	}
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
	payment, response := <-paid, <-edited
	if payment != settlementFirst {
		t.Errorf("settlement accepted=%v, settlement-first=%v", payment, settlementFirst)
	}
	var applied, reversals int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT coalesce(sum(amount_cents),0) FROM payment_invoice_allocations WHERE invoice_id=$1)+(SELECT coalesce(sum(amount_cents),0) FROM credit_applications WHERE invoice_id=$1),(SELECT count(*) FROM settlement_reversals)`, invoice).Scan(&applied, &reversals))
	want := int64(0)
	if settlementFirst {
		want = 2500
	}
	if applied != want || reversals != 0 {
		t.Errorf("race applied=%d, want=%d, automatic reversals=%d", applied, want, reversals)
	}
	return response
}
