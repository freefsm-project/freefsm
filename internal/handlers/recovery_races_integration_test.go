package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRecoveryCommitTimeInvariantsHTTP(t *testing.T) {
	for _, mutation := range []struct{ name, sql string }{
		{"policy", `UPDATE company_settings SET password_min_length=40`},
		{"disable", `UPDATE users SET is_active=false WHERE email='target-race@test'`},
		{"replace", `DELETE FROM password_reset_tokens WHERE purpose='password-recovery-v1'`},
		{"expire", `UPDATE password_reset_tokens SET expires_at=clock_timestamp()`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			pool, client, router, sessions, company, _ := permissionHTTPFixture(t)
			ctx := context.Background()
			u := client.User.Create().SetCompanyID(company).SetName("Race").SetEmail("target-race@test").SetPasswordHash("original").SetRole("tech").SaveX(ctx)
			resets := services.NewPasswordResetService(client, pool)
			token, err := resets.CreateToken(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var pid int
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
				t.Fatal(err)
			}
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- recoveryPost(router, token, "RecoveryAfter1!") }()
			waitCredentialBlock(t, pool, pid)
			if _, err = tx.Exec(ctx, mutation.sql); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if w := <-result; w.Code == 303 {
				t.Fatal("stale authority/invariant acknowledged")
			}
			if client.User.GetX(ctx, u.ID).PasswordHash != "original" {
				t.Fatal("stale action changed credential")
			}
			if _, err = sessions.Validate(ctx, cookie.Value); err != nil {
				t.Fatal("failed action revoked session")
			}
		})
	}
}

func waitCredentialBlock(t *testing.T, pool *pgxpool.Pool, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		if err := pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)) AND wait_event='advisory')`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("credential action never reached shared transaction lock")
}

func TestLegacyRecoveryUpgradePreservesIdentityAndQuarantines(t *testing.T) {
	ctx := context.Background()
	var before string
	const token = "unknown-pre-v1-link"
	hash := sha256.Sum256([]byte(token))
	pool, client, router, _, _, admin := permissionHTTPFixtureSeeded(t, func(pool *pgxpool.Pool) {
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(u)::text FROM users u WHERE email='admin@test'`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO password_reset_tokens(company_id,user_id,token_hash,expires_at) SELECT company_id,id,$1,clock_timestamp()+interval '1 hour' FROM users WHERE email='admin@test'`, hex.EncodeToString(hash[:])); err != nil {
			t.Fatal(err)
		}
	})
	var after string
	if err := pool.QueryRow(ctx, `SELECT (to_jsonb(u)-'credential_revision'-'audit_identity')::text FROM users u WHERE email='admin@test'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("upgrade rewrote user identity, credential or historical timestamps")
	}
	var evidenceIdentity string
	if err := pool.QueryRow(ctx, `SELECT audit_identity::text FROM users WHERE id=$1`, admin).Scan(&evidenceIdentity); err != nil || evidenceIdentity == "" {
		t.Fatalf("missing current evidence identity: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM password_recovery_review WHERE user_id=$1 AND review_reason LIKE '%Unknown legacy%'`, admin).Scan(&n); err != nil || n != 1 {
		t.Fatalf("review=%d %v", n, err)
	}
	if w := recoveryPost(router, token, "RecoveryAfter1!"); w.Code == 303 {
		t.Fatal("legacy unknown token became valid")
	}
	resets := services.NewPasswordResetService(client, pool)
	replacement, err := resets.CreateToken(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if w := recoveryPost(router, replacement, "RecoveryAfter1!"); w.Code != 303 {
		t.Fatalf("explicit replacement failed: %d", w.Code)
	}
	var sameIdentity bool
	if err := pool.QueryRow(ctx, `SELECT audit_identity::text=$2 FROM users WHERE id=$1`, admin, evidenceIdentity).Scan(&sameIdentity); err != nil || !sameIdentity {
		t.Fatalf("credential recovery changed evidence identity: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM password_reset_tokens WHERE token_hash=$1 AND purpose='legacy-unknown'`, hex.EncodeToString(hash[:])).Scan(&n); err != nil || n != 1 {
		t.Fatalf("legacy record not preserved: %d %v", n, err)
	}
}
