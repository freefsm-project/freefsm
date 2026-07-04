package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MartialM1nd/freefsm/internal/config"
	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/enttest"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestHTTPAuthorizationBoundaries(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	pool.Close()

	ctx := context.Background()
	sessions := services.NewSessionService(pool)
	router := New(pool, client, sessions, &config.Config{UploadDir: t.TempDir(), MaxUploadSize: 1024 * 1024})

	tech := client.User.Create().SetEmail("tech@example.test").SetPasswordHash("hash").SetName("Tech").SetRole("tech").SaveX(ctx)
	admin := client.User.Create().SetEmail("admin@example.test").SetPasswordHash("hash").SetName("Admin").SetRole("admin").SaveX(ctx)
	dispatcher := client.User.Create().SetEmail("dispatcher@example.test").SetPasswordHash("hash").SetName("Dispatcher").SetRole("dispatcher").SaveX(ctx)
	customer := client.Customer.Create().SetDisplayName("Route Customer").SaveX(ctx)
	assignedJob := client.Job.Create().SetCustomerID(customer.ID).SetJobType("Assigned Route Job").SetBillingType("hourly").SetLineItems(`[{"title":"Billable","quantity":1,"unit_price":100}]`).SaveX(ctx)
	archivedJob := client.Job.Create().SetCustomerID(customer.ID).SetJobType("Archived Route Job").SetDeletedAt(time.Now()).SaveX(ctx)
	unassignedJob := client.Job.Create().SetCustomerID(customer.ID).SetJobType("Unassigned Route Job").SaveX(ctx)
	workflow := client.StatusWorkflow.Create().SetObjectType("job").SetName("Job Workflow").SaveX(ctx)
	status := client.Status.Create().SetWorkflowID(workflow.ID).SetName("Scheduled").SetColor("#336699").SetSortOrder(1).SaveX(ctx)
	estimate := client.Estimate.Create().SetCustomerID(customer.ID).SetTitle("Protected Estimate").SaveX(ctx)
	invoice := client.Invoice.Create().SetCustomerID(customer.ID).SetTitle("Protected Invoice").SaveX(ctx)
	contact := client.CustomerContact.Create().SetCustomerID(customer.ID).SetFirstName("Protected").SetLastName("Contact").SaveX(ctx)
	location := client.Location.Create().SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Protected Location").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(assignedJob.ID).SetUserID(tech.ID).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(archivedJob.ID).SetUserID(tech.ID).SaveX(ctx)

	techCookie := sessionCookie(t, ctx, sessions, tech.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin.ID)
	dispatcherCookie := sessionCookie(t, ctx, sessions, dispatcher.ID)

	t.Run("tech route boundaries", func(t *testing.T) {
		expectStatus(t, router, techCookie, http.MethodGet, "/customers", http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, "/assets", http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, "/projects", http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, "/invoices", http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, "/items", http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, "/jobs/activity", http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/jobs/%d", assignedJob.ID), http.StatusOK)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/jobs/%d", unassignedJob.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/jobs/%d", archivedJob.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/jobs/%d/comments", assignedJob.ID), http.StatusOK)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/jobs/%d/comments", archivedJob.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/jobs/%d/activity", assignedJob.ID), http.StatusOK)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/jobs/%d/activity", archivedJob.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/invoices/%d", invoice.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/invoices/%d/pdf", invoice.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/invoices/%d/pdf/preview", invoice.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/estimates/%d", estimate.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/estimates/%d/pdf", estimate.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/estimates/%d/pdf/preview", estimate.ID), http.StatusForbidden)
	})

	t.Run("dispatcher route boundaries", func(t *testing.T) {
		expectStatus(t, router, dispatcherCookie, http.MethodGet, "/customers", http.StatusOK)
		expectStatus(t, router, dispatcherCookie, http.MethodGet, "/invoices", http.StatusOK)
		expectStatus(t, router, dispatcherCookie, http.MethodGet, fmt.Sprintf("/invoices/%d", invoice.ID), http.StatusOK)
		expectStatus(t, router, dispatcherCookie, http.MethodGet, fmt.Sprintf("/estimates/%d", estimate.ID), http.StatusOK)
		expectStatus(t, router, dispatcherCookie, http.MethodGet, fmt.Sprintf("/jobs/%d", assignedJob.ID), http.StatusOK)
		expectStatus(t, router, dispatcherCookie, http.MethodGet, fmt.Sprintf("/jobs/%d", archivedJob.ID), http.StatusOK)
	})

	t.Run("tech cannot mutate assigned jobs", func(t *testing.T) {
		expectStatus(t, router, techCookie, http.MethodPost, fmt.Sprintf("/jobs/%d/status?status_id=%d", assignedJob.ID, status.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodPost, fmt.Sprintf("/jobs/%d/create-next-occurrence", assignedJob.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodPost, fmt.Sprintf("/jobs/%d/cancel-next-occurrence", assignedJob.ID), http.StatusForbidden)
	})

	t.Run("tech cannot mutate customer subresources", func(t *testing.T) {
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/customers/%d/contacts/%d/edit", customer.ID, contact.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodPost, fmt.Sprintf("/customers/%d/contacts/%d", customer.ID, contact.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodPost, fmt.Sprintf("/customers/%d/contacts/%d/delete", customer.ID, contact.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodGet, fmt.Sprintf("/customers/%d/locations/%d/edit", customer.ID, location.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodPost, fmt.Sprintf("/customers/%d/locations/%d", customer.ID, location.ID), http.StatusForbidden)
		expectStatus(t, router, techCookie, http.MethodPost, fmt.Sprintf("/customers/%d/locations/%d/delete", customer.ID, location.ID), http.StatusForbidden)
	})

	t.Run("archived job pages are read-only", func(t *testing.T) {
		dispatcherBody := requestBody(t, router, dispatcherCookie, http.MethodGet, fmt.Sprintf("/jobs/%d", archivedJob.ID), http.StatusOK)
		assertContains(t, dispatcherBody, "Archived Route Job")
		assertContains(t, dispatcherBody, "Archived on")
		assertNotContains(t, dispatcherBody, "Edit")
		assertNotContains(t, dispatcherBody, "Delete")
		assertNotContains(t, dispatcherBody, "Create Invoice")
		assertNotContains(t, dispatcherBody, "Restore")

		adminBody := requestBody(t, router, adminCookie, http.MethodGet, fmt.Sprintf("/jobs/%d", archivedJob.ID), http.StatusOK)
		assertContains(t, adminBody, "Restore")
		assertNotContains(t, adminBody, "Create Invoice")

		expectStatus(t, router, dispatcherCookie, http.MethodGet, fmt.Sprintf("/jobs/%d/edit", archivedJob.ID), http.StatusForbidden)
		expectStatus(t, router, dispatcherCookie, http.MethodPost, fmt.Sprintf("/jobs/%d/create-invoice", archivedJob.ID), http.StatusForbidden)
		expectStatus(t, router, dispatcherCookie, http.MethodPost, fmt.Sprintf("/jobs/%d/comments", archivedJob.ID), http.StatusForbidden)
	})

	t.Run("tech page hides commercial data", func(t *testing.T) {
		body := requestBody(t, router, techCookie, http.MethodGet, fmt.Sprintf("/jobs/%d", assignedJob.ID), http.StatusOK)
		assertContains(t, body, "Assigned Route Job")
		assertNotContains(t, body, "Billing Type")
		assertNotContains(t, body, "Line Items")
		assertNotContains(t, body, "Create Invoice")
		assertNotContains(t, body, "Delete")
	})

	t.Run("tech dashboard hides global widgets", func(t *testing.T) {
		body := requestBody(t, router, techCookie, http.MethodGet, "/", http.StatusOK)
		assertContains(t, body, "Assigned Work")
		assertNotContains(t, body, "View Customers")
		assertNotContains(t, body, "View Invoices")
		assertNotContains(t, body, "Recent Activity")
	})

	t.Run("tech search only shows assigned readable work", func(t *testing.T) {
		body := requestBody(t, router, techCookie, http.MethodGet, "/search?q=Route", http.StatusOK)
		assertContains(t, body, "Assigned Route Job")
		assertNotContains(t, body, "Unassigned Route Job")
		assertNotContains(t, body, "Archived Route Job")
		assertNotContains(t, body, "Invoices")
		assertNotContains(t, body, "Estimates")
	})
}

func openHandlerTestDB(t *testing.T) (*ent.Client, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run handler authorization integration tests")
	}

	schemaName := fmt.Sprintf("freefsm_handler_test_%d", time.Now().UnixNano())
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE SCHEMA ` + schemaName); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA ` + schemaName + ` CASCADE`)
	})

	schemaDSN, err := dsnWithSearchPath(dsn, schemaName)
	if err != nil {
		t.Fatalf("set test search_path: %v", err)
	}
	schemaDB, err := sql.Open("pgx", schemaDSN)
	if err != nil {
		t.Fatalf("open schema database: %v", err)
	}
	t.Cleanup(func() { _ = schemaDB.Close() })

	client := enttest.NewClient(t, enttest.WithOptions(ent.Driver(entsql.OpenDB(dialect.Postgres, schemaDB))))
	if _, err := schemaDB.Exec(`CREATE TABLE sessions (
		id BIGSERIAL PRIMARY KEY,
		token_hash TEXT NOT NULL UNIQUE,
		user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		t.Fatalf("create sessions table: %v", err)
	}

	pool, err := pgxpool.New(context.Background(), schemaDSN)
	if err != nil {
		t.Fatalf("open test pool: %v", err)
	}
	return client, pool
}

func dsnWithSearchPath(dsn, schemaName string) (string, error) {
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		q := u.Query()
		q.Set("search_path", schemaName)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	return strings.TrimSpace(dsn) + " search_path=" + schemaName, nil
}

func sessionCookie(t *testing.T, ctx context.Context, sessions *services.SessionService, userID int64) *http.Cookie {
	t.Helper()
	token, err := sessions.Create(ctx, userID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: "session", Value: token, Path: "/"}
}

func expectStatus(t *testing.T, router http.Handler, cookie *http.Cookie, method, path string, want int) {
	t.Helper()
	_ = requestBody(t, router, cookie, method, path, want)
}

func requestBody(t *testing.T, router http.Handler, cookie *http.Cookie, method, path string, want int) string {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("%s %s status = %d, want %d; body: %s", method, path, rr.Code, want, rr.Body.String())
	}
	return rr.Body.String()
}

func assertContains(t *testing.T, body, needle string) {
	t.Helper()
	if !strings.Contains(body, needle) {
		t.Fatalf("response does not contain %q", needle)
	}
}

func assertNotContains(t *testing.T, body, needle string) {
	t.Helper()
	if strings.Contains(body, needle) {
		t.Fatalf("response unexpectedly contains %q", needle)
	}
}
