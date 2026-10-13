package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestAssetIdentityRestartHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Restart equipment").SaveX(ctx)
	fieldPost(t, router, cookie, "/assets", url.Values{"name": {"Before process restart"}, "number": {"RESTART-034"}, "ownership_class": {"company_owned"}, "asset_type_id": {fmt.Sprint(typ.ID)}, "notes": {"Persisted service instructions"}}, 303)
	a := client.Asset.Query().OnlyX(ctx)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"edit", "read"} {
		command := exec.Command(binary, "-test.run=^TestAssetIdentityRestartProbe$", "-test.v")
		command.Env = append(os.Environ(), "FREEFSM_ASSET_PROBE_DSN="+pool.Config().ConnString(), "FREEFSM_ASSET_PROBE_COOKIE="+cookie.Value, "FREEFSM_ASSET_PROBE_PATH="+fmt.Sprintf("/assets/%d", a.ID), "FREEFSM_ASSET_PROBE_PHASE="+phase)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("restart %s: %v\n%s", phase, err, output)
		}
	}
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d", a.ID), 200), "After process restart")
}

func TestAssetIdentityRestartProbe(t *testing.T) {
	dsn := os.Getenv("FREEFSM_ASSET_PROBE_DSN")
	if dsn == "" {
		t.Skip("subprocess helper")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, raw)))
	defer client.Close()
	router := New(pool, client, services.NewSessionService(pool), &config.Config{UploadDir: t.TempDir()})
	cookie := &http.Cookie{Name: "session", Value: os.Getenv("FREEFSM_ASSET_PROBE_COOKIE")}
	path := os.Getenv("FREEFSM_ASSET_PROBE_PATH")
	if os.Getenv("FREEFSM_ASSET_PROBE_PHASE") == "edit" {
		fieldPost(t, router, cookie, path, url.Values{"name": {"After process restart"}, "notes": {"Persisted service instructions"}}, 303)
	}
	body := requestBody(t, router, cookie, "GET", path, 200)
	for _, text := range []string{"RESTART-034", "Company-owned", "Unplaced", "After process restart", "Persisted service instructions"} {
		assertContains(t, body, text)
	}
}
