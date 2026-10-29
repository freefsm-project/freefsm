package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/freefsm-project/freefsm/internal/testutil"
	"github.com/google/uuid"
)

func TestCreditApplicationInstalledHistoryUpgradeHTTP(t *testing.T) {
	source := database.MigrationFS()
	old := fstest.MapFS{}
	files, err := fs.ReadDir(source, ".")
	historyCheck(t, err)
	for _, file := range files {
		if file.Name() >= "093_" {
			continue
		}
		b, err := fs.ReadFile(source, file.Name())
		historyCheck(t, err)
		old[file.Name()] = &fstest.MapFile{Data: b}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed Credit history").SaveX(ctx)
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	documentTransactionRequest(t, web, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Installed Application destination"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var invoice int64
	var credit uuid.UUID
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits`).Scan(&credit))
	historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice))
	key := newOperationKey()
	application, err := settlement.New(pool).ApplyCredit(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: key}, InvoiceID: invoice, CreditID: credit, RequestedCents: 6000, EffectiveDate: "2026-01-02"})
	historyCheck(t, err)
	// This is an actual pre093 schema. Record the old reversal shape, which had
	// no reference/note columns, without synthesizing any missing metadata.
	reversalID, reverseKey := uuid.New(), newOperationKey()
	_, err = pool.Exec(ctx, `INSERT INTO settlement_reversals(id,company_id,operation_type,operation_id,reason,actor_id,effective_date) VALUES($1,$2,'credit_application',$3,'Installed correction',$4,'2026-01-02')`, reversalID, company, application.ID, admin)
	historyCheck(t, err)
	// Recorded pre093 semantic request shape: optional metadata did not exist.
	canonical := fmt.Sprintf(`{"CustomerID":0,"EffectiveDate":"2026-01-02","ID":%q,"InvoiceID":%d,"Reason":"Installed correction"}`, application.ID.String(), invoice)
	hash := sha256.Sum256([]byte(canonical))
	_, err = pool.Exec(ctx, `INSERT INTO settlement_idempotency(company_id,operation,idempotency_key,request_fingerprint,result_id,result_json) VALUES($1,'reverse_credit_application',$2,$3,$4::uuid,jsonb_build_object('ID',$4::uuid::text,'AppliedCents',0,'CreditCents',0,'State',''))`, company, reverseKey, hex.EncodeToString(hash[:]), reversalID)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `SELECT settlement_set_invoice_state($1,'unpaid')`, invoice)
	historyCheck(t, err)
	// Real pre093 refund portions and compensations must survive unchanged too.
	refundKey := newOperationKey()
	refund, err := settlement.New(pool).RefundCredit(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: refundKey}, CustomerID: c.ID, AmountCents: 2000, Method: settlement.Check, EffectiveDate: "2026-01-02", Reason: "Installed external refund"})
	historyCheck(t, err)
	refundReverseKey, refundReverseID := newOperationKey(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO settlement_reversals(id,company_id,operation_type,operation_id,reason,actor_id,effective_date) VALUES($1,$2,'credit_refund',$3,'Installed refund correction',$4,'2026-01-02')`, refundReverseID, company, refund.ID, admin)
	historyCheck(t, err)
	canonical = fmt.Sprintf(`{"CustomerID":%d,"EffectiveDate":"2026-01-02","ID":%q,"InvoiceID":0,"Reason":"Installed refund correction"}`, c.ID, refund.ID.String())
	hash = sha256.Sum256([]byte(canonical))
	_, err = pool.Exec(ctx, `INSERT INTO settlement_idempotency(company_id,operation,idempotency_key,request_fingerprint,result_id,result_json) VALUES($1,'reverse_credit_refund',$2,$3,$4::uuid,jsonb_build_object('ID',$4::uuid::text,'AppliedCents',0,'CreditCents',0,'State',''))`, company, refundReverseKey, hex.EncodeToString(hash[:]), refundReverseID)
	historyCheck(t, err)
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"30.00"}, "method": {"cash"}, "date": {"2026-01-03"}}, 303)
	var oldPayment uuid.UUID
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoice_payments WHERE received_date='2026-01-03'`).Scan(&oldPayment))
	paymentReverseID := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO settlement_reversals(id,company_id,operation_type,operation_id,reason,actor_id,effective_date) VALUES($1,$2,'payment',$3,'Historical bank return prose',$4,'2026-01-03')`, paymentReverseID, company, oldPayment, admin)
	historyCheck(t, err)
	tables := []string{"credit_applications", "credit_refunds", "credit_refund_allocations", "customer_credits", "invoice_payments", "settlement_idempotency", "invoices", "authorization_grants"}
	before := preLeaseUpgradeRows(t, pool, tables)
	sequences := testutil.Sequences(t, ctx, pool)
	var oldReversal string
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(r) ORDER BY id)::text FROM settlement_reversals r`).Scan(&oldReversal))
	for range 2 {
		historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, source))
	}
	testutil.AssertNoInvoiceLeaseBackfill(t, ctx, pool)
	testutil.AssertSequenceUpgrade(t, ctx, pool, sequences, "lease_billing_obligations_id_seq", "leases_id_seq")
	historyEqual(t, "installed Applications and grants unchanged", preLeaseUpgradeRows(t, pool, tables), before)
	var after string
	var unknown bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(r)-'reference'-'notes' ORDER BY id)::text,bool_and(reference IS NULL AND notes IS NULL) FROM settlement_reversals r`).Scan(&after, &unknown))
	if after != oldReversal || !unknown {
		t.Fatal("upgrade rewrote reversal or invented metadata")
	}
	refundPath := fmt.Sprintf("/customers/%d/credit/refunds", c.ID)
	documentTransactionRequest(t, web, cookie, "POST", refundPath, url.Values{"idempotency_key": {refundKey}, "amount": {"20.00"}, "method": {"check"}, "effective_date": {"2026-01-02"}, "reason": {"Installed external refund"}}, 303)
	documentTransactionRequest(t, web, cookie, "POST", refundPath+"/"+refund.ID.String()+"/reverse", url.Values{"idempotency_key": {refundReverseKey}, "effective_date": {"2026-01-02"}, "reason": {"Installed refund correction"}}, 303)
	assertCreditApplicationReceipt(t, documentTransactionRequest(t, web, cookie, "GET", "/refund-operations/"+refundKey, nil, 200), refundKey, refund.ID.String(), "credit_refund")
	assertCreditApplicationReceipt(t, documentTransactionRequest(t, web, cookie, "GET", "/refund-operations/"+refundReverseKey, nil, 200), refundReverseKey, refundReverseID.String(), "credit_refund_reversal")
	page := documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/customers/%d/refunds", c.ID), nil, 200)
	assertContains(t, page.Body.String(), "Historical bank return prose")
	lookup := documentTransactionRequest(t, web, cookie, "GET", "/credit-application-operations/"+key, nil, 200)
	assertCreditApplicationReceipt(t, lookup, key, application.ID.String(), "credit_application")
	assertCreditApplicationReceipt(t, documentTransactionRequest(t, web, cookie, "GET", "/credit-application-operations/"+reverseKey, nil, 200), reverseKey, reversalID.String(), "credit_application_reversal")
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/invoices/%d/credit-applications", invoice), url.Values{"idempotency_key": {key}, "credit_id": {credit.String()}, "amount": {"60.00"}, "effective_date": {"2026-01-02"}}, 303)
	historyEqual(t, "installed replay preserves original reversed Application", preLeaseUpgradeRows(t, pool, tables), before)
	assertCreditApplicationReceipt(t, documentTransactionRequest(t, web, cookie, "GET", "/credit-application-operations/"+key, nil, 200), key, application.ID.String(), "credit_application")
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/invoices/%d/credit-applications/%s/reverse", invoice, application.ID), url.Values{"idempotency_key": {reverseKey}, "effective_date": {"2026-01-02"}, "reason": {"Installed correction"}}, 303)
	assertCreditApplicationReceipt(t, documentTransactionRequest(t, web, cookie, "GET", "/credit-application-operations/"+reverseKey, nil, 200), reverseKey, reversalID.String(), "credit_application_reversal")
	historyEqual(t, "installed compensation replay keeps its original effect", preLeaseUpgradeRows(t, pool, tables), before)
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/invoices/%d/credit-applications", invoice), url.Values{"idempotency_key": {key}, "credit_id": {credit.String()}, "amount": {"60.01"}, "effective_date": {"2026-01-02"}}, 409)
	// Old per-family keys could collide. Both real effects remain recorded, but
	// an untyped lookup must not choose an arbitrary effect identity for the key.
	_, err = pool.Exec(ctx, `INSERT INTO settlement_idempotency(company_id,operation,idempotency_key,request_fingerprint,result_id,result_json) SELECT company_id,operation,$1,request_fingerprint,result_id,result_json FROM settlement_idempotency WHERE operation='reverse_credit_application' AND idempotency_key=$2`, key, reverseKey)
	historyCheck(t, err)
	documentTransactionRequest(t, web, cookie, "GET", "/credit-application-operations/"+key, nil, 409)
}
