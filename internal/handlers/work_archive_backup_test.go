package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
)

func TestWorkArchiveEncryptedBackupPreservesHistory(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL disposable owned cluster required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "Archived work source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Archived work destination")
	for _, workflow := range []struct {
		kind       string
		categories []string
	}{
		{"job", []string{"new", "travel_time", "in_progress", "pending", "completed", "canceled"}},
		{"project", []string{"new", "in_progress", "pending", "completed", "canceled"}},
		{"invoice", []string{"draft", "invoiced", "sent", "partially_paid", "paid", "void"}},
	} {
		tx, err := src.pool.Begin(ctx)
		historyCheck(t, err)
		var id int64
		historyCheck(t, tx.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,$2,$2) RETURNING id`, src.company, workflow.kind).Scan(&id))
		for _, category := range workflow.categories {
			_, err = tx.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES($1,$2,$3,$4,1,true)`, src.company, id, category, workflow.kind+":"+category)
			historyCheck(t, err)
		}
		historyCheck(t, tx.Commit(ctx))
	}
	web := middleware.Company(services.NewCompanySettingsService(src.client))(New(src.pool, src.client, src.sessions, &config.Config{UploadDir: src.uploadDir, MaxUploadSize: 25 << 20}))
	h := exerciseWorkArchiveHistory(t, src.pool, src.client, web, sessionCookie(t, ctx, src.sessions, src.actor), src.company)
	_, err = authorization.New(src.pool).SaveRole(ctx, authorization.Actor{UserID: src.actor}, 0, "Work history without Restore", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.archived.view", Scope: authorization.All}, {Permission: "project.view", Scope: authorization.All}, {Permission: "project.archived.view", Scope: authorization.All}})
	historyCheck(t, err)
	before := historyDurableRows(t, src.pool, h.tables)
	var auditThrough int64
	var originalAudit string
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT max(id),jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a`).Scan(&auditThrough, &originalAudit))
	src.roundTripTo(t, dst)
	historyCheck(t, dst.manager.RefreshConnections())
	historyEqual(t, "encrypted backup preserves archived work, active clock/GPS, pending Delivery, settlement, progress, historical masters and grants", historyDurableRows(t, dst.pool, h.tables), before)
	var restoredAudit string
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a WHERE id<=$1`, auditThrough).Scan(&restoredAudit))
	historyEqual(t, "backup preserves immutable work lifecycle evidence", restoredAudit, originalAudit)
	web = middleware.Company(services.NewCompanySettingsService(dst.client))(New(dst.pool, dst.client, dst.sessions, &config.Config{UploadDir: dst.uploadDir, MaxUploadSize: 25 << 20}))
	cookie := sessionCookie(t, ctx, dst.sessions, src.actor)
	for _, target := range []struct {
		kind string
		id   int64
	}{{"job", h.job}, {"project", h.project}} {
		path := fmt.Sprintf("/%ss/%d", target.kind, target.id)
		assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Archived on")
		fieldPost(t, web, cookie, path+"/restore", nil, 303)
	}
	for _, table := range h.tables {
		if table == "jobs" || table == "projects" || table == "activity_logs" {
			continue
		}
		historyEqual(t, "post-backup Restore does not revive or rewrite "+table, historyDurableRows(t, dst.pool, []string{table})[table], before[table])
	}
}
