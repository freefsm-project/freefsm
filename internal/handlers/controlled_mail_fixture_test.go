package handlers

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

// controlledMailInstance owns a whole public-schema database and one admission
// owner. Use it for mail producers and workers; permissionHTTPFixture remains a
// cheap schema-only fixture for nonmail tests. Never attach a Manager to that
// schema-only fixture. Startup imports installation provenance before opening
// admission; it is not a restore-email enable shortcut. Every router and direct
// service below shares the same Control and connection-generation refresh.
type controlledMailInstance struct {
	pool           *pgxpool.Pool
	client         *ent.Client
	web, api       http.Handler
	sessions       *services.SessionService
	company, admin int64
	control        *instancecontrol.Control
	manager        *backup.Manager
	outbox         *delivery.Service
	email          *services.EmailService
}

func permissionHTTPControlledMailInstanceFixture(t *testing.T) *controlledMailInstance {
	t.Helper()
	f := &controlledMailInstance{}
	f.pool, f.client, _, f.sessions, f.company, f.admin = permissionHTTPInstanceFixture(t)
	f.start(t)
	return f
}

// start composes admission only after an owned public-schema fixture has reached
// the current schema. Upgrade tests can record and verify old facts first.
func (f *controlledMailInstance) start(t *testing.T) {
	t.Helper()
	var err error
	f.control, err = instancecontrol.New(filepath.Join(t.TempDir(), "state"))
	historyCheck(t, err)
	f.manager = admissionManager(t, f.pool, f.control)
	startup, err := f.manager.BeginStartup(context.Background())
	historyCheck(t, err)
	defer startup.Close()
	importFixtureEmailPolicy(t, f.pool, f.control, f.manager)
	historyCheck(t, startup.Finish(context.Background()))
	cfg := &config.Config{UploadDir: filepath.Join(filepath.Dir(f.control.StateDir()), "state-uploads"), MaxUploadSize: 25 << 20}
	f.web = f.control.Middleware(middleware.Company(services.NewCompanySettingsService(f.client))(New(f.pool, f.client, f.sessions, cfg, f.control)))
	f.outbox = delivery.New(f.pool, cfg.PublicURL)
	f.outbox.SetInstanceControl(f.control)
	f.outbox.SetBeforeWork(f.manager.RefreshConnections)
	f.api = f.control.Middleware(apiv1.NewRouterWithDelivery(f.pool, f.client, f.sessions, config.APIConfig{}, nil, f.outbox))
	f.email = services.NewControlledEmailService(services.NewCompanySettingsService(f.client), f.control)
}
