# Numbered commercial document commands

Estimates and Invoices retain independent, immutable creation numbers. The
existing reviewed-number cutover remains authoritative: an unknown historical
label is not reconstructed from today's prefix. Commercial migrations repair
future allocation high-water marks from recorded numbers and reservations;
they never renumber documents, reset workflow, reprice snapshots, or infer old
actors or revisions. `commercial_revision = 1` is a cutover concurrency baseline,
not a claim that the document had only one historical version.

## Editing and Customer changes

Web details saves require the form's save identity and whole-commercial revision.
Missing metadata returns 428; stale or ABA-changed intent returns 409. Content,
dates, fields, workflow and anchors share one commercial clock, including writers
outside the editor. Comments and Files remain independent. Field-level activity
records changed before/after values, rather than offering restorable revisions.

Assigned Jobs editing cannot change Customer or Job, even between two assigned
Jobs. All Records editors can choose an eligible Customer, review its applicable
defaults, retain or explicitly apply those defaults, and save. An incompatible
Job is cleared. Source conversion provenance is retained. Lease Invoice Customer
immutability is enforced when the Lease owning slice supplies its `lease_id`
anchor; this ticket does not introduce Lease generation.

Job Customer replacement is one fenced transaction: clear operational master
links, reassign operational Estimates and editable Draft Invoices, and audit the
inseparable effects using Job planning authority. Finalized, settled, or archived
Invoices must be unlinked first. Permanent `job_customer_blockers` entries forbid
replacement. Conversion tombstones retain historical anchors rather than being
rewritten as operational links. A newly linked document, document revision, or
Service Entry invalidates the reviewed Job form. Settlement and all commercial
writers acquire the common authority fence before Customer/Invoice row locks.

The Service Entry owner must register its permanent blocker in its own creation
transaction, after validating the current Job/Asset relationship under the same
authority fence. Revising or removing an entry never removes that blocker.

## Public field API and reference client

Authenticated routes:

- `POST /api/v1/estimates`, `POST /api/v1/invoices`
- `GET|PATCH /api/v1/estimates/{id}`, `GET|PATCH /api/v1/invoices/{id}`
- `GET /api/v1/commercial-operations/{operation_id}`

Every command carries a UUID `operation_id`. Creation additionally carries a
UUID `client_id`, a nonzero `job_id`, and a title. The server derives Customer
from the current authorized Job and allocates the number. No endpoint falls back
to Customer-only creation. Patch carries the originally read `job_id` and
opaque `revision`; it cannot choose new anchors or source provenance.

```json
{
  "operation_id": "47000000-0000-4000-8000-000000000001",
  "client_id": "47000000-0000-4000-8000-000000000002",
  "job_id": 123,
  "title": "Inspection proposal",
  "line_items": [
    {"title": "Inspection", "quantity": 2, "unit_price": "17.25", "taxable": false}
  ]
}
```

Money inputs are decimal strings. Optional `notes`, `terms`, `tax_rate`, dates,
lines, and typed `custom_fields` follow the published OpenAPI contract. Explicit
values win over Customer defaults and company defaults. Omitted Invoice date
uses company today. Empty optional date input explicitly means no date; Invoice
read `due_date: null` represents No Due Date. Estimate Valid Through is a
discovery value, not a workflow transition.

Accepted responses contain immutable effect identity and disposition. Replay
returns that same receipt without duplicate effects or audit. Changed intent
under an existing operation ID is rejected. Terminal validation or concurrency
failure stays terminal: review current authorized state and submit a new UUID.
Audit/storage failure leaves no effect or terminal receipt, allowing the same
intent and identity to be retried after inspection. Receipt lookup survives
record-scope loss and contains no commercial content; current document reads
still require current authority and captured pricing has an independent grant.

`internal/api/v1/testdata/commercial-reference-harness.cjs` is an executable,
in-memory reference reconciler. It stages without network access, preserves
uncertain identities after lost responses, queries receipts before retry, and
requires explicit same-target repair after conflict. It does not implement a
production encrypted mobile cache or claim the broader synchronization protocol.

## Verification seams

The commercial acceptance tests exercise authenticated production web/API
routing over migrated PostgreSQL, database invariants and contention, upgrade
fixtures, a real-network reference client, and persisted Chromium phone forms.
Run with an explicitly disposable `FREEFSM_TEST_DATABASE_URL`. Browser tests also
require `BROWSER_TESTS=1`, Playwright, and `BROWSER_EXECUTABLE`. Mandatory repository
checks remain `make lint` and explicit-database `make test` (including `-race`).
