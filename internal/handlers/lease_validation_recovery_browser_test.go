package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestLeaseAgreementCorrectionAndUncertainTransportBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("BROWSER_TESTS=1 required")
	}
	f := newLeaseRecoveryFixture(t, true)
	lost := newOperationKey()
	_, err := f.pool.Exec(f.ctx, fmt.Sprintf(`CREATE FUNCTION lose_lease_save_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.operation_id='%s'::uuid AND NEW.completed_at IS NOT NULL THEN RAISE EXCEPTION 'injected completion publication failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER lose_lease_save_completion BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION lose_lease_save_completion()`, lost))
	historyCheck(t, err)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(f.web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/lease-validation-recovery.cjs", server.URL, f.cookie.Value, fmt.Sprint(f.id), fmt.Sprint(f.oldFile), fmt.Sprint(f.newFile), fmt.Sprint(f.field), lost).CombinedOutput()
	t.Logf("persisted correction/transport: %s", output)
	historyCheck(t, err)
	var file, revision, changes int64
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT agreement_file_id,details_revision FROM leases WHERE id=$1`, f.id).Scan(&file, &revision))
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='lease' AND target_id=$1 AND action='lease.details.change'`, f.id).Scan(&changes))
	if file != f.newFile || revision != 4 || changes != 4 {
		t.Fatalf("expected setup, two corrections and one uncertain committed effect; file=%d revision=%d changes=%d", file, revision, changes)
	}
	if receipt := f.receipt(t, lost); receipt.Disposition != "unknown" || receipt.RetrySafe || receipt.CommittedSteps == 0 {
		t.Fatalf("transport uncertainty misclassified: %+v", receipt)
	}
}
