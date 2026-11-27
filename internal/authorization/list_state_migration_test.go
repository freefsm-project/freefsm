package authorization_test

import (
	"context"
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5"
)

func TestListStatePopulatedMigrationPreservesFacts(t *testing.T) {
	ctx := context.Background()
	pool := authorizationPool(t)
	db := &database.DB{Pool: pool}
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "126_" {
			continue
		}
		data, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err := db.Migrate(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role)
		SELECT 7201,id,'list-migration@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1;
		INSERT INTO customers(id,company_id,display_name,notes,custom_fields)
		SELECT 7202,id,'Recorded customer','Historical notes','[{"id":7203,"value":"Retained label"}]' FROM companies LIMIT 1;
		INSERT INTO customers(id,company_id,display_name,deleted_at)
		SELECT 7204,id,'Archived customer',clock_timestamp() FROM companies LIMIT 1;
		INSERT INTO custom_field_definitions(id,object_type,name,field_type,options)
		VALUES(7203,'customer','Recorded option','select','["Retained label","Other label"]');
		INSERT INTO items(name,sku,unit_price,unit_cost) VALUES('Recorded item','RECORDED-SKU',123.45,67.89);
		INSERT INTO jobs(id,company_id,customer_id,job_type) SELECT 7205,id,7202,'Recorded job' FROM companies LIMIT 1;
		INSERT INTO estimates(company_id,customer_id,job_id,title,conversion_hidden_at)
		SELECT id,7202,7205,'Hidden provenance',clock_timestamp() FROM companies LIMIT 1;`); err != nil {
		t.Fatal(err)
	}
	svc := authorization.New(pool)
	if _, err := svc.RegisterDraftLease(ctx, authorization.Actor{UserID: 7201}, 7202); err != nil {
		t.Fatal(err)
	}
	// Snapshot every existing application table, including grants, immutable
	// audit, relationships, options, numbering, and conversion-hidden records.
	// Compare ordered JSON rows rather than selected columns that could miss
	// accidental historical rewrites. Sequences are checked independently.
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() AND tablename <> 'schema_migrations' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string]string {
		t.Helper()
		result := map[string]string{}
		for _, table := range tables {
			var value string
			if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) v FROM `+pgx.Identifier{table}.Sanitize()+` t) x`).Scan(&value); err != nil {
				t.Fatal(err)
			}
			result[table] = value
		}
		var sequences string
		if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY sequencename),'[]'::jsonb)::text FROM pg_sequences s WHERE schemaname=current_schema()`).Scan(&sequences); err != nil {
			t.Fatal(err)
		}
		result["sequences"] = sequences
		return result
	}
	before := snapshot()
	for range 2 {
		if err := db.Migrate(ctx, database.MigrationFS()); err != nil {
			t.Fatal(err)
		}
		after := snapshot()
		for key, value := range before {
			if !reflect.DeepEqual(value, after[key]) {
				t.Errorf("migration rewrote %s", key)
			}
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM list_states`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("synthetic state backfilled: %d %v", count, err)
		}
	}
}
