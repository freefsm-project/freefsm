package database

import (
	"os"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/credentials"
)

func TestMailMapUpgradePreservesRecordedFacts(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := activityMigrationDatabaseThrough048(t, dsn)
	if err := db.Migrate(ctx, legacyPreflightFS(t)); err != nil {
		t.Fatal(err)
	}
	_, err := db.Pool.Exec(ctx, `UPDATE company_settings SET smtp_host='old-provider.example',smtp_password='legacy-format',map_tile_url='https://old-tiles.example/{z}/{x}/{y}',geocoder_url='https://old-geocoder.example';
 INSERT INTO users(id,company_id,email,name,password_hash,role) SELECT 902,id,'migration@test','Migration','hash','admin' FROM companies LIMIT 1;
 INSERT INTO customers(id,company_id,display_name) SELECT 902,id,'Historical address owner' FROM companies LIMIT 1;
 INSERT INTO locations(id,company_id,object_type,object_id,title,address_1,latitude,longitude,geocoded_at,geocode_source) SELECT 902,id,'customer',902,'Historical location','Original address',40.5,-70.5,'2020-01-01','old provider' FROM companies LIMIT 1;
 INSERT INTO estimates(id,company_id,customer_id,title) SELECT 902,id,902,'Historical estimate' FROM companies LIMIT 1;
 INSERT INTO document_deliveries(company_id,document_type,document_id,estimate_id,actor_id,idempotency_key,request_fingerprint,recipients_to,subject,text_body,pdf_data,pdf_filename,message_id,lifetime_attempt_count)
 SELECT id,'estimate',902,902,902,'00000000-0000-0000-0000-000000000902',decode(repeat('00',32),'hex'),ARRAY['original@example.com'],'Original subject','Original body',decode('00ff','hex'),'original.pdf','<original@message>',3 FROM companies LIMIT 1;`)
	if err != nil {
		t.Fatal(err)
	}
	settingsColumns := legacyColumnProjection(t, ctx, db, "company_settings")
	locationColumns := legacyColumnProjection(t, ctx, db, "locations")
	deliveryColumns := legacyColumnProjection(t, ctx, db, "document_deliveries")
	snapshot := func() (string, string, string) {
		t.Helper()
		var settings, location, delivery string
		if err := db.Pool.QueryRow(ctx, `SELECT to_jsonb(s)::text FROM (SELECT `+settingsColumns+` FROM company_settings LIMIT 1) s`).Scan(&settings); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(ctx, `SELECT to_jsonb(l)::text FROM (SELECT `+locationColumns+` FROM locations WHERE id=902) l`).Scan(&location); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(ctx, `SELECT to_jsonb(d)::text FROM (SELECT `+deliveryColumns+` FROM document_deliveries WHERE document_id=902) d`).Scan(&delivery); err != nil {
			t.Fatal(err)
		}
		return settings, location, delivery
	}
	s, l, d := snapshot()
	warnings, err := db.IntegrationPreflight(ctx)
	if err != nil || len(warnings) == 0 {
		t.Fatalf("legacy preflight did not require review: %v %v", warnings, err)
	}
	if strings.Contains(strings.Join(warnings, " "), "legacy-format") {
		t.Fatal("preflight disclosed credential")
	}
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	s2, l2, d2 := snapshot()
	if s != s2 || l != l2 || d != d2 {
		t.Fatal("upgrade rewrote recorded integration facts")
	}
	var unknown bool
	if err := db.Pool.QueryRow(ctx, `SELECT sender_address IS NULL FROM document_deliveries WHERE document_id=902`).Scan(&unknown); err != nil || !unknown {
		t.Fatalf("invented historical sender: %v", err)
	}
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	s2, l2, d2 = snapshot()
	if s != s2 || l != l2 || d != d2 {
		t.Fatal("restart rewrote recorded integration facts")
	}
	t.Setenv("FREEFSM_CREDENTIAL_KEYS", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	encrypted, err := credentials.Seal("canary")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE company_settings SET smtp_password=$1`, encrypted); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FREEFSM_CREDENTIAL_KEYS", "")
	if _, err := db.IntegrationPreflight(ctx); err == nil {
		t.Fatal("missing required key passed startup preflight")
	}
}
