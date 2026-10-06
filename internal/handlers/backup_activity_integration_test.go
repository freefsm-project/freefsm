package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestBackupActivityHTTPVisibilityIntegration(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	const companyID int64 = 42
	client.CompanySettings.Create().SetCompanyID(companyID).SetBusinessName("Destination").SaveX(ctx)
	sessions := services.NewSessionService(pool)
	cookies := map[string]*http.Cookie{}
	for _, role := range []string{"admin", "dispatcher", "tech"} {
		user := client.User.Create().SetCompanyID(companyID).SetName(role).SetEmail(role + "@example.test").SetPasswordHash("hash").SetRole(role).SetIsActive(true).SaveX(ctx)
		cookies[role] = sessionCookie(t, ctx, sessions, user.ID)
	}
	for i := 0; i < 11; i++ {
		err := services.WriteBackupActivity(ctx, pool.Config().ConnString(), backup.ActivityEvent{Key: fmt.Sprintf("visibility-%d", i), Action: "backup_created", OccurredAt: time.Now().UTC(), Actor: backup.ActivityActor{Name: "Historical backup administrator"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	client.ActivityLog.Create().SetCompanyID(companyID).SetAction("created").SetObjectType("customer").SetObjectID(1).SetMetadata(`{"actor_name":"Ordinary customer actor","entity_name":"Ordinary customer activity"}`).SaveX(ctx)
	control, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := backup.New(backup.Config{DSN: pool.Config().ConnString(), UploadDir: t.TempDir(), StateDir: control.StateDir(), Control: control, BuildKind: backup.BuildDevelopment})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown()
	backupRouter := NewBackupRouter(manager, control, services.NewUserService(client), services.NewCompanySettingsService(client), sessions, "", backupTestActivityHandler(client))
	globalRouter := New(pool, client, sessions, &config.Config{UploadDir: t.TempDir()})
	for _, surface := range []struct {
		router http.Handler
		path   string
	}{{backupRouter, "/settings/backup/activity"}, {globalRouter, "/activity?type=instance"}} {
		router, path := surface.router, surface.path
		body := requestBody(t, router, nil, http.MethodGet, path, http.StatusSeeOther)
		assertNotContains(t, body, "Historical backup administrator")
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("HX-Request", "true")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" || strings.Contains(w.Body.String(), "Historical backup administrator") {
			t.Fatalf("anonymous HTMX activity: %d %s", w.Code, w.Body)
		}
		for _, role := range []string{"dispatcher", "tech"} {
			body := requestBody(t, router, cookies[role], http.MethodGet, path, http.StatusForbidden)
			assertNotContains(t, body, "Historical backup administrator")
		}
		body = requestBody(t, router, cookies["admin"], http.MethodGet, path, http.StatusOK)
		assertContains(t, body, "Historical backup administrator")
		assertNotContains(t, body, "Ordinary customer activity")
	}
	body := requestBody(t, backupRouter, cookies["admin"], http.MethodGet, "/settings/backup/activity", http.StatusOK)
	assertContains(t, body, "Recent Activity")
	assertContains(t, body, "/activity?type=instance")
	if count := strings.Count(body, "Historical backup administrator"); count != 10 {
		t.Fatalf("recent panel returned %d entries, want 10", count)
	}
	body = requestBody(t, globalRouter, cookies["dispatcher"], http.MethodGet, "/activity", http.StatusOK)
	assertContains(t, body, "Ordinary customer activity")
	assertNotContains(t, body, "Historical backup administrator")
	body = requestBody(t, globalRouter, cookies["dispatcher"], http.MethodGet, "/activity?action=backup_created", http.StatusOK)
	assertNotContains(t, body, "Historical backup administrator")
	expectStatus(t, globalRouter, cookies["tech"], http.MethodGet, "/activity", http.StatusForbidden)

	// Prove the maintenance/status shell renders with both database clients
	// closed, while the activity boundary is rejected before authentication.
	pool.Close()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := control.KeepClosed(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/settings/backup", "/settings/backup/status-page"} {
		body := requestBody(t, backupRouter, nil, http.MethodGet, path, http.StatusOK)
		assertContains(t, body, "backup-progress")
		assertNotContains(t, body, `id="backup-activity"`)
		assertNotContains(t, body, "Historical backup administrator")
	}
	expectStatus(t, backupRouter, nil, http.MethodGet, "/settings/backup/activity", http.StatusServiceUnavailable)
}
