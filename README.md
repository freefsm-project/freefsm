# FreeFSM - Free Field Service Manager

Self-hosted, open-source field service management for FreeBSD and Linux.
Single static Go binary, PostgreSQL backend, and no npm dependencies.

This README describes functionality released through `v0.7.0`.

## Features

- **Dashboard** — customizable widgets, KPI cards, work queues, time clock, quick actions, global search
- **Customers** — full CRUD with reusable locations, contacts, financial summary, receivables graph, related work, search, HTMX pagination
- **Assets** — equipment tracking with types, statuses, service history
- **Jobs** — work orders with configurable status workflow, inline status updates, scheduling, arrival windows, subtasks, job-linked clock in/out
- **Schedule** — list, calendar, dispatch, and map views with clickable job cards and drag/drop scheduling
- **Projects** - linked jobs, progress tracking, customer and location linkage, comments, and tags
- **Estimates and invoices** - line items, tax, PDFs, durable queued email delivery with immutable message snapshots and retry history
- **Estimate conversion** - atomic estimate-to-invoice conversion with preserved history and reversal when the generated invoice is active (not archived) and has no settlement blockers
- **Settlement** - immutable payment, allocation, customer-credit, credit-application, and refund records; corrections are explicit reversals
- **Status workflows** - configurable labels, colors, defaults, and ordering within fixed semantic categories for jobs, projects, estimates, and invoices
- **Items / Pricebook** — service and product catalog with SKU and pricing
- **Timesheets** — standalone or job-linked clock in/out, GPS coordinates, manual entry flag
- **Tags** — color-coded labels on customers, projects, jobs, assets, estimates, and invoices
- **Custom Fields** — user-defined fields for customers, projects, jobs, assets, estimates, and invoices
- **Comments** — notes on customers, projects, jobs, assets, estimates, and invoices
- **User Management** — roles, welcome emails, password policies, force password change
- **Company Settings** — branding, email config, timezone, invoice numbering, status workflows, map settings, document defaults, security policies
- **Whole-instance Backup and Restore** — administrator-only password-encrypted archives, reviewed instance replacement, crash recovery, session expiry, and persistent outbound-email disablement after restore
- **Dark Mode** — persistent theme toggle
- **Activity / Audit Log** — audited business actions with per-record and administrative activity views
- **File Attachments** — customer, job, estimate, invoice, and asset uploads; queued drag-and-drop batches of up to 10; PNG/JPEG/GIF, PDF, plain text, legacy/OpenXML Microsoft Office, ZIP, and JSON formats; image/PDF preview and disk storage
- **Soft-Delete / Archive** — business entities archived instead of hard-deleted; admin restore from show page banner
- **Dependency Protection** — prevents deletion of configuration items (tags, asset types, statuses) when referenced by other records
- **Mobile Sidebar** — responsive navigation
- **Auth** — setup token, bcrypt, HTTP-only session cookies, CSRF protection

## Tech Stack

| Layer | Choice |
|-------|--------|
| Language | Go |
| Router | chi |
| Database | PostgreSQL (JSONB, TIMESTAMPTZ) |
| ORM | ent (type-safe codegen) |
| Templates | Templ (compile-time safety) |
| Interactivity | HTMX 2 + Alpine.js |
| CSS | Pico CSS |
| Deploy | Single binary, systemd + rc.d |

## Quick Start

### Prerequisites

- Go 1.25.10+
- PostgreSQL 16+
- Matching-major `pg_dump` and `pg_restore` on the service's PATH for backup and restore
- `ent` CLI: `go install entgo.io/ent/cmd/ent@v0.14.6`
- `templ` CLI: `go install github.com/a-h/templ/cmd/templ@v0.3.1020`
- Ensure `$HOME/go/bin` is in `$PATH`

### Database

```sql
CREATE USER freefsm WITH PASSWORD 'changeme';
CREATE DATABASE freefsm OWNER freefsm;
GRANT ALL PRIVILEGES ON DATABASE freefsm TO freefsm;
```

If using Fedora or any system with ident/peer auth, edit `pg_hba.conf` and change
`local` and `127.0.0.1` entries from `ident`/`peer` to `md5`, then restart
PostgreSQL.

### Build & Run

```bash
git clone https://github.com/freefsm-project/freefsm.git
cd freefsm
cp .env.example .env
# Edit .env: replace the FREEFSM_DB_PASSWORD, FREEFSM_SESSION_SECRET,
# and FREEFSM_SETUP_TOKEN placeholders

make run
# http://localhost:3000
```

### First-Time Setup

1. Visit `http://localhost:3000` - you'll be redirected to `/setup`
2. Enter your `FREEFSM_SETUP_TOKEN` value (from `.env`)
3. Create an admin account (name, email, password)
4. Complete company setup at `/setup/company`
5. Continue to the dashboard

`FREEFSM_SETUP_TOKEN` is required at startup and is only accepted while no users exist.

### Demo Data

Populate the database with sample HVAC-themed data (customers, jobs, assets, invoices, etc.) for testing:

```bash
./dist/freefsm -seed
```

Seeding refuses to run and exits unsuccessfully if any customers already exist.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `FREEFSM_DB_HOST` | `localhost` | PostgreSQL host |
| `FREEFSM_DB_PORT` | `5432` | PostgreSQL port |
| `FREEFSM_DB_NAME` | `freefsm` | Database name |
| `FREEFSM_DB_USER` | `freefsm` | Database user |
| `FREEFSM_DB_PASSWORD` | *(empty)* | PostgreSQL password |
| `FREEFSM_DB_SSLMODE` | `disable` | PostgreSQL SSL mode |
| `FREEFSM_ADDR` | `:3000` | HTTP listen address |
| `FREEFSM_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `FREEFSM_LOG_FILE` | *(empty)* | Optional file path for application logs |
| `FREEFSM_SESSION_SECRET` | *(required)* | Required startup value currently reserved for session configuration |
| `FREEFSM_SETUP_TOKEN` | *(required)* | Initial admin registration token |
| `FREEFSM_PUBLIC_URL` | *(empty)* | Optional public origin; recommended for externally valid email links and tracking URLs |
| `FREEFSM_UPLOAD_DIR` | `/var/lib/freefsm/uploads` (Linux); `/var/db/freefsm/uploads` (FreeBSD) | File upload storage |
| `FREEFSM_STATE_DIR` | `state` sibling of the upload directory | Persistent recovery and instance-control storage; defaults to `/var/lib/freefsm/state` on Linux or `/var/db/freefsm/state` on FreeBSD; must be disjoint from uploads |
| `FREEFSM_MAX_UPLOAD_SIZE` | `26214400` (25 MB) | Maximum upload file size in bytes |
| `FREEFSM_TILE_URL` | `https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png` | Default map tile URL template |
| `FREEFSM_GEOCODER_URL` | *(empty)* | Optional geocoder base URL for map location lookup |

### In-App Settings

Administrators can manage runtime company settings from the Settings screens:

- Company profile, timezone, invoice prefix, next invoice number, and estimate prefix
- Job, project, estimate, and invoice statuses with custom labels, colors, defaults, and ordering inside fixed semantic categories
- SMTP settings, invoice/estimate email defaults, and automatic CC recipients
- PDF branding, logo, colors, footer text, payment terms, and line item description visibility
- Map tile and geocoder URLs for schedule map features
- Password policies

## JSON API (`/api/v1`)

`POST /session` accepts email, password, and an optional device name. Send the returned token as `Authorization: Bearer <token>` for every other endpoint. Mobile bearer sessions expire after 30 days of inactivity, and login is rate-limited to five attempts per minute.

| Method | Endpoint | Purpose |
|--------|----------|---------|
| `POST` / `DELETE` | `/session` | Create or revoke the current bearer session |
| `GET` | `/me` | Return the authenticated user |
| `GET` | `/jobs` | List visible jobs |
| `GET` | `/jobs/{id}` | Return one visible job |
| `POST` | `/jobs/{id}/status` | Apply a valid semantic status transition |
| `PATCH` | `/jobs/{id}/subtasks/{index}` | Set subtask completion |
| `GET` | `/time-entries/active` | Return the user's active entry, or `null` |
| `POST` | `/jobs/{id}/clock-in` | Clock in to a job, optionally with notes and coordinates |
| `POST` | `/time-entries/clock-out` | Clock out the user's active entry |

Admins and dispatchers can access company jobs and receive customer, project, location, asset, billing, and line-item fields. Tech and technician users can access only jobs assigned to them; those office/financial fields are omitted. Subtask updates and clock-in are rejected for closed jobs, and field users cannot change a closed job's status.

## Project Structure

```
freefsm/
├── cmd/freefsm/           # Entry point + static file embed
│   └── static/            # CSS, JS (Pico, HTMX, Alpine)
├── internal/
│   ├── api/v1/            # Bearer-authenticated mobile JSON API
│   ├── backup/            # Encrypted whole-instance archives and recovery
│   ├── config/            # Env loading + DSN builder
│   ├── conversion/        # Estimate/invoice conversion and reversal
│   ├── database/          # pgxpool connection + SQL migration runner
│   │   └── migrations/    # SQL migration files
│   ├── delivery/          # Durable document email outbox
│   ├── ent/
│   │   └── schema/        # ent schema definitions
│   ├── handlers/          # HTTP handlers (chi routes)
│   ├── instancecontrol/   # Maintenance, locking, and persistent email gate
│   ├── middleware/         # Auth, Flash, user context, CSRF
│   ├── objectref/          # Typed business-object references and capabilities
│   ├── settlement/         # Payments, credits, refunds, and reversals
│   ├── services/          # Business logic (ent queries)
│   ├── statusflow/         # Semantic status configuration and transitions
│   └── templates/         # Templ files (pages + partials)
├── deploy/
│   ├── freebsd/           # rc.d service script
│   ├── linux/             # systemd unit + config sample
│   └── README.md          # Detailed deployment guide
├── AGENTS.md              # Agent guidelines (this project)
├── Makefile               # build, install, fmt, lint, test
├── PLAN.md                # Historical planning document
└── go.mod
```

## Development

```bash
make build            # ent generate → templ generate → go build → dist/freefsm
make compile          # go build only → dist/freefsm
make run              # build + run
make generate         # ent generate + templ generate
make ent              # regenerate ent code
make templ            # regenerate templ code
make fmt              # go fmt ./...
make lint             # go vet ./...
make test             # go test -v -race ./...
make clean            # remove dist/
make checksum         # SHA256 of the binary
make install-linux    # install binary + systemd unit
make install-freebsd  # install binary + rc.d script
```

PostgreSQL integration tests use `FREEFSM_TEST_DATABASE_URL`; tests that require it are skipped when it is unset:

```sql
CREATE DATABASE freefsm_test OWNER freefsm;
GRANT ALL PRIVILEGES ON DATABASE freefsm_test TO freefsm;
```

```bash
FREEFSM_TEST_DATABASE_URL='postgres://freefsm:changeme@localhost/freefsm_test?sslmode=disable' make test
```

Run with a custom config file:

```bash
./dist/freefsm -config /usr/local/etc/freefsm.conf
```

### Adding a New Entity

1. Create a SQL migration in `internal/database/migrations/`
2. Define an ent schema in `internal/ent/schema/`
3. Run `ent generate ./internal/ent/schema`
4. Create a service in `internal/services/`
5. Create a handler in `internal/handlers/`
6. Create templates in `internal/templates/`
7. Register routes in `internal/handlers/router.go`

## Deployment

See [`deploy/README.md`](deploy/README.md) for detailed platform-specific instructions including:
- User creation and permissions
- Binary installation
- Config file setup
- Nginx reverse proxy
- Cross-platform checksum verification

Quick commands:

### Linux (systemd)

```bash
make install-linux
systemctl enable --now freefsm
```

### FreeBSD (rc.d)

```bash
gmake install-freebsd
service freefsm start
```

## Whole-instance backup and restore

Administrators can use **Settings → Backup** to create
password-encrypted `.age` archives and replace an instance from a reviewed archive.
Fresh account-password verification is required for download and restore.
Development builds (including dirty, untagged and `go run` builds) support backup
and restore and are explicitly labeled. All restores require matching trusted-local
database schemas; between two releases the exact version and commit must also match.

Successful restore expires all sessions and persistently disables outbound email.
Log in using a restored account and review SMTP settings and queued work before
explicitly enabling email. Archive passwords are not retained or recoverable.
Deployment Operators remain responsible for host and infrastructure recovery.
See [deployment and persistent recovery storage](deploy/README.md#whole-instance-backup-and-recovery)
before using the feature.

## License

AGPL-3.0
