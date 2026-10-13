# Durable CSV exports

Customer list → **Export Customer CSV** reviews the current search, sorting,
archive inclusion and **All Records** scope. It exports every matching row,
independent of the list's 25/50/100-row page. **My CSV exports** lists committed
requests, including after navigation, a lost response or a process restart.
An interrupted click alone is not acceptance. Check that list before starting a
new request. Reposting the same operation identity with the same intent returns
the original job; changed intent is rejected.

## Customer operational v1 CSV contract

UTF-8, comma-separated, RFC 4180-compatible quoting, LF record separators,
one header and one record per Customer. Columns, in fixed order:

| Column | Value |
|---|---|
| `customer_id` | Stable Customer ID |
| `display_name` | Display name |
| `kind` | Individual or Organization |
| `first_name` | Person first name |
| `last_name` | Person last name |
| `organization_name` | Organization name |
| `email` | Main email |
| `phone` | Main phone |
| `service_notes` | Operational Service Notes |
| `archived` | `true` or `false` at snapshot time |

There are no billing addresses, tax settings, financial measures, settlement,
Credit, child records or other independent facets in this contract, even for an
Administrator. Search follows the Customer list's case-insensitive identity and
authorized active Contact/Location relationship matching. Relationship matches
never multiply Customer rows. Ordering is lower-case display name + ID ascending,
both descending for `name_desc`, or updated time + ID descending for `updated`.
Duplicate names remain distinct.

**Spreadsheet escaping is explicit and reversible.** Any text whose first
non-whitespace/non-control character is `=`, `+`, `-`, or `@` receives one leading
apostrophe. Text already beginning with an apostrophe also receives one. To
recover the original value, remove exactly the added first apostrophe in these
cases. This includes phone numbers beginning with `+`. No trimming, formula
wrapping, normalization, numeric conversion or business-value rewriting occurs.
Ordinary CSV quoting independently preserves commas, quotes, Unicode and line
breaks. The confirmation explains this encoding before acceptance.

The result page gives the as-of UTC time and total row count from the same
snapshot. No matches is a Ready, header-only CSV, explicitly distinguished from
Pending, Running, Failed, and a completed nonempty result.

## Durability, access and failure

All sizes use Pending → Running → Ready or Failed. The background worker starts
with the application, participates in instance maintenance admission, refreshes
connections after restore, and stops with application cancellation. A PostgreSQL
session advisory lock elects one generator across processes. Abandoned Running
work is recoverable; Ready jobs are never generated again.

The authority fence is acquired **before** a repeatable-read transaction takes
its snapshot. This prevents an exporter waiting behind a grant revocation from
using stale grants. Every row, its authorization reference, the header, SHA-256
digest, count, as-of time, Ready timestamp and generation audit commit together.
Customer rows stream through a cursor in batches of 128. There is no result cap
or page-size truncation. The shared fence serializes production mutations during
generation; this favors coherent authorization over writer throughput. There is
no export latency or unlimited-capacity promise.

The original requester alone can obtain status or bytes; even another protected
Administrator cannot download the result. Every download verifies the current
session, compatible Export/View, requested facets, every included row and any
relationship-search facet before any bytes are sent. Metadata receives the same
checks. One lost row/facet blocks the whole result; an observed block is durable
and requires a new export even if access is later restored. Receipts retain only
nonsensitive disposition after loss. No subset filtering occurs.

The expiry boundary is `now >= ready_at + 24 hours`, checked again after transfer
preparation. Starting a transfer does not extend it. Expired/blocked pages offer
regeneration. Expiry is an access cutoff, not a secure-erasure promise. Downloaded
copies and existing backups are outside subsequent product access control.

The complete durable artifact lives in PostgreSQL (`export_jobs` and
`export_records`), so the existing consistent database backup includes accepted
work and artifacts without a second durable filesystem publication protocol.
Database/storage/audit failure rolls back publication. A live worker records an
actionable Failed status; an unavailable database or killed worker leaves the
last committed state recoverable. No raw dependency error is exposed in status.

Downloads spool to a private mode-0600 temporary file, verify the full digest,
sync/rewind, then atomically authorize and audit transfer preparation. Missing,
corrupt or unpreparable bytes and audit failure send no CSV. Temporary files are
removed on close. Operators must provide sufficient PostgreSQL and `TMPDIR`
capacity (one complete CSV per concurrent transfer), and include PostgreSQL in
their existing backup/retention planning. Generation is audited with current
requester Role/Permission/scope, worker channel and explicit system executor plus
initiator; each authorized download is audited. The download event is evidence
of authorized transfer preparation, not proof that a remote client received or
retained every byte. Network interruption cannot be recalled or inferred away.

## Downstream export seam

`internal/exports.Definition` is the versioned domain contract. Its adapter owns:

1. Kind, fixed columns, independent permission/scope and validated options.
2. Contract authorization, including all requested facets even for zero rows.
3. Snapshot selection, ordering and emitted authorization references using the
   supplied repeatable-read transaction. Additional report totals/queries must
   use that transaction, never a pool query or a second snapshot.
4. Current whole-record/facet recheck for **every** emitted reference, including
   stable Own/Assigned anchors, archived access and sensitive field facets.

The engine owns intent deduplication, queue/restart/concurrency, transactional
artifacts, CSV escaping, requester isolation, expiry, complete transfer
preparation, nondisclosing receipt/status and audit. Register new definitions in
`exports.Product`, the single HTTP/worker registry. Keep old versioned kinds
registered while their artifacts may remain available; never change an existing
kind's column/reference meaning. Definition code must enforce domain lifecycle
and row authority under the shared fence. No adapter may interpret a stored
reference as a permanent grant. Public field API exports remain excluded.

The migration is additive and grants nothing to existing Roles. Existing
Customer identity and relationship facts are retained.
