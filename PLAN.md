# FreeFSM — Free Field Service Manager

## Vision
Self-hosted, open-source field service management for FreeBSD and Linux. Single binary, PostgreSQL, zero proprietary dependencies.

---

## Tech Stack

| Layer | Choice | Why |
|---|---|---|
| **Backend** | Go (chi router) | Single binary, cross-compiles to FreeBSD/Linux, excellent stdlib |
| **Frontend** | Templ + HTMX + Alpine.js + Pico CSS | Templ gives compile-time type safety. HTMX for server-driven interactivity. Alpine for client state. Zero node deps or build step. |
| **Database** | PostgreSQL | JSONB for line items, custom fields, polymorphic data |
| **Data Access** | `ent` ORM + `entpoly` | Type-safe polymorphic relations (tags, comments, custom fields). Eager loading with batching, no N+1. |
| **Query Gen** | `sqlc` | Type-safe SQL for complex queries `ent` can't handle |
| **Auth** | bcrypt + HTTP-only session cookies | Standard, proven |
| **PDF** | maroto (MVP), chromedp (Phase 2) | maroto for simple invoices first; swap to headless Chromium for pixel-perfect later |
| **Deploy** | FreeBSD rc.d + systemd + Makefile | One `make install` per platform |

## Architecture

```
┌─────────────────────────────────────────┐
│            Go Binary (chi)              │
│  ┌──────────────┬────────────────────┐  │
│  │  Web UI      │    REST API       │  │
│  │ (Templ+HTMX) │    (JSON API)     │  │
│  ├──────────────┴────────────────────┤  │
│  │       Services (CQRS-lite)       │  │
│  ├──────────────────────────────────┤  │
│  │   ent ORM + entpoly (polymorph)  │  │
│  │   sqlc (complex queries)         │  │
│  ├──────────────────────────────────┤  │
│  │     PostgreSQL (JSONB columns)   │  │
│  └──────────────────────────────────┘  │
│  FreeBSD rc.d / systemd / Makefile     │
└─────────────────────────────────────────┘
```

## Key Database Design Decisions

### JSONB columns (not separate tables) for owned value objects
- `invoices.line_items` — items, quantities, prices, taxes (snapshots, never queried alone)
- `invoices.payments` — payment records attached to invoice
- `estimates.line_items` — same structure
- `jobs.visits` — multi-visit scheduling data with arrival windows
- `jobs.assignments` — team + user assignments
- `entities.custom_fields` — extensible fields on customers, jobs, invoices, etc.

**Why JSONB**: Line items and payments are value objects — they're always fetched with their parent, never queried independently. PostgreSQL's JSONB avoids joins, enforces snapshot semantics, and supports indexing when needed. This is the same approach used by Invoice Ninja (production-proven).

### Polymorphic relations (separate tables with type/ID pattern)
- `tags` — `object_type` + `object_id`
- `comments` — `object_type` + `object_id`
- `locations` — `object_type` + `object_id`

### Status workflows (configurable, not hardcoded enums)
- `status_workflows` (name, applies_to: job/invoice/estimate)
- `statuses` (workflow_id, name, color, sort_order)
- Each entity has `status_id` FK -> `statuses`

## Data Model

### Core Entities (MVP — Phase 1)

**User / Session**
- `users` — id, email, password_hash, name, role (admin/tech/dispatcher), created_at, updated_at
- `sessions` — id, token_hash, user_id, expires_at

**Customer**
- `customers` — id, first_name, last_name, display_name, email, phone, notes,
  company_name, status (lead/opportunity/customer/lost/inactive),
  pipeline_status_id, lead_source_id, assigned_to,
  billing_address fields (4), service_address fields (4),
  account_type (individual/company), custom_fields JSONB,
  created_at, updated_at

**Customer Contact** (nested under customer)
- `customer_contacts` — id, customer_id, first_name, last_name, email, phone, notes, sort_order

**Location** (polymorphic)
- `locations` — id, object_type, object_id, title, address_1, address_2, city, state, zip,
  notes, is_primary, created_at, updated_at

**Job / Work Order**
- `jobs` — id, customer_id, project_id, location_id, customer_contact_id,
  job_type, subtitle, status_id, visits JSONB, assignments JSONB,
  start_time, end_time, due_date, arrival_window_start, arrival_window_end,
  notes, field_notes, billing_type, custom_fields JSONB,
  created_at, updated_at

**Project** (groups jobs under a customer)
- `projects` — id, customer_id, name, description, status, location_id,
  completion_percentage, start_time, end_time, notes, created_at

**Item / Pricebook**
- `items` — id, name, type (service/product), sku, unit_price, unit_cost,
  taxable, tax_rate, track_inventory, description, is_active,
  created_at

**Invoice**
- `invoices` — id, customer_id, job_id, status_id, title, notes,
  invoice_date, due_date, tax_rate, line_items JSONB, payments JSONB,
  display_settings JSONB, created_at, updated_at

**Estimate** (same structure as Invoice)
- `estimates` — id, customer_id, job_id, status_id, title, notes,
  line_items JSONB, created_at, updated_at

**Status Workflow**
- `status_workflows` — id, name, object_type (job/invoice/estimate)
- `statuses` — id, workflow_id, name, color, sort_order

### Support Entities (Post-MVP)

- **Asset** — Customer equipment (manufacturer, model, serial, warranty, install_date)
- **Asset Category** — Grouping for assets
- **Vendor** — Supplier management
- **Purchase Order** — Vendor ordering (items, delivery, status, payment_status)
- **Material List** — Grouped line items connectable to jobs/projects/invoices
- **Subtask** — Checklist items on jobs
- **Timesheet** — Clock in/out per user, linked to jobs
- **Tag** — Polymorphic (object_type + object_id)
- **Comment** — Polymorphic (object_type + object_id)
- **Custom Field Definition** — Admin-configurable fields per entity type
- **Contract / Maintenance Agreement** — Recurring service
- **Lead Source** — Marketing source tracking
- **Pipeline Status** — Sales pipeline stages

## API Endpoints (Phase 4)

113 endpoints across 26 resource groups, all using:
- `x-api-key` header auth
- Standardized pagination: `limit`, `page`, `search`, `filter[]`, `sort[]`, `rel[]`
- Polymorphic relations via `object_type` + `object_id`
- JSON request/response bodies

### Resource Groups

| Group | Endpoints | Purpose |
|---|---|---|
| `/customers` | 5 | Customer CRUD |
| `/customers/:id/customer-contact` | 5 | Customer contact CRUD |
| `/jobs` | 7 | Job CRUD + status workflows |
| `/invoices` | 8 | Invoice CRUD + price update + status workflows |
| `/estimates` | 7 | Estimate CRUD + status workflows |
| `/items` | 4 | Pricebook CRUD |
| `/projects` | 5 | Project CRUD |
| `/payments` | 5 | Payment CRUD |
| `/locations` | 5 | Location CRUD |
| `/purchase-orders` | 5 | Purchase order CRUD |
| `/assets` | 5 | Asset CRUD |
| `/assets-category` | 5 | Asset category CRUD |
| `/material-list` | 7 | Material list CRUD + connect/disconnect |
| `/subtasks` | 5 | Subtask CRUD |
| `/timesheets` | 5 | Timesheet CRUD |
| `/tags` | 5 | Tag CRUD |
| `/custom-fields` | 5 | Custom field definition CRUD |
| `/comments` | 5 | Comment CRUD |
| `/users` | 1 | List users |
| `/teams` | 1 | List teams |
| `/vendors` | 2 | List and view vendors |
| `/lead-source` | 1 | List lead sources |
| `/pipeline-status` | 1 | List pipeline statuses |
| `/contracts` | 2 | List and view contracts |
| `/company-profile` | 2 | List and view company profiles |
| `/version` | 1 | API version |

### Webhook Events (Phase 4)
- Job Created / Custom Status Update / Start Time Update / End Time Update
- Estimate Created / Custom Status Update / Workflow Status Update
- Invoice Created / Custom Status Update / Workflow Status Update

## Project Structure

```
freefsm/
├── cmd/
│   └── freefsm/
│       └── main.go
├── internal/
│   ├── config/
│   │   └── config.go
│   ├── database/
│   │   ├── database.go
│   │   └── migrations/
│   │       ├── 001_users.up.sql
│   │       ├── 002_customers.up.sql
│   │       ├── 003_jobs.up.sql
│   │       ├── 004_invoices.up.sql
│   │       ├── 005_items.up.sql
│   │       └── ...
│   ├── handlers/
│   │   ├── router.go
│   │   ├── auth.go
│   │   ├── dashboard.go
│   │   ├── customers.go
│   │   ├── jobs.go
│   │   ├── invoices.go
│   │   ├── estimates.go
│   │   ├── schedule.go
│   │   ├── items.go
│   │   ├── payments.go
│   │   ├── projects.go
│   │   └── api/
│   │       └── ... (REST API)
│   ├── middleware/
│   │   ├── auth.go
│   │   └── session.go
│   ├── ent/                    (generated by ent)
│   │   └── schema/             (ent schema definitions)
│   ├── repository/             (sqlc generated)
│   ├── services/
│   │   ├── auth.go
│   │   ├── customer.go
│   │   ├── job.go
│   │   ├── invoice.go
│   │   └── ...
│   ├── templates/              (Templ files)
│   │   ├── layouts/
│   │   │   ├── base.templ
│   │   │   └── auth.templ
│   │   ├── pages/
│   │   │   ├── dashboard.templ
│   │   │   ├── login.templ
│   │   │   ├── setup.templ
│   │   │   ├── customers/
│   │   │   ├── jobs/
│   │   │   ├── invoices/
│   │   │   ├── estimates/
│   │   │   ├── schedule/
│   │   │   ├── items/
│   │   │   └── settings/
│   │   └── partials/
│   │       ├── nav.templ
│   │       ├── sidebar.templ
│   │       ├── pagination.templ
│   │       └── flash.templ
│   └── pdf/
│       ├── invoice.go
│       └── templates/
├── ui/
│   └── static/
│       ├── css/
│       │   └── app.css
│       └── js/
│           ├── app.js
│           └── calendar.js
├── deploy/
│   ├── freebsd/
│   │   └── freefsm
│   └── linux/
│       └── freefsm.service
├── go.mod
├── go.sum
├── Makefile
├── .gitignore
├── README.md
└── PLAN.md
```

## Phased Delivery

### Phase 0 — Foundation (1-2 days)
- `go.mod`, `Makefile`, `.gitignore`, `README.md`
- Config system: env vars + CLI flags + optional YAML
- PostgreSQL connection + migration runner
- `ent` schema setup + `entpoly` for polymorphic relations
- `sqlc` config + first generated query
- `Templ` setup + first render test
- FreeBSD rc.d script + Linux systemd unit
- `make install` target
- `./freefsm` runs, prints help, connects to DB, exits cleanly

### Phase 1 — MVP: Auth + Customers + Jobs + Schedule + Invoices (2-3 weeks)

**Step 1: Auth + UI Shell**
- Setup token for initial admin registration
- Login/logout with bcrypt + session cookies
- Base Templ layout with sidebar navigation
- Flash messages, CSRF protection

**Step 2: Customers**
- Customer CRUD (list, create, view, edit, delete)
- Search + filter + pagination
- Customer contacts (nested CRUD inline)
- Customer locations (polymorphic base)
- Pipeline status + lead source selection
- Custom fields as JSONB

**Step 3: Jobs / Work Orders**
- Job CRUD with configurable status workflow
- Job assignments (team + user) via JSONB
- Multi-visit support via JSONB `visits`
- Arrival window scheduling
- Job notes + field notes
- Polymorphic tags + comments on jobs

**Step 4: Schedule / Dispatch**
- Calendar view (month/week/day) using Alpine.js
- Drag-and-drop status changes
- Filter by team/user/status

**Step 5: Items / Pricebook**
- Item CRUD (service + product types)
- SKU, price, cost, tax settings
- Price tiers

**Step 6: Estimates & Invoices**
- Create estimate with line items from pricebook
- JSONB `line_items` (snapshot semantics, copied from pricebook)
- Convert estimate -> job -> invoice
- Invoice status workflow (draft -> invoiced -> paid -> void)
- Payment recording (JSONB `payments` on invoice)
- Basic PDF generation via maroto

### Phase 2 — Support Entities + Polish
- Projects (grouping jobs under a customer) ✅
- Subtasks (job checklists)
- Polymorphic tags system (admin-configurable)
- Polymorphic custom fields (admin-configurable)
- Polymorphic comments
- Dashboard with real KPIs (revenue, job counts, status breakdown) ✅
- Global search across customers, jobs, invoices
- Nginx reverse proxy config in deploy/

### Phase 3 — Operations
- Timesheets (clock in/out, GPS? maybe)
- Assets + asset categories (customer equipment tracking)
- Purchase orders + vendors
- Material lists (grouped line items connectable to entities)
- Lead sources + pipeline management
- Recurring job / maintenance agreement templates

### Phase 4 — Integration + Advanced
- REST API covering 113 standardized endpoints
- Webhook system (standard webhook events)
- Chromium-based PDF rendering (swap from maroto)
- QuickBooks export (QBO/QBD)
- Customer portal / self-service booking
- Zapier-compatible webhook triggers
- Mobile-responsive UI refinements

## Key Architectural Patterns

1. **Polymorphic Relations** — Tags, custom fields, comments, and locations use `object_type` + `object_id` to attach to multiple entities. Built via `entpoly`.

2. **Status Workflow System** — Jobs, estimates, and invoices share a common workflow system. Statuses can be system defaults or custom workflow-based. Workflows have ordered statuses. Configurable per-company.

3. **Line Items Architecture** — Estimates and invoices use a JSONB array of line items (snapshots copied from pricebook at creation). Three conceptual categories: required, optional, not-optional (tracked via a `type` field). Each line item has: item_id, title, description, unit_price, quantity, taxable, tax_rate, discounts, surcharges.

4. **Assignment Pattern** — Jobs, material lists, subtasks use a JSONB array with `team_id` + `assigned_members[]` (user IDs).

5. **Standardized Query System** — All list endpoints support: `limit`, `page`, `search`, `sort_by`/`sort_dir`, `filter[]` array, `rel[]` (eager loading), `calculate_count`.

6. **Custom Fields Everywhere** — Most entities support custom fields stored as JSONB `{ field_instance_id, value }[]`.
