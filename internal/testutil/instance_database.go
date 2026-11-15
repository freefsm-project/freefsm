package testutil

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// InstanceDatabase owns a disposable public-schema instance for tests exercising
// the backup Manager. It retains the configured unprivileged application role.
func InstanceDatabase(t *testing.T) string {
	t.Helper()
	adminURL, appURL := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL"), os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("disposable application and backup admin URLs required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	app, err := pgx.ParseConfig(appURL)
	if err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	name := fmt.Sprintf("admission_%d", time.Now().UnixNano())
	ident := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+ident+" OWNER "+pgx.Identifier{app.User}.Sanitize()+" TEMPLATE template0 ENCODING 'UTF8'")
	if err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+ident); err != nil {
			t.Error(err)
		}
		admin.Close(ctx)
	})
	u, err := url.Parse(appURL)
	if err != nil || u.Scheme == "" {
		t.Fatal("instance fixture requires a PostgreSQL URL", err)
	}
	u.Path = "/" + name
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return u.String()
}
