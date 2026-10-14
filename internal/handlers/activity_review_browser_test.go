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

	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestActivityRepairAssetPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldType := client.AssetType.Create().SetCompanyID(company).SetName("Original browser Type").SaveX(ctx)
	client.AssetType.Create().SetCompanyID(company).SetName("Revised browser Type").SaveX(ctx)
	oldStatus := client.AssetStatus.Create().SetCompanyID(company).SetName("Original browser Status").SaveX(ctx)
	client.AssetStatus.Create().SetCompanyID(company).SetName("Revised browser Status").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(oldType.ID).SetAssetStatusID(oldStatus.ID).SetName("Browser history Asset").SetWarrantyExpires(time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = exports.Product(pool).Run(ctx, nil, nil) }()
	defer func() { cancel(); <-done }()
	limited, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	output, err := exec.CommandContext(limited, "node", "testdata/browser/activity-repair.cjs", server.URL, cookie.Value, fmt.Sprint(a.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
