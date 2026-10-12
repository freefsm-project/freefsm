package backup

import (
	"context"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/credentials"
)

func TestRestorePreflightRequiresOriginalProviderKeys(t *testing.T) {
	m, db := preflightFixture(t)
	ctx := context.Background()
	t.Setenv("FREEFSM_CREDENTIAL_KEYS", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	encrypted, err := credentials.Seal("restore-secret-canary")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE company_settings SET smtp_password=$1`, encrypted); err != nil {
		t.Fatal(err)
	}
	op, err := m.StartBackup(1, "source", "long archive password")
	if err != nil {
		t.Fatal(err)
	}
	op = awaitOperation(t, m, 1, op.ID)
	if op.Phase != "complete" {
		t.Fatalf("backup: %+v", op)
	}
	reader, _, _, err := m.OpenDownload(1, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	t.Setenv("FREEFSM_CREDENTIAL_KEYS", "")
	staged, err := m.StageUpload(ctx, 1, reader, "long archive password")
	if err != nil {
		t.Fatal(err)
	}
	staged = awaitOperation(t, m, 1, staged.ID)
	if staged.Phase != "failed" || !strings.Contains(staged.Error, "operator") {
		t.Fatalf("restore accepted unavailable provider key: %+v", staged)
	}
	var actual string
	if err := db.Pool.QueryRow(ctx, `SELECT smtp_password FROM company_settings LIMIT 1`).Scan(&actual); err != nil || actual != encrypted {
		t.Fatalf("preflight mutated stored credential: %v", err)
	}
}
