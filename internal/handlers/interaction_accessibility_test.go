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

	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
)

// These are installed production pages under current authenticated authority,
// not copied HTML fixtures. Browser dependencies are opt-in development tools.
func TestSharedAccessibilityPersistedBrowser(t *testing.T) {
	if os.Getenv("ACCESSIBILITY_TESTS") != "1" {
		t.Skip("set ACCESSIBILITY_TESTS=1, NODE_PATH and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Measured accessibility customer").SaveX(context.Background())
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(time.Now().Add(-time.Hour)).SetClockOut(time.Now()).SetNotes("Measured time record").SaveX(context.Background())
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	control, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := backup.New(backup.Config{DSN: os.Getenv("FREEFSM_TEST_DATABASE_URL"), UploadDir: t.TempDir(), StateDir: control.StateDir(), BuildKind: backup.BuildDevelopment, Version: "dev", Commit: "ui-measurements", Control: control, ResetConnections: pool.Reset})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown()
	objects := objectref.NewEntDirectory(client)
	activity := NewActivityHandler(services.NewActivityService(client, objects), services.NewActivityResolver(client), services.NewPolicyService(client, objects), nil)
	backupRouter := NewBackupRouter(pool, manager, control, services.NewUserService(client), services.NewCompanySettingsService(client), sessions, "", activity)
	mux.Handle("/settings/backup", backupRouter)
	mux.Handle("/settings/backup/", backupRouter)
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/accessibility.cjs", server.URL, cookie.Value, fmt.Sprint(customer.ID), fmt.Sprint(entry.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
