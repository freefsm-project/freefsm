package handlers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
)

func TestCommercialArchiveBackupPreservesClosedHistoryAndRestore(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL disposable owned cluster required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "Archived commercial source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Archived commercial destination")
	for _, kind := range []string{"estimate", "invoice"} {
		tx, err := src.pool.Begin(ctx)
		historyCheck(t, err)
		var workflow int64
		historyCheck(t, tx.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,$2,$2) RETURNING id`, src.company, kind).Scan(&workflow))
		categories := []string{"draft", "estimate", "sent", "accepted", "rejected", "completed"}
		if kind == "invoice" {
			categories = []string{"draft", "invoiced", "sent", "partially_paid", "paid", "void"}
		}
		for _, category := range categories {
			_, err := tx.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES($1,$2,$3,$4,1,true)`, src.company, workflow, category, kind+":"+category)
			historyCheck(t, err)
		}
		historyCheck(t, tx.Commit(ctx))
	}
	web := middleware.Company(services.NewCompanySettingsService(src.client))(New(src.pool, src.client, src.sessions, &config.Config{UploadDir: src.uploadDir, MaxUploadSize: 25 << 20}))
	cookie := sessionCookie(t, ctx, src.sessions, src.actor)
	history := exerciseConversionHistory(t, src.pool, src.client, web, cookie, src.company)
	path := fmt.Sprintf("/invoices/%d", history.current)
	fieldPost(t, web, cookie, path+"/delete", nil, 303)
	_, err = authorization.New(src.pool).SaveRole(ctx, authorization.Actor{UserID: src.actor}, 0, "Archived history viewer without Restore", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.archived.view", Scope: authorization.All}})
	historyCheck(t, err)
	tables := []string{"customers", "estimates", "invoices", "estimate_invoice_conversion_cycles", "commercial_operations", "operation_admissions", "custom_field_definitions", "files", "comments", "tag_links", "document_pdf_versions", "document_deliveries", "document_delivery_events", "activity_logs", "authorization_roles", "authorization_grants"}
	before := historyDurableRows(t, src.pool, tables)
	var auditThrough int64
	var originalAudit string
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT max(id),jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a`).Scan(&auditThrough, &originalAudit))
	src.roundTripTo(t, dst)
	historyCheck(t, dst.manager.RefreshConnections())
	before["files"] = strings.ReplaceAll(before["files"], src.uploadDir+"/", dst.uploadDir+"/")
	historyEqual(t, "backup retains closed document, cycles, exact Delivery/PDF bytes, roles and grants", historyDurableRows(t, dst.pool, tables), before)
	var restoredAudit string
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a WHERE id<=$1`, auditThrough).Scan(&restoredAudit))
	historyEqual(t, "backup preserves original immutable lifecycle evidence", restoredAudit, originalAudit)
	web = middleware.Company(services.NewCompanySettingsService(dst.client))(New(dst.pool, dst.client, dst.sessions, &config.Config{UploadDir: dst.uploadDir, MaxUploadSize: 25 << 20}))
	cookie = sessionCookie(t, ctx, dst.sessions, src.actor)
	var documentBefore, documentAfter string
	query := `SELECT (to_jsonb(d)-'deleted_at'-'updated_at'-'commercial_revision')::text FROM invoices d WHERE id=$1`
	historyCheck(t, dst.pool.QueryRow(ctx, query, history.current).Scan(&documentBefore))
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Archived on")
	fieldPost(t, web, cookie, path+"/restore", nil, 303)
	historyCheck(t, dst.pool.QueryRow(ctx, query, history.current).Scan(&documentAfter))
	historyEqual(t, "post-backup explicit Restore changes no commercial facts", documentAfter, documentBefore)
	for _, table := range []string{"customers", "estimates", "estimate_invoice_conversion_cycles", "commercial_operations", "operation_admissions", "custom_field_definitions", "files", "comments", "tag_links", "document_pdf_versions", "document_deliveries", "document_delivery_events", "authorization_roles", "authorization_grants"} {
		historyEqual(t, "explicit Restore does not revive or rewrite "+table, historyDurableRows(t, dst.pool, []string{table})[table], before[table])
	}
}
