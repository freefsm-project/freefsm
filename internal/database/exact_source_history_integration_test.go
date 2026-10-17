package database

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// Source objects, rather than a numeric cutoff of today's embedded SQL, are
// the fixture. The subprocess overlay also runs the installed initializer:
// notably its fresh-install detection predates schema_initialization.
const exactHistoryTransfer = "9b0ad4d04051b4286f757627f6d0674b2585726e"
const exactHistoryPlanning = "0bede469d802e4966b39f3db27fc2e830b569508"

func historyGit(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = "../.."
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
	return b
}

func exactHistorySQL(t *testing.T, pin string) fstest.MapFS {
	t.Helper()
	const dir = "internal/database/migrations/"
	files := strings.Fields(string(historyGit(t, "ls-tree", "-r", "--name-only", pin, "--", dir)))
	result := fstest.MapFS{}
	for _, path := range files {
		if !strings.HasSuffix(path, ".up.sql") {
			continue
		}
		b := historyGit(t, "show", pin+":"+path)
		result[strings.TrimPrefix(path, dir)] = &fstest.MapFile{Data: b}
	}
	if len(result) == 0 {
		t.Fatal("empty historical migration tree")
	}
	t.Logf("source=%s migration-tree=%s", strings.TrimSpace(string(historyGit(t, "rev-parse", pin))), strings.TrimSpace(string(historyGit(t, "rev-parse", pin+":internal/database/migrations"))))
	return result
}

func historyLedger(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(), `SELECT name FROM schema_migrations ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

// Only selected by the parent with the exact source database.go overlay.
func TestExactSourceHistoryInitializer(t *testing.T) {
	pin := os.Getenv("FREEFSM_EXACT_HISTORY_PIN")
	if pin == "" {
		t.Skip("subprocess fixture; exercised by TestExactSourceInstalledHistories")
	}
	ctx := context.Background()
	db, err := Connect(ctx, os.Getenv("FREEFSM_EXACT_HISTORY_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := exactHistorySQL(t, pin)
	beforeClock := fstest.MapFS{}
	for name, file := range source {
		if name < "053_" {
			beforeClock[name] = file
		}
	}
	if err := db.Migrate(ctx, beforeClock); err != nil {
		t.Fatal(err)
	}
	a := seedFileOwnershipTenant(t, ctx, db, "exact-history")
	var job int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Exact historical Job') RETURNING id`, a.company, a.customer), &job)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO time_entries(user_id,job_id,clock_in) VALUES($1,$2,'2020-01-02T03:00:00Z'),($1,$2,'2020-01-02T04:00:00Z')`, a.user, job); err != nil {
		t.Fatal(err)
	}
	insertMigrationFile(t, ctx, db, a.company, a.user, "customer", a.customer)
	filePath := os.Getenv("FREEFSM_EXACT_HISTORY_FILE")
	if err := os.WriteFile(filePath, []byte("hello world"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE files SET file_size=11,file_path=$1`, filePath); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	// These are explicit historical source facts, not attribution reconstructed
	// during upgrade. The old source has no operational074 projection yet.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO authorization_audit(actor_id,scope,target_type,target_id,channel,action,detail,recorded_at,occurred_at) VALUES($1,'A','job',$2,'api','job.fields.edit','{"before":{"serial":"old"},"after":{"serial":"recorded"}}','2020-01-03T04:05:06Z','2020-01-03T03:04:05Z')`, a.user, job); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at) VALUES($3,$1,'created','job',$2,'{"recorded":"original activity"}','2020-01-02T03:00:00Z')`, a.user, job, a.company); err != nil {
		t.Fatal(err)
	}
	const id = "85000000-0000-4000-8000-000000000001"
	if _, err := db.Pool.Exec(ctx, `INSERT INTO file_transfers(id,user_id,session_hash,publication_id,object_type,object_id,name,size,sha256,transferred) VALUES($1,$2,'recorded-client-key','85000000-0000-4000-8000-000000000002','customer',$3,'retained.txt',11,'b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9',11)`, id, a.user, a.customer); err != nil {
		t.Fatal(err)
	}
	if pin == exactHistoryTransfer {
		_, err = db.Pool.Exec(ctx, `INSERT INTO file_transfer_chunks(transfer_id,offset_bytes,data,user_id,session_hash) VALUES($1,0,$2,$3,'recorded-client-key')`, id, []byte("hello world"), a.user)
	} else {
		_, err = db.Pool.Exec(ctx, `INSERT INTO file_transfer_chunks(transfer_id,offset_bytes,data) VALUES($1,0,$2)`, id, []byte("hello world"))
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestExactSourceInstalledHistories(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	for _, pin := range []string{exactHistoryTransfer, exactHistoryPlanning} {
		t.Run(pin, func(t *testing.T) {
			ctx := context.Background()
			admin, err := Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			schema := fmt.Sprintf("exact_history_%d", time.Now().UnixNano())
			if _, err := admin.Pool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
				admin.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = admin.Pool.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
			scoped := migrationSearchPath(t, dsn, schema)
			db, err := Connect(ctx, scoped)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			source := exactHistorySQL(t, pin)
			// Execute the historical initializer AND its pinned dependencies. Current
			// validation may evolve; it must never silently initialize old evidence.
			dir := t.TempDir()
			replacements := map[string]string{}
			for _, path := range []string{"internal/database/workflow_preflight.go", "internal/database/asset_identity_preflight.go", "internal/database/vocabulary_preflight.go", "internal/customfields/preflight.go", "internal/customfields/values.go", "internal/customfields/catalog.go", "internal/customfields/references.go", "internal/customfields/cycles.go"} {
				old := historyGit(t, "show", pin+":"+path)
				current, err := filepath.Abs(filepath.Join("../..", path))
				if err != nil {
					t.Fatal(err)
				}
				replacement := filepath.Join(dir, "source", path)
				if err := os.MkdirAll(filepath.Dir(replacement), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(replacement, old, 0600); err != nil {
					t.Fatal(err)
				}
				replacements[current] = replacement
				t.Logf("historical initializer dependency %s sha256=%x", path, sha256.Sum256(old))
			}
			original := historyGit(t, "show", pin+":internal/database/database.go")
			t.Logf("database.go sha256=%x", sha256.Sum256(original))
			replacement := filepath.Join(dir, "database.go")
			if err := os.WriteFile(replacement, original, 0600); err != nil {
				t.Fatal(err)
			}
			current, err := filepath.Abs("database.go")
			if err != nil {
				t.Fatal(err)
			}
			replacements[current] = replacement
			overlay, _ := json.Marshal(map[string]any{"Replace": replacements})
			overlayPath := filepath.Join(dir, "overlay.json")
			if err := os.WriteFile(overlayPath, overlay, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "test", "-race", "-count=1", "-overlay", overlayPath, "-run", "^TestExactSourceHistoryInitializer$", "-v", ".")
			filePath := filepath.Join(dir, "original-file.txt")
			cmd.Env = append(os.Environ(), "FREEFSM_EXACT_HISTORY_PIN="+pin, "FREEFSM_EXACT_HISTORY_DSN="+scoped, "FREEFSM_EXACT_HISTORY_FILE="+filePath)
			output, err := cmd.CombinedOutput()
			t.Logf("source initializer: %s", output)
			if err != nil {
				t.Fatalf("source initializer: %v", err)
			}
			var expected []string
			for name := range source {
				expected = append(expected, strings.TrimSuffix(name, ".up.sql"))
			}
			sort.Strings(expected)
			if got := historyLedger(t, db); !reflect.DeepEqual(got, expected) {
				t.Fatalf("pre-upgrade ledger=%v want exact source %v", got, expected)
			}
			for _, name := range expected {
				if strings.HasPrefix(name, "074_operational") || name >= "076" {
					t.Fatalf("impossible source ledger: %s", name)
				}
			}
			peer := "075_job_title_whitespace"
			if pin == exactHistoryPlanning {
				peer = "075_file_transfer_scope"
			}
			for _, name := range expected {
				if name == peer {
					t.Fatalf("peer preinstalled: %s", peer)
				}
			}
			var inferred bool
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*)=2 AND count(*) FILTER(WHERE clock_in='2020-01-02T03:00:00Z' AND clock_out='2020-01-02T04:00:00Z')=1 AND count(*) FILTER(WHERE clock_out IS NULL)=1 FROM time_entries`), &inferred)
			if !inferred {
				t.Fatal("source053 did not produce its historical inferred clock-out")
			}
			if pin == exactHistoryPlanning {
				if _, err := db.Pool.Exec(ctx, `UPDATE jobs SET job_type=' '`); err == nil {
					t.Fatal("installed planning guard accepted blank title")
				}
			}
			snapshot := func() string {
				var v string
				mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT jsonb_build_object('transfers',(SELECT jsonb_agg(to_jsonb(t)) FROM file_transfers t),'chunks',(SELECT jsonb_agg(to_jsonb(c)-'user_id'-'session_hash') FROM file_transfer_chunks c),'files',(SELECT jsonb_agg(to_jsonb(f)) FROM files f),'audit',(SELECT jsonb_agg(to_jsonb(a)) FROM authorization_audit a),'activity',(SELECT jsonb_agg(to_jsonb(a)) FROM activity_logs a WHERE action='created'),'clock',(SELECT jsonb_agg(jsonb_build_array(id,user_id,job_id,clock_in,clock_out) ORDER BY id) FROM time_entries),'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g))::text`), &v)
				return v
			}
			before := snapshot()
			installedLedger := func() string {
				var value string
				mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(s) ORDER BY name)::text FROM schema_migrations s WHERE name=ANY($1)`, expected), &value)
				return value
			}
			beforeLedger := installedLedger()
			var firstLedger string
			for pass := 0; pass < 2; pass++ {
				if err := db.Migrate(ctx, MigrationFS()); err != nil {
					t.Fatal(err)
				}
				if got := snapshot(); got != before {
					t.Fatalf("upgrade rewrote source facts\nbefore=%s\nafter=%s", before, got)
				}
				if installedLedger() != beforeLedger {
					t.Fatal("upgrade rewrote installed migration evidence")
				}
				var backfill int
				mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs l JOIN authorization_audit a ON l.event_key='authorization-audit:'||a.id WHERE l.action='job_fields_changed' AND l.metadata->'before'='{"serial":"old"}' AND l.metadata->'after'='{"serial":"recorded"}' AND l.created_at='2020-01-03T04:05:06Z' AND l.actor_id=a.actor_id AND l.metadata->>'actor_name'=a.actor_name AND (l.metadata->>'source_occurred_at')::timestamptz='2020-01-03T03:04:05Z'`), &backfill)
				if backfill != 1 {
					t.Fatalf("pending operational074 backfill=%d", backfill)
				}
				var ledger string
				mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(s) ORDER BY name)::text FROM schema_migrations s`), &ledger)
				if pass == 0 {
					firstLedger = ledger
				} else if ledger != firstLedger {
					t.Fatal("second upgrade changed migration ledger")
				}
				var bytes string
				mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT convert_from(c.data,'UTF8') FROM file_transfer_chunks c JOIN file_transfers t ON (t.user_id,t.session_hash,t.id)=(c.user_id,c.session_hash,c.transfer_id)`), &bytes)
				if bytes != "hello world" {
					t.Fatal("scoped transfer bytes changed")
				}
				fileBytes, err := os.ReadFile(filePath)
				if err != nil || string(fileBytes) != "hello world" {
					t.Fatalf("original File bytes changed: %q %v", fileBytes, err)
				}
			}
			entries, err := fs.ReadDir(MigrationFS(), ".")
			if err != nil {
				t.Fatal(err)
			}
			var currentNames []string
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".up.sql") {
					currentNames = append(currentNames, strings.TrimSuffix(e.Name(), ".up.sql"))
				}
			}
			sort.Strings(currentNames)
			if !reflect.DeepEqual(historyLedger(t, db), currentNames) {
				t.Fatal("pending migration missing after upgrade")
			}
		})
	}
}
