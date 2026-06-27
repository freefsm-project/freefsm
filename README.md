# FreeFSM — Free Field Service Manager

Self-hosted, open-source field service management for FreeBSD and Linux.
Single static Go binary, PostgreSQL backend, zero npm/NPM dependencies.

## Features

- **Dashboard** — customizable widgets, KPI cards, work queues, time clock, quick actions, global search
- **Customers** — full CRUD with reusable locations, contacts, financial summary, receivables graph, related work, search, HTMX pagination
- **Assets** — equipment tracking with types, statuses, service history
- **Jobs** — work orders with configurable status workflow, inline status updates, scheduling, arrival windows, subtasks, job-linked clock in/out
- **Schedule** — list, calendar, dispatch, and map views with clickable job cards and drag/drop scheduling
- **Projects** — nested jobs, progress tracking, customer and location linkage
- **Estimates** — line items editor, tax calculation, PDF generation, email with CC/BCC, convert to invoice
- **Invoices** — configurable numbering, line items, payment recording, PDF generation, email with CC/BCC, finalization workflow, status workflow
- **Items / Pricebook** — service and product catalog with SKU and pricing
- **Timesheets** — standalone or job-linked clock in/out, GPS coordinates, manual entry flag
- **Tags** — color-coded labels on any object (customers, jobs, assets, etc.)
- **Custom Fields** — user-defined fields per object type
- **Comments** — threaded notes on any object
- **User Management** — roles, welcome emails, password policies, force password change
- **Company Settings** — branding, email config, timezone, invoice numbering, job statuses, map settings, document defaults, security policies
- **Dark Mode** — persistent theme toggle
- **Activity / Audit Log** — every mutation tracked across all entities; per-entity widgets on show pages, admin-only admin activity, recent activity panels on every list page
- **File Attachments** — polymorphic uploads on customers, jobs, estimates, invoices, and assets; MIME whitelist, inline preview for images/PDFs, disk storage
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

- Go 1.25+
- PostgreSQL 16+
- `ent` CLI: `go install entgo.io/ent/cmd/ent@latest`
- `templ` CLI: `go install github.com/a-h/templ/cmd/templ@latest`
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
git clone https://github.com/MartialM1nd/freefsm.git
cd freefsm
cp .env.example .env
# Edit .env: set DB_PASSWORD, SESSION_SECRET, SETUP_TOKEN

make run
# → http://localhost:3000
```

### First-Time Setup

1. Visit `http://localhost:3000` — you'll be redirected to `/setup`
2. Enter your `FREEFSM_SETUP_TOKEN` value (from `.env`)
3. Create an admin account (name, email, password)
4. You're logged in and on the Dashboard

`FREEFSM_SETUP_TOKEN` is required at startup and is only accepted while no users exist.

### Demo Data

Populate the database with sample HVAC-themed data (customers, jobs, assets, invoices, etc.) for testing:

```bash
./dist/freefsm -seed
```

This is idempotent — it skips if any customers already exist.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `FREEFSM_DB_HOST` | `localhost` | PostgreSQL host |
| `FREEFSM_DB_PORT` | `5432` | PostgreSQL port |
| `FREEFSM_DB_NAME` | `freefsm` | Database name |
| `FREEFSM_DB_USER` | `freefsm` | Database user |
| `FREEFSM_DB_PASSWORD` | *(required)* | Database password |
| `FREEFSM_DB_SSLMODE` | `disable` | PostgreSQL SSL mode |
| `FREEFSM_ADDR` | `:3000` | HTTP listen address |
| `FREEFSM_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `FREEFSM_LOG_FILE` | *(empty)* | Optional file path for application logs |
| `FREEFSM_SESSION_SECRET` | *(required)* | Cookie encryption key |
| `FREEFSM_SETUP_TOKEN` | *(required)* | Initial admin registration token |
| `FREEFSM_PUBLIC_URL` | request host | Public base URL for emailed links |
| `FREEFSM_UPLOAD_DIR` | `/var/lib/freefsm/uploads` (Linux) / `/var/db/freefsm/uploads` (FreeBSD) | File upload storage directory |
| `FREEFSM_MAX_UPLOAD_SIZE` | `26214400` (25 MB) | Maximum upload file size in bytes |
| `FREEFSM_TILE_URL` | `https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png` | Default map tile URL template |
| `FREEFSM_GEOCODER_URL` | *(empty)* | Optional geocoder base URL for map location lookup |

### In-App Settings

Administrators can manage runtime company settings from the Settings screens:

- Company profile, timezone, invoice prefix, next invoice number, and estimate prefix
- Job statuses with custom names, colors, ordering, usage counts, and replacement on delete
- SMTP settings, invoice/estimate email defaults, and automatic CC recipients
- PDF branding, logo, colors, footer text, payment terms, and line item description visibility
- Map tile and geocoder URLs for schedule map features
- Password and session security policies

## Project Structure

```
freefsm/
├── cmd/freefsm/           # Entry point + static file embed
│   └── static/            # CSS, JS (Pico, HTMX, Alpine)
├── internal/
│   ├── config/            # Env loading + DSN builder
│   ├── database/          # pgxpool connection + SQL migration runner
│   │   └── migrations/    # SQL migration files
│   ├── ent/
│   │   └── schema/        # ent schema definitions
│   ├── handlers/          # HTTP handlers (chi routes)
│   ├── middleware/         # Auth, Flash, user context, CSRF
│   ├── services/          # Business logic (ent queries)
│   └── templates/         # Templ files (pages + partials)
├── deploy/
│   ├── freebsd/           # rc.d service script
│   ├── linux/             # systemd unit + config sample
│   └── README.md          # Detailed deployment guide
├── AGENTS.md              # Agent guidelines (this project)
├── Makefile               # build, install, fmt, lint, test
├── PLAN.md                # Full roadmap + architecture
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

## License

AGPL-3.0
