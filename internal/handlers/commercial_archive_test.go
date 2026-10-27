package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func archiveDraftFixture(t *testing.T, pool *pgxpool.Pool, client *ent.Client, company int64) *ent.Invoice {
	t.Helper()
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archive eligibility").SaveX(ctx)
	var status int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:draft'`, company).Scan(&status))
	return client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetSettlementState("paid").SetInvoiceDate(time.Now()).SetDueDate(time.Time{}).SetTitle("Zero-total Draft").SaveX(ctx)
}

func TestCommercialArchiveRejectsEffectivelyDraftHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	i := archiveDraftFixture(t, pool, client, company)
	var effective, settlement, before string
	historyCheck(t, pool.QueryRow(ctx, `SELECT s.category_key,i.settlement_state,to_jsonb(i)::text FROM invoices i JOIN invoice_effective_status e ON e.invoice_id=i.id JOIN statuses s ON s.id=e.status_id WHERE i.id=$1`, i.ID).Scan(&effective, &settlement, &before))
	if effective != "invoice:draft" || settlement != "paid" {
		t.Fatalf("fixture effective=%s settlement=%s", effective, settlement)
	}
	fieldPost(t, web, cookie, fmt.Sprintf("/invoices/%d/delete", i.ID), url.Values{}, 409)
	var after string
	historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(i)::text FROM invoices i WHERE id=$1`, i.ID).Scan(&after))
	if before != after {
		t.Fatal("rejected archive changed Invoice")
	}
}

func TestCommercialArchiveCommitRejectsEffectivelyDraft(t *testing.T) {
	pool, client, _, _, company, _ := permissionHTTPFixture(t)
	ctx := context.Background()
	i := archiveDraftFixture(t, pool, client, company)
	_, err := pool.Exec(ctx, `UPDATE invoices SET deleted_at=now() WHERE id=$1`, i.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "invoice archive requires paid or void" {
		t.Fatalf("expected effective-status archive rejection, got %v", err)
	}
}
