package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/jackc/pgx/v5"
)

func TestBoundedPaymentBackupRestoreReconciliationAPI(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL disposable cluster required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "Bounded source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Bounded destination")
	tx, err := src.pool.Begin(ctx)
	historyCheck(t, err)
	defer tx.Rollback(ctx)
	var workflow int64
	historyCheck(t, tx.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Invoices','invoice') RETURNING id`, src.company).Scan(&workflow))
	for _, category := range []string{"draft", "invoiced", "sent", "partially_paid", "paid", "void"} {
		_, err := tx.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES($1,$2,$3,$4,1,true)`, src.company, workflow, category, "invoice:"+category)
		historyCheck(t, err)
	}
	historyCheck(t, tx.Commit(ctx))
	c := src.client.Customer.Create().SetCompanyID(src.company).SetDisplayName("Restored bounded Customer").SaveX(ctx)
	j := src.client.Job.Create().SetCompanyID(src.company).SetCustomerID(c.ID).SetJobType("Restored bounded Job").SaveX(ctx)
	api := apiv1.NewRouter(src.pool, src.client, src.sessions)
	created := lifecycleCall(t, api, src.mobile, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Restored fixed payment","line_items":[{"title":"Work","quantity":"1","unit_price":"100.00"}]}`, newOperationKey(), newOperationKey(), j.ID), 200)
	var document struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	historyCheck(t, json.Unmarshal([]byte(created), &document))
	id := document.Data.ID
	historyCheck(t, statusflow.New(src.pool).FinalizeInvoice(ctx, statusflow.Actor{ID: src.actor, CompanyID: src.company}, id))
	historyCheck(t, settlement.New(src.pool).RenamePaymentMethod(ctx, authorization.Actor{UserID: src.actor, Channel: "web"}, settlement.Cash, "Notes and coins"))
	op, uncertain := newOperationKey(), newOperationKey()
	path := fmt.Sprintf("/invoices/%d/payments", id)
	command := func(op string) string {
		return fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"amount":"25.00","method":"cash","date":"2026-01-02"}`, op, j.ID)
	}
	accepted := boundedAPICall(api, src.mobile, "POST", path, command(op))
	if accepted.Code != 200 {
		t.Fatalf("source Payment %d: %s", accepted.Code, accepted.Body.String())
	}
	tables := []string{"invoice_payments", "payment_invoice_allocations", "invoices", "bounded_payment_operations", "operation_admissions", "payment_method_labels"}
	before := historyDurableRows(t, src.pool, tables)
	src.roundTripTo(t, dst, func() {
		w := boundedAPICall(api, src.mobile, "POST", path, command(uncertain))
		if w.Code != 200 {
			t.Fatalf("post-backup effect %d: %s", w.Code, w.Body.String())
		}
	})
	historyCheck(t, dst.manager.RefreshConnections())
	historyEqual(t, "bounded settlement and operation identity", historyDurableRows(t, dst.pool, tables), before)
	if _, err := dst.sessions.ValidateMobile(ctx, src.mobile); err == nil {
		t.Fatal("restore retained original bearer")
	}
	fresh, err := dst.sessions.CreateMobile(ctx, src.actor, "Fresh restore reconciliation")
	historyCheck(t, err)
	api = apiv1.NewRouter(dst.pool, dst.client, dst.sessions)
	receipt := boundedAPICall(api, fresh, "GET", "/payment-operations/"+op, "")
	if receipt.Code != 200 || receipt.Body.String() != accepted.Body.String() {
		t.Fatalf("fresh authenticated read-only reconciliation %d: %s", receipt.Code, receipt.Body.String())
	}
	if w := boundedAPICall(api, fresh, "POST", path, command(op)); w.Code != 409 {
		t.Fatalf("new client replay rebound old intent: %d %s", w.Code, w.Body.String())
	}
	if w := boundedAPICall(api, fresh, "GET", "/payment-operations/"+uncertain, ""); w.Code != 404 {
		t.Fatalf("restore invented lost history: %d %s", w.Code, w.Body.String())
	}
	// No replacement/replay is submitted for that uncertain post-backup intent.
	historyEqual(t, "reconciliation remains read only", historyDurableRows(t, dst.pool, tables), before)
}
