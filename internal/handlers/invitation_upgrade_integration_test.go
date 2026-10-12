package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func onboardingMigrationsThrough(t *testing.T, through string) fs.FS {
	t.Helper()
	result := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() <= through {
			b, err := fs.ReadFile(database.MigrationFS(), entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			result[entry.Name()] = &fstest.MapFile{Data: b}
		}
	}
	return result
}

func reviewOnboardingFixtureWorkflows(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	review, err := (&database.DB{Pool: pool}).WorkflowPreflight(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Statuses []struct {
			ID         int64 `json:"id"`
			WorkflowID int64 `json:"workflow_id"`
		} `json:"statuses"`
		Workflows []struct {
			ID   int64  `json:"id"`
			Type string `json:"object_type"`
		} `json:"workflows"`
	}
	if err := json.Unmarshal(review.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	types := map[int64]string{}
	for _, w := range evidence.Workflows {
		types[w.ID] = w.Type
	}
	for _, s := range evidence.Statuses {
		// Explicit review of empty workflow fixtures, not inference from labels.
		category := map[string]string{"job": "job:pending", "project": "project:pending", "estimate": "estimate:estimate", "invoice": "invoice:invoiced"}[types[s.WorkflowID]]
		if category == "" {
			t.Fatal("unknown fixture workflow")
		}
		review.Mappings[fmt.Sprint(s.ID)] = category
	}
	b, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "workflow-review.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FREEFSM_WORKFLOW_MAPPING_FILE", path)
}

func TestInvitationPre040UpgradePreservesRecordedEvidenceHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixtureMigrations(t, onboardingMigrationsThrough(t, "039_user_schedule_preferences.up.sql"))
	ctx := context.Background()
	ids := map[string]int64{}
	for _, name := range []string{"unknown", "replaced", "expired", "completed-disabled", "active"} {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,name,email,password_hash,role,is_active,welcome_email_sent_at,created_at) SELECT $1,$2,$2||'@legacy21.test',password_hash,'tech',$3,'2024-01-01T00:30:00Z','2024-01-01T00:00:00Z' FROM users WHERE id=$4 RETURNING id`, company, name, name == "active", admin).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	unknownToken := "legacy-unknown-purpose-21"
	sum := sha256.Sum256([]byte(unknownToken))
	unknownHash := hex.EncodeToString(sum[:])
	var resetID int64
	if err := pool.QueryRow(ctx, `INSERT INTO password_reset_tokens(company_id,user_id,token_hash,expires_at,created_at) VALUES($1,$2,$3,'2030-01-01T00:00:00Z','2024-01-01T00:00:00Z') RETURNING id`, company, ids["unknown"], unknownHash).Scan(&resetID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,created_at) VALUES($1,$2,'logged_in','user',$2,'2024-01-01T02:00:00Z'),($1,$2,'invite_accepted','user',$2,'2024-01-01T01:00:00Z')`, company, ids["completed-disabled"]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(user_id,token_hash,created_at,expires_at) VALUES($1,'recorded-session21','2024-01-01T02:00:00Z','2024-01-02T02:00:00Z')`, ids["completed-disabled"]); err != nil {
		t.Fatal(err)
	}
	reviewOnboardingFixtureWorkflows(t, pool)
	if err := (&database.DB{Pool: pool}).Migrate(ctx, onboardingMigrationsThrough(t, "047_file_ownership.up.sql")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"replaced", "expired", "completed-disabled"} {
		var consumed any
		if name != "expired" {
			consumed = time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO invitation_tokens(user_id,token_hash,created_at,expires_at,consumed_at) VALUES($1,$2,'2024-01-01T00:00:00Z','2024-01-03T00:00:00Z',$3)`, ids[name], "recorded-invitation-"+name, consumed); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	// Cutover is explicitly reviewed; legacy labels never grant these users access.
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	empty, err := auth.SaveRole(ctx, actor, 0, "Reviewed empty onboarding Role", nil)
	if err != nil {
		t.Fatal(err)
	}
	review, err := auth.Review(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range review.Users {
		current, err := auth.Review(ctx, actor)
		if err != nil {
			t.Fatal(err)
		}
		role := empty
		if u.ID == admin {
			role = 1
		}
		if err := auth.ApproveUser(ctx, actor, u.ID, role, current.Snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if err := auth.Cutover(ctx, actor); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	adminHash := client.User.GetX(ctx, admin).PasswordHash
	for name, id := range ids {
		u := client.User.GetX(ctx, id)
		if u.ID != id || u.PasswordHash != adminHash || u.WelcomeEmailSentAt == nil || u.Name != name || u.IsActive != (name == "active") {
			t.Fatalf("upgrade changed original identity/credential facts for %s", name)
		}
		body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/users/%d", id), 200)
		if name == "completed-disabled" || name == "active" {
			assertNotContains(t, body, "Resend Welcome</button>")
		} else {
			assertContains(t, body, "Resend Welcome</button>")
		}
		if name == "unknown" || name == "replaced" {
			assertContains(t, body, "Legacy onboarding needs review")
		}
	}
	var preserved bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM password_reset_tokens WHERE id=$1 AND user_id=$2 AND token_hash=$3 AND created_at='2024-01-01T00:00:00Z')`, resetID, ids["unknown"], unknownHash).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("legacy unknown token relationship changed: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invitation_tokens WHERE user_id=$1 AND token_hash='recorded-invitation-replaced' AND consumed_at='2024-01-01T01:00:00Z' AND expires_at='2024-01-03T00:00:00Z')`, ids["replaced"]).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("replacement evidence changed: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE user_id=$1 AND token_hash='recorded-session21')`, ids["completed-disabled"]).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("recorded session relationship changed: %v", err)
	}
	if w := onboardingPost(router, nil, "/accept-invite", url.Values{"token": {unknownToken}, "password": {"ChosenPassword1!"}, "confirm_password": {"ChosenPassword1!"}}); w.Code != 400 {
		t.Fatal("legacy reset-table token repurposed as invitation")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='sender@example.test'`, listener.Addr().(*net.TCPAddr).Port); err != nil {
		t.Fatal(err)
	}
	mail := make(chan []byte, 3)
	go func() {
		for range 3 {
			b, _ := receiveTestSMTP(listener)
			mail <- b
		}
	}()
	for _, name := range []string{"unknown", "replaced", "expired"} {
		accountPost(t, router, cookie, fmt.Sprintf("/users/%d/resend-welcome", ids[name]), nil, 303)
		token := invitationTokenFromMail(t, <-mail)
		if w := onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {"ChosenPassword1!"}, "confirm_password": {"ChosenPassword1!"}}); w.Code != 303 {
			t.Fatalf("explicit resend failed for %s: %d %s", name, w.Code, w.Body.String())
		}
	}
}
