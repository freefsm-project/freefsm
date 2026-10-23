package handlers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
)

func TestConversionBackupRestoresCapturedCyclesAndSharedReceipts(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL disposable cluster required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "Conversion source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Conversion destination")
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
	history := exerciseConversionHistory(t, src.pool, src.client, web, sessionCookie(t, ctx, src.sessions, src.actor), src.company)
	tables := []string{"estimates", "invoices", "estimate_invoice_conversion_cycles", "commercial_operations", "operation_admissions", "custom_field_definitions", "files", "comments", "tag_links", "document_pdf_versions", "document_deliveries", "document_delivery_events", "activity_logs"}
	before := historyDurableRows(t, src.pool, tables)
	var auditThrough int64
	var originalAudit string
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT max(id),jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a`).Scan(&auditThrough, &originalAudit))
	src.roundTripTo(t, dst)
	historyCheck(t, dst.manager.RefreshConnections())
	// Restore retains the destination host upload root. Compare the complete
	// File rows with only that documented absolute-root relocation applied.
	before["files"] = strings.ReplaceAll(before["files"], src.uploadDir+"/", dst.uploadDir+"/")
	after := historyDurableRows(t, dst.pool, tables)
	for _, table := range tables {
		historyEqual(t, "restored "+table, after[table], before[table])
	}
	var restoredAudit string
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a WHERE id<=$1`, auditThrough).Scan(&restoredAudit))
	historyEqual(t, "every original immutable authorization audit row", restoredAudit, originalAudit)
	web = middleware.Company(services.NewCompanySettingsService(dst.client))(New(dst.pool, dst.client, dst.sessions, &config.Config{UploadDir: dst.uploadDir, MaxUploadSize: 25 << 20}))
	cookie := sessionCookie(t, ctx, dst.sessions, src.actor)
	for _, command := range history.commands {
		body := requestBody(t, web, cookie, "GET", "/commercial-operations/"+command.values.Get("idempotency_key"), 200)
		assertContains(t, body, `"outcome":"accepted"`)
		fieldPost(t, web, cookie, command.path, command.values, 303)
	}
	historyEqual(t, "restored command replay has no new cycle or relation effects", historyDurableRows(t, dst.pool, tables), before)
	for name, want := range map[string]string{"source.txt": "Original file bytes", "invoice.txt": "Current Invoice file bytes"} {
		var file int64
		historyCheck(t, dst.pool.QueryRow(ctx, `SELECT id FROM files WHERE original_name=$1`, name).Scan(&file))
		assertContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/files/%d", file), 200), want)
	}
}
