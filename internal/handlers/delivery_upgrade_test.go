package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/delivery"
)

func TestDeliveryInstalledOutboxUpgradePreservesKnownAndUnknown(t *testing.T) {
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	historyCheck(t, err)
	for _, entry := range entries {
		if entry.Name() >= "096" {
			continue
		}
		raw, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		historyCheck(t, err)
		prior[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, prior)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed customer").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Current title must not be rendered").SaveX(ctx)
	_, err = pool.Exec(ctx, `UPDATE company_settings SET smtp_from='current@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
	historyCheck(t, err)
	for _, row := range []struct {
		key, sender, state, message string
		attempts                    int
	}{{"56000000-0000-4000-8000-000000000001", "original@example.test", "queued", "<installed-pending@example.test>", 2}, {"56000000-0000-4000-8000-000000000002", "", "queued", "<unknown-sender@example.test>", 0}, {"56000000-0000-4000-8000-000000000003", "original@example.test", "accepted", "<installed-accepted@example.test>", 1}} {
		var id int64
		historyCheck(t, pool.QueryRow(ctx, `INSERT INTO document_deliveries(company_id,document_type,document_id,estimate_id,actor_id,idempotency_key,request_fingerprint,recipients_to,recipients_cc,recipients_bcc,subject,text_body,html_body,pdf_data,pdf_filename,message_id,sender_address,state,lifetime_attempt_count,cycle_attempt_count,accepted_at,provider_identifier,acceptance_evidence,created_at)
	  VALUES($1,'estimate',$2,$2,$3,$4,decode(repeat('ab',32),'hex'),ARRAY['installed@example.test'],ARRAY['installed-copy@example.test'],ARRAY['installed-private@example.test'],'Installed subject','Installed text','<p>Installed HTML</p>',$5,'installed.pdf',$6,NULLIF($7,''),$8,$9,$9,CASE WHEN $8='accepted' THEN '2026-01-02T03:04:05Z'::timestamptz END,CASE WHEN $8='accepted' THEN 'smtp-installed' ELSE '' END,CASE WHEN $8='accepted' THEN '{"transport":"smtp","accepted":true}'::jsonb ELSE '{}'::jsonb END,'2026-01-01T00:00:00Z') RETURNING id`, company, doc.ID, admin, row.key, []byte("%PDF-1.3\ninstalled exact bytes\n%%EOF"), row.message, row.sender, row.state, row.attempts).Scan(&id))
		_, err = pool.Exec(ctx, `INSERT INTO document_delivery_events(delivery_id,company_id,actor_id,event_kind,to_state,evidence,created_at) VALUES($1,$2,$3,'state',$4,'{"recorded":"installed history"}','2026-01-01T00:00:00Z')`, id, company, admin, row.state)
		historyCheck(t, err)
	}
	var inflight int64
	historyCheck(t, pool.QueryRow(ctx, `INSERT INTO document_deliveries(company_id,document_type,document_id,estimate_id,actor_id,idempotency_key,request_fingerprint,recipients_to,subject,text_body,pdf_data,pdf_filename,message_id,sender_address,state,lifetime_attempt_count,cycle_attempt_count,attempt_token,lease_expires_at)
	VALUES($1,'estimate',$2,$2,$3,'56000000-0000-4000-8000-000000000004',decode(repeat('cd',32),'hex'),ARRAY['installed@example.test'],'Installed in flight','Unknown transport result',$4,'installed.pdf','<installed-inflight@example.test>','original@example.test','sending',2,2,'56000000-0000-4000-8000-000000000005',clock_timestamp()+interval '1 hour') RETURNING id`, company, doc.ID, admin, []byte("%PDF-1.3\ninstalled exact bytes\n%%EOF")).Scan(&inflight))
	before := historyDurableRows(t, pool, []string{"document_deliveries", "document_delivery_events", "authorization_grants"})
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	var upgraded string
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(d)-ARRAY['attachment_snapshots','composition_snapshot','handoff_started_at','dispatch_boundary_known','cancelled_at'] ORDER BY (to_jsonb(d)-ARRAY['attachment_snapshots','composition_snapshot','handoff_started_at','dispatch_boundary_known','cancelled_at'])::text)::text FROM document_deliveries d`).Scan(&upgraded))
	historyEqual(t, "upgrade retains all recorded outbox facts", upgraded, before["document_deliveries"])
	retained := historyDurableRows(t, pool, []string{"document_delivery_events", "authorization_grants"})
	historyEqual(t, "no fabricated historical events", retained["document_delivery_events"], before["document_delivery_events"])
	historyEqual(t, "no new nonprotected grants", retained["authorization_grants"], before["authorization_grants"])
	var invented bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM document_deliveries WHERE attachment_snapshots IS NOT NULL OR composition_snapshot IS NOT NULL OR handoff_started_at IS NOT NULL OR dispatch_boundary_known IS NOT NULL OR cancelled_at IS NOT NULL)`).Scan(&invented))
	if invented {
		t.Fatal("upgrade invented historical snapshots/dispatch")
	}
	svc := delivery.New(pool, "")
	history, err := svc.History(ctx, company, delivery.DocumentRef{Type: "estimate", ID: doc.ID})
	historyCheck(t, err)
	unknown := false
	for _, row := range history {
		if strings.Contains(row.LastError, "explicit review") {
			if row.SenderRecorded {
				t.Fatal("unknown sender reported as recorded")
			}
			unknown = true
		}
	}
	if !unknown {
		t.Fatal("unknown legacy sender not disclosed for review")
	}
	assertContains(t, requestBody(t, web, sessionCookie(t, ctx, sessions, admin), "GET", fmt.Sprintf("/estimates/%d", doc.ID), 200), "Legacy sender snapshot unknown")
	assertContains(t, requestBody(t, web, sessionCookie(t, ctx, sessions, admin), "GET", fmt.Sprintf("/estimates/%d", doc.ID), 200), "Legacy transport handoff unknown")
	sender := &compositionSender{}
	worked, err := svc.ProcessOne(ctx, sender, nil)
	historyCheck(t, err)
	if !worked || len(sender.messages) != 1 {
		t.Fatal("known installed pending work lost")
	}
	sent := sender.messages[0]
	if len(sent.CC) != 1 || sent.CC[0] != "installed-copy@example.test" || len(sent.BCC) != 1 || sent.BCC[0] != "installed-private@example.test" {
		t.Fatal("installed recipient arrays were split or rewritten")
	}
	if sent.From != "original@example.test" || sent.MessageID != "<installed-pending@example.test>" || sent.Subject != "Installed subject" || sent.TextBody != "Installed text" || sent.HTMLBody != "<p>Installed HTML</p>" || string(sent.PDF) != "%PDF-1.3\ninstalled exact bytes\n%%EOF" || sent.LifetimeAttemptCount != 3 {
		t.Fatalf("legacy snapshot regenerated or attempts lost: %+v", sent)
	}
	worked, err = svc.ProcessOne(ctx, sender, nil)
	historyCheck(t, err)
	if worked || len(sender.messages) != 1 {
		t.Fatal("unknown legacy sender silently substituted")
	}
	_, err = pool.Exec(ctx, `UPDATE estimates SET deleted_at=now() WHERE id=$1`, doc.ID)
	historyCheck(t, err)
	var state string
	historyCheck(t, pool.QueryRow(ctx, `SELECT state FROM document_deliveries WHERE id=$1`, inflight).Scan(&state))
	if state != "sending" {
		t.Fatal("unknown historical handoff was asserted unhanded")
	}
	_, err = pool.Exec(ctx, `UPDATE document_deliveries SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, inflight)
	historyCheck(t, err)
	worked, err = svc.ProcessOne(ctx, sender, nil)
	historyCheck(t, err)
	if worked || len(sender.messages) != 1 {
		t.Fatal("archived unknown attempt retried")
	}
	var honest bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT evidence->'dispatch_admitted'='null'::jsonb FROM document_delivery_events WHERE delivery_id=$1 AND to_state='cancelled'`, inflight).Scan(&honest))
	if !honest {
		t.Fatal("cancellation manufactured historical dispatch knowledge")
	}
	var versions, delivered int
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM document_pdf_versions),(SELECT count(*) FROM document_deliveries WHERE delivered_at IS NOT NULL)`).Scan(&versions, &delivered))
	if versions != 0 || delivered != 0 {
		t.Fatal("upgrade invented PDF generation or delivery evidence")
	}
}

func TestDeliveryUpgradeCancelsCurrentlyUnavailablePendingWork(t *testing.T) {
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	historyCheck(t, err)
	for _, entry := range entries {
		if entry.Name() >= "096" {
			continue
		}
		raw, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		historyCheck(t, err)
		prior[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	pool, client, _, _, company, admin := permissionHTTPFixtureMigrations(t, prior)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Unavailable installed source").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Original").SaveX(ctx)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `INSERT INTO document_deliveries(company_id,document_type,document_id,estimate_id,actor_id,idempotency_key,request_fingerprint,recipients_to,subject,text_body,pdf_data,pdf_filename,message_id,sender_address,lifetime_attempt_count,cycle_attempt_count)
 VALUES($1,'estimate',$2,$2,$3,'56000000-0000-4000-8000-000000000010',decode(repeat('ef',32),'hex'),ARRAY['original@example.test'],'Recorded subject','Recorded message',$4,'original.pdf','<unavailable-installed@example.test>','recorded@example.test',2,2) RETURNING id`, company, doc.ID, admin, []byte("%PDF-1.3\nrecorded bytes\n%%EOF")).Scan(&id))
	client.Estimate.UpdateOne(doc).SetDeletedAt(time.Now()).SaveX(ctx)
	var started time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&started))
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	var state, reason, subject, messageID, sender string
	var pdf []byte
	var attempts int
	var currentCancellation bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT state,last_error,subject,message_id,sender_address,pdf_data,lifetime_attempt_count,coalesce(cancelled_at>=$2,false) FROM document_deliveries WHERE id=$1`, id, started).Scan(&state, &reason, &subject, &messageID, &sender, &pdf, &attempts, &currentCancellation))
	if state != "cancelled" || !currentCancellation || !strings.Contains(reason, "dispatch fencing") {
		t.Fatalf("unavailable installed work can revive: %s %s", state, reason)
	}
	if subject != "Recorded subject" || messageID != "<unavailable-installed@example.test>" || sender != "recorded@example.test" || string(pdf) != "%PDF-1.3\nrecorded bytes\n%%EOF" || attempts != 2 {
		t.Fatal("upgrade rewrote immutable installed content/attempts")
	}
	var unknown bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT evidence->'dispatch_admitted'='null'::jsonb FROM document_delivery_events WHERE delivery_id=$1 AND to_state='cancelled'`, id).Scan(&unknown))
	if !unknown {
		t.Fatal("upgrade invented historical handoff knowledge")
	}
	client.Estimate.UpdateOne(doc).ClearDeletedAt().SaveX(ctx)
	capture := &compositionSender{}
	worked, err := delivery.New(pool, "").ProcessOne(ctx, capture, nil)
	historyCheck(t, err)
	if worked || len(capture.messages) != 0 {
		t.Fatal("restoring the source restarted cancelled installed mail")
	}
}
