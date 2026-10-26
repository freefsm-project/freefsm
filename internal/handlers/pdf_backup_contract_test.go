package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/jackc/pgx/v5"
)

func TestPDFBackupRestoresBytesSourcesReceiptsAndAudit(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL disposable cluster required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "PDF source")
	dst := newHistoryBackupInstance(t, admin, dsn, "PDF destination")
	c := src.client.Customer.Create().SetCompanyID(src.company).SetDisplayName("Retained PDF customer").SaveX(ctx)
	j := src.client.Job.Create().SetCompanyID(src.company).SetCustomerID(c.ID).SetJobType("Retained PDF Job").SaveX(ctx)
	i := src.client.Invoice.Create().SetCompanyID(src.company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Retained PDF invoice").SetTerms("Captured terms").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	api := apiv1.NewRouter(src.pool, src.client, src.sessions)
	call := func(method, path, body string) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+src.mobile)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s=%d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	path := fmt.Sprintf("/invoices/%d/pdf", i.ID)
	var review struct {
		Data struct {
			Token string `json:"source_token"`
		}
	}
	historyCheck(t, json.Unmarshal([]byte(call("GET", path+"/source", "")), &review))
	key := newOperationKey()
	call("POST", path+"/generate", fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]}`, key, j.ID, review.Data.Token))
	web := New(src.pool, src.client, src.sessions, &config.Config{UploadDir: src.uploadDir, MaxUploadSize: 25 << 20})
	cookie := sessionCookie(t, ctx, src.sessions, src.actor)
	original := requestBody(t, web, cookie, "GET", path, 200)
	var version int64
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM document_pdf_versions`).Scan(&version))
	src.client.Invoice.UpdateOne(i).SetNotes("Changed after retained generation").SaveX(ctx)
	requestBody(t, web, cookie, "GET", fmt.Sprintf("%s?version=%d&confirm_stale=1", path, version), 200)
	tables := []string{"document_pdf_versions", "pdf_operations", "operation_admissions"}
	before := historyDurableRows(t, src.pool, tables)
	var through int64
	var audit string
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT max(id),jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a`).Scan(&through, &audit))
	src.roundTripTo(t, dst)
	historyCheck(t, dst.manager.RefreshConnections())
	historyEqual(t, "PDF bytes, exact source, generation metadata and receipts", historyDurableRows(t, dst.pool, tables), before)
	var restored string
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(a) ORDER BY id)::text FROM authorization_audit a WHERE id<=$1`, through).Scan(&restored))
	historyEqual(t, "PDF immutable audit", restored, audit)
	web = New(dst.pool, dst.client, dst.sessions, &config.Config{UploadDir: dst.uploadDir, MaxUploadSize: 25 << 20})
	if got := requestBody(t, web, sessionCookie(t, ctx, dst.sessions, src.actor), "GET", fmt.Sprintf("%s?version=%d&confirm_stale=1", path, version), 200); got != original {
		t.Fatal("restore changed retained PDF bytes")
	}
}
