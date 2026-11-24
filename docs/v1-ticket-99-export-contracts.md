# Ticket 99 fixed v1 financial CSV contracts

Contract handoff, 2026-11-23. This document freezes the five output contracts for
the approved #99 implementation phase. Adapter checks are recorded below;
complete ticket acceptance remains coordinator-owned. Sources: live #99 and PRD #18 (DISC-02/04/05/06, MAS-02,
FIN-01–04, PER-07/12, API-01), ADRs 0001–0003, the #96 durable engine and #98
adapters. The independent reviewer decisions supplied by the coordinator override
the earlier permission proposals in `v1-ticket-99-reconciliation.md`.

## Five registrations and authority

All five definitions have fixed `authorization.All` scope (`A`). There is no
client-selected scope or variable column set.

| Definition | Immutable kind | Export grant | View prerequisites |
|---|---|---|---|
| `InvoiceAging` | `invoice-aging-A-v1` | `customer.financial.export` | `customer.view:A`, `customer.financial.view:A`, compatible `invoice.view:A` or `invoice.view:J` |
| `CustomerFinancials` | `customer-financial-A-v1` | `customer.financial.export` | Same as Aging, including Invoice View for every contributing Invoice |
| `SettlementActivity` | `settlement-activity-A-v1` | `settlement.export` | `customer.view:A`, `customer.financial.view:A`, `settlement.view:A` |
| `CurrentCredit` | `current-credit-A-v1` | `credit.export` | `customer.view:A`, `customer.financial.view:A`, `credit.view:A` |
| `PaymentsCredits` | `payments-credits-A-v1` | `settlement.export` **and** `credit.export` | Union of Settlement activity and Current Credit prerequisites |

These are the existing catalog prerequisite chains, checked explicitly even when
the result is empty. Aging and Customer financials require compatible Invoice
View, **not Invoice Export**. Assigned Invoice View restricts contributors to
currently eligible assigned Jobs; the financial Export grant does not widen it.
Operational Customer Export grants none of these outputs.

Settlement history includes immutable Credit creation/application and persisted
source allocations under settlement authority alone; it contains no current
Credit balance and requires no Credit Export. Current Credit contains intrinsic
Credit provenance, not receipt method/reference/actor/notes/history, and requires
no settlement grant. Combined requires both exports but no additional
`customer.financial.export`. Its `Permission()` is `settlement.export`; its
`Authorize` checks both full chains on every invocation.

Explicit archived history requires `customer.archived.view:A`, including empty
results. Every selected Customer is checked before selection; inaccessible or
wrong-company IDs fail without disclosing existence. Office web only: a public
field API bearer cannot accept, inspect or download these exports regardless of
its user's grants.

## Strict filters, defaults and selection

Options are one JSON object. Reject unknown or duplicate keys, trailing JSON,
null values, incorrect types, invalid enums, fractional/nonpositive/overflowing
IDs, malformed calendar dates and reversed bounds. Omission means the documented
default; empty optional date strings mean unbounded. No pagination, limit, offset,
arbitrary SQL/sort expression, as-of input, timezone override, field selection,
search, event-type filter or unknown-time exclusion option is accepted. Review
and acceptance use the same normalizer and retain canonical options for recovery.

| Output | Accepted keys | Defaults / fixed ordering |
|---|---|---|
| Invoice Aging | `customer_id`, `bucket`, `sort` | All authorized active Customers; `bucket=""` (all); `sort="customer_invoice"` |
| Customer financials | `customer_id`, `sort` | All authorized active Customers; `sort="customer"` |
| Settlement activity | `customer_id`, `from`, `to`, `include_archived`, `sort` | All authorized active Customers; unbounded dates; `include_archived=false`; `sort="entry_asc"` |
| Current Credit | `customer_id`, `include_archived`, `sort` | All authorized active Customers; `include_archived=false`; `sort="customer_source"` |
| Payments/Credits | `customer_id`, `from`, `to`, `include_archived`, `sort` | Same activity defaults; only/default `sort="entry_asc"`; Credit always uses `customer_source` |

Each sort accepts only the named value (or empty, normalized to that value).
`include_archived=true` adds authorized archived Customers; it does not select
only archived Customers or reopen them. An explicitly selected archived Customer
requires this option and archived View. Aging/Customer financials are current
receivables and reject archived inclusion and archived Customer selection.

`bucket` accepts only empty, `not_yet_due`, `1_30`, `31_60`, `61_90`, `91_plus`,
`no_due_date`. Due today or later is `not_yet_due`. Overdue days use calendar-date
subtraction in the captured company timezone, never elapsed 24-hour intervals.
Missing and legacy zero-sentinel due dates are `no_due_date`. SQL DATE values
remain dates; converting a midnight UTC placeholder into company time must not
shift them to yesterday.

`from` and `to` are inclusive company-local entry dates (`YYYY-MM-DD`), each
optional. Resolve to local midnight and the midnight after `to`, then use a
half-open instant interval; preserve DST day lengths. No implicit current-month
default, no effective-date substitution. A single supplied bound is still a
bounded report. These predicates apply **only to known-entry activity**. Current
Credit never receives them, including in the combined output.

All outputs select all matching records, not a displayed page. Customer ordering
is `(lower(display_name), customer_id)` ascending under the database's established
text ordering. Aging then orders numeric Invoice number and Invoice ID ascending.
Credit then orders source effective date ascending, unknown dates last, and Credit
ID ascending. Activity has a known-time section ordered by
`(entered_at, event_type, event_id)` ascending (event type compared as the literal
ASCII key below); unknown-time events follow ordered by `(event_type, event_id)`.
Allocation rows immediately follow their event, ordered by source effective date
(unknown last), Credit ID, allocation kind, allocation ID; Payment allocations
without a Credit sort first. Customer/Invoice IDs and Invoice numbers use numeric
ordering. Financial event/source IDs use PostgreSQL UUID ordering (equivalently
canonical fixed-width UUID text ordering), never an invented sequence.

## Snapshot, encoding and unknown facts

Acceptance stores intent. Generation captures `transaction_timestamp()` as
`as_of` and the company IANA timezone once in the engine-supplied repeatable-read
transaction. All detail, allocations, summaries and current Credit use that same
transaction and as-of. This is generation-time state, not historical balance
reconstruction. Concurrent activity produces one coherent before/after snapshot.

CSV is UTF-8 with LF records and normal CSV quoting. Customer/Invoice IDs and
counts are decimal integers; Payment/Credit/Application/Refund/Reversal IDs are
their existing canonical lowercase UUID strings. Booleans are `true`/`false`;
dates `YYYY-MM-DD`; instants RFC3339Nano with
offset, emitted in UTC. `timezone` records the company zone used for calculations.
All amounts are exact USD cents rendered as decimal dollars with two fractional
digits, no currency symbol/grouping/exponent, e.g. `0.00`, `12.34`, `-12.34`.
Never convert through float arithmetic. Overflow or failed exact calculation is
an error, not a clamped or rounded successful report.

The existing engine escapes every formula-leading cell, including negative money,
by adding an apostrophe (also when preceded by whitespace/control characters),
and doubles an existing leading apostrophe. Decode by removing exactly that
added apostrophe before interpreting a typed value. CSV quoting is independent.
The confirmation explains this reversible spreadsheet protection.

Empty cells mean not applicable or absent optional data. For an applicable but
unknown event entry instant, `entered_at` is empty and `entry_time_known=false`;
known instants have `entry_time_known=true`. Current Credit uses the corresponding
`source_entered_at` and `source_entry_time_known` pair. No timestamp zero value,
effective date, migration timestamp or Credit-row insertion time substitutes for
unknown receipt entry time. Credit creation's entry evidence is its source
Payment's truthful entry evidence, including legacy unknowns. An unknown source
effective date is an empty `source_received_date`; no fabricated date is emitted.

Every activity report includes authorized unknown-entry events. Unbounded exports
put them in `section=unknown_entry_time` after `section=dated_history`. Bounded
exports put them in the same separate unknown section after `section=period`;
they cannot be claimed to fall inside or outside the requested period. Their
count, gross amount by event type, and signed cash effect are disclosed in that
section's own totals, excluded from all dated totals. This prevents a period
report from implying complete attribution while silently omitting legacy facts.
Unknown allocations stay with their parent event; allocation rows do not inflate
the unknown event count.

## Exact ordered headers and row semantics

Headers and row-type meanings are immutable for each kind. Summary rows repeat
`as_of` and `timezone`; activity additionally repeats normalized `period_from` and
`period_to` on every row (empty for omitted bounds). A total summarizes only the
selected authorized set. Summaries are not additive with details. No value named
“unapplied balance” duplicates Available Credit.

### Invoice Aging

```text
as_of,timezone,row_type,customer_id,customer,invoice_id,invoice_number,due_date,bucket,invoice_count,total_amount,settled_amount,outstanding_amount
```

- `invoice`: one eligible active, nonhidden, issued, non-Void, positive-due Invoice;
  `invoice_count=1`. Draft, Paid, zero/negative due, archived and inaccessible
  Invoices never contribute. `total_amount` is canonical commercial total,
  `settled_amount` is active immutable Payment allocations plus Credit Applications,
  and `outstanding_amount=total_amount-settled_amount`. Available Credit is never
  subtracted. The bucket filter applies before both rows and totals.
- `bucket_total`: after all Invoice rows, one row per selected bucket in the
  fixed bucket order listed above, including zero buckets. Only `bucket`, count
  and the three monetary columns are populated beyond common metadata.
- `grand_total`: last; count and monetary columns sum all selected Invoices.
  Customer, Invoice, due date and bucket cells are empty.

### Customer financials

```text
as_of,timezone,row_type,customer_id,customer,invoice_count,total_amount,settled_amount,outstanding_amount,not_yet_due_amount,overdue_1_30_amount,overdue_31_60_amount,overdue_61_90_amount,overdue_91_plus_amount,no_due_date_amount
```

- `customer`: one authorized active Customer, including zero-eligible-Invoice
  Customers. Invoice count and money use exactly the Aging eligible population
  without a bucket filter. Six aging amounts sum to outstanding. No Credit,
  Payment history or unrelated customer fields are included.
- `grand_total`: last; sums Customer rows; Customer identity cells empty. Summary
  authorization references retain every contributing Invoice, even though its ID
  is not a CSV column. A Customer-only reference is insufficient.

### Settlement activity

```text
as_of,timezone,row_type,section,period_from,period_to,customer_id,customer,customer_archived,event_type,event_id,original_event_type,original_event_id,effective_date,entered_at,entry_time_known,invoice_id,credit_id,source_payment_id,allocation_kind,allocation_id,source_received_date,event_count,event_amount,cash_effect,allocation_amount
```

Seven fixed `event_type` values:

| Type | Event identity | Original identity on a compensating event | Signed cash effect |
|---|---|---|---|
| `payment` | Payment ID | empty | +Payment amount, including unapplied |
| `credit_creation` | Credit ID | empty | zero |
| `credit_application` | Application ID | empty | zero |
| `credit_refund` | Refund ID | empty | −Refund amount |
| `payment_reversal` | Reversal ID | `payment`, original Payment ID | −original Payment amount |
| `application_reversal` | Reversal ID | `credit_application`, original Application ID | zero |
| `refund_reversal` | Reversal ID | `credit_refund`, original Refund ID | +original Refund amount |

- `event`: exactly one row per immutable event; `event_count=1`, `event_amount`
  is positive face amount, `cash_effect` follows the table. Credit creation's
  amount is original funded Credit, not full receipt or current availability.
  Reversals have their own effective/entry dates and keep the original ID even
  when the original lies outside the period. Never replace an original with a
  netted row or discard it because a later reversal exists. Populate intrinsic
  Invoice/Credit/source Payment IDs when applicable, not inferred detail fields.
- `allocation`: persisted provenance of the immediately preceding event, with
  `allocation_kind` equal to `payment_invoice`, `credit_application`, or
  `refund_credit`. `allocation_id` is the Payment ID (the allocation primary key), Application ID,
  or Refund ID respectively; refund allocations are uniquely identified by
  `(allocation_kind, allocation_id, credit_id)`. Copy the parent's event and
  original-event identities; populate source Credit/Payment, target Invoice and
  source received date where applicable. `allocation_amount` is positive for
  original allocation/consumption and negative for its reversal. Reversal rows
  reuse the original persisted allocation identities and exact amounts; no FIFO
  rerun at reporting time. `event_count`, `event_amount`, `cash_effect` are empty
  to prevent join multiplication. Entry/effective metadata matches the parent.
  Credit creation has no invented allocation; its source Payment is sufficient.
- `event_total`: after each section's details, seven rows in the table's order,
  including zeros, with `event_type`, event count, gross event amount and summed
  signed cash effect only (besides section/period/common metadata). Gross event
  amounts across types are **not** a cash or balance measure.
- `net_payments_total`: after those seven totals, event count and summed cash
  effect for the section; event amount and other fields empty. The formula is
  Payments − Payment reversals − refunds + refund reversals. Applications and
  Credit creation have zero cash effect. Never add this total to `event_total`.

Invoice IDs here are retained financial provenance, including after conversion
reversion or archive. They are not document links, Invoice number/title
enrichment, or authority to open a document. Settlement authority rechecks their
immutable ownership/source relationships, not ordinary Invoice View. No current
Credit fields, receipt method/reference/actor/notes or dynamic detail enrichment
are part of this fixed schema.

### Current Credit

```text
as_of,timezone,row_type,customer_id,customer,customer_archived,credit_id,source_payment_id,source_received_date,source_entered_at,source_entry_time_known,original_funded_amount,current_available_amount
```

- `credit_source`: one source with strictly positive current availability, whose
  origin Payment is not reversed. Available = original funded amount minus active
  Applications and active persisted refund allocations. Original refunded/applied
  events remain in settlement history, but no event history appears here.
  `source_payment_id`, `original_funded_amount`, truthful Payment received date
  and nullable entry time are intrinsic Credit provenance; no settlement grant
  is required. Exhausted or reversed sources are not current available sources.
- `current_credit_total`: last; only `current_available_amount` and common
  metadata populated. Original funding is deliberately not summed as a second
  balance. A zero-source result has this row with `0.00`.

### Payments/Credits

The exact union of the two fixed facet schemas, with shared columns appearing
once and the four remaining Credit columns appended to Settlement's header:

```text
as_of,timezone,row_type,section,period_from,period_to,customer_id,customer,customer_archived,event_type,event_id,original_event_type,original_event_id,effective_date,entered_at,entry_time_known,invoice_id,credit_id,source_payment_id,allocation_kind,allocation_id,source_received_date,event_count,event_amount,cash_effect,allocation_amount,source_entered_at,source_entry_time_known,original_funded_amount,current_available_amount
```

Emit the complete Settlement sections first, then Current Credit rows with
`section=current_credit`, using the same row types/semantics and source ordering.
Populate `period_from/to` as review context on Credit rows too; these do not
filter balances. Settlement-only columns are empty on Credit rows, Credit-only
columns empty on activity rows. Both facet selectors share one transaction,
Customer/archive selection, timestamp and timezone. Never add current Credit to
Net Payments. A period change alone cannot alter the current source set/balance
when the underlying snapshot is otherwise unchanged.

### Empty results and counts

These five new kinds deliberately emit typed zero totals rather than header-only
empties, so the CSV itself carries as-of and an explicit authorized zero result.
Aging has bucket/grand totals, Customer financials a grand total, Settlement both
sections' totals, Current Credit its total, and Combined both facets' totals.
Customer rows may legitimately have zero Invoice counts. Receipt `row_count` is
the number of CSV data records, including allocations and totals, **not** Invoice,
event or source count. UI result counts use the typed snapshot's detail counts;
never describe receipt row count as a count of financial events. Query failures
or incomplete iteration must fail preparation, never produce zero totals.

## Facet-specific references and whole-artifact policy

Each emitted record carries a private versioned authorization reference. Detail
references identify the facet, original Customer/company, financial source and
persisted ownership/allocation relationships. Summary references contain the
complete contributing reference set (deduplicated is fine); zero summaries still
carry their facet and selected-input authority. No contributor may become
unprotected merely because it appears only inside a total.

- Aging/Customer financials: check financial View/Export, Customer access and
  every original contributing Invoice's current compatible View/assignment and
  visibility. Preserve original/current Job anchors where assignment scope is
  relevant. Hidden state, lost assignment or missing required archived authority
  blocks old bytes.
- Settlement: check settlement View/Export and original Customer/source event/
  allocation ownership, plus current Customer archived authority when applicable.
  Retained Invoice IDs require no Invoice Export/View or Credit Export. Hidden
  provenance is not promoted to document access. A source's ordinary Invoice
  visibility change alone is not loss of this financial facet.
- Credit: check Credit View/Export, Customer/source Credit and source Payment
  ownership, plus current Customer archived authority. The Payment ID/date are
  intrinsic provenance; no settlement authority is required. Availability changes
  alone do not revoke a frozen snapshot.
- Combined: dispatch each stored reference by its fixed facet, and require both
  grant chains globally, including for zero detail rows.

Recheck authority and retained identity/relationships, not fresh equality of
money, labels, period matches or current balances. Data changes do not regenerate
or rewrite frozen bytes. Loss of a required grant, contributor access or retained
source invariant blocks the **entire** artifact. Never redact a column or return
a subset under the same kind. Status exposes only nonsensitive disposition after
loss; no filters, counts, as-of, source identities or totals leak through recovery.

Retain #96's Pending → Running → Ready/Failed workflow, worker restart recovery,
atomic records/header/digest/as-of/count/Ready/audit publication, requester/company
isolation and authority fence acquired before the snapshot. Every download checks
all references, spools privately and verifies full integrity before transactional
download audit and release of bytes. Expiry is exactly
`now >= ready_at + 24 hours`, checked again after preparation; starting earlier
does not extend it. Observed blocking is durable; restoration of access requires a
new export. Downloaded copies are outside later product revocation.

Extend `RecoverySelection`'s explicit adapter allowlist to these five definitions.
For Failed or Ready receipts only, check requester/company, both current grants
where applicable, selected inputs and **every** original Ready reference even
after expiry. Failed receipts have no published contributors but still require
current facet/selected-input access. Any failed check returns no selection
metadata. Authorized recovery opens a new same-filter confirmation with a new
operation ID; it never reuses bytes, extends expiry or silently replays acceptance.
Blocked recovery remains unavailable; the user may independently open a fresh
authorized report. Keep existing operational/legacy recovery behavior intact.

## Adapter architecture recommendation and next-phase handoff

Use the existing `exports.Definition` seam with five concrete, fixed adapters in
`internal/exports/financial_*.go`, registered once in `Product` for both HTTP and
worker use. Keep formatting local to those files; small shared money/time/header
helpers are appropriate. Do not introduce a generic financial export framework,
dynamic facet selection, configurable query engine or a second financial ledger.

The core-owned `docs/v1-ticket-99-interfaces.md` defines Go names/types separately.
This contract requires its deep reporting module to supply typed transaction-
taking selectors for eligible receivables, Customer summaries, immutable activity
with persisted allocations, and current Credit, plus normalized filters and
facet-specific reference checking. That module owns authorization-first selection,
canonical cents, recording-time semantics, unknown evidence and ordered totals.
HTTP and CSV consume the same results. The CSV adapter only validates the fixed
intent, invokes those selectors, formats rows and serializes references; it must
not duplicate financial SQL/arithmetic or call pool-backed helpers inside the
snapshot. Combined composes the two fixed selectors within the same supplied
transaction, never two separately acquired snapshots. Do not use
`CustomerMasterSettlement`, which couples settlement and Credit visibility.

`Definition.AuthorizeSelection` performs selected-Customer/archive checks at
acceptance, generation, status and download even for empties. `Recheck` delegates
current View checks to the core facet checker, separately checks every fixed
Export prerequisite, and rejects unknown/malformed reference variants.
Summary references must be supported explicitly; they are not unchecked `{}`.
The adapter's private `financialProvenance` identity tuples supplement the core
references with source Payment, Credit, original/compensating event and persisted
allocation relationships. Both detail and summary records retain these tuples.
Rechecks use narrow existence/ownership/relationship queries in the supplied
transaction; they do not recompute money, rerun FIFO or require document detail
grants. No source date or amount is fabricated to fill a missing identity.
The engine already owns queueing, CSV escaping, atomic publication, retention,
requester-only transfer and audit. No engine redesign is needed for these
contracts; `catalog.go` and `recovery.go` get explicit registrations only.

Next-phase verification must exercise the installed durable seam with real
PostgreSQL: exact headers/row widths/order, zero results, independent grants and
empty denial, all aging edges, signed money escaping, original/compensating events
and multi-source refunds without multiplication, truthful legacy unknowns,
period-independent Credit, contributor revocation/recovery, coherent concurrent
snapshots, and atomic query/emission/storage/audit failure. HTTP/browser/API gates
and full `make lint`/disposable-DB `make test` remain implementation acceptance
work.

## Implemented adapter handoff and checks (2026-11-23)

Zero-value definitions: `InvoiceAging`, `CustomerFinancials`, `SettlementActivity`,
`CurrentCredit`, `PaymentsCredits`. Exported string constants:
`InvoiceAgingKind`, `CustomerFinancialsKind`, `SettlementActivityKind`,
`CurrentCreditKind`, `PaymentsCreditsKind`. `Product` registers all five and
`RecoverySelection` explicitly allows their concrete types.

The exported helper is:

```go
func ParseFinancialExportFilter(
    kind authorization.FinancialReportKind,
    raw json.RawMessage,
) (services.FinancialReportFilter, error)
```

It strictly decodes each fixed kind's keys and calls the core normalizer. The
core-owned snapshot now supplies Customer total/settled cents, numeric Invoice
sort identity, nullable source Payment entry time and actual allocation kind/ID;
the adapters fully consume these fields. No missing-field scaffolding remains.

Only the dedicated `freefsm_issue99_exports` database was used by this adapter
owner. Final commands (exit 0):

```sh
source /tmp/opencode/issue99-evidence-20261123/env.sh
export FREEFSM_TEST_DATABASE_URL="$ISSUE99_EXPORTS_DATABASE_URL"
export GOFLAGS=-p=2 GOMAXPROCS=2
go test -race ./internal/exports -count=1 -timeout=10m -v
make lint
```

Logs: `/tmp/opencode/issue99-export-adapter-logs/exports-race-final.log`
and `/tmp/opencode/issue99-export-adapter-logs/make-lint.log`.
The final race run passed **21 top-level tests**, including **10 financial tests**
(six non-DB contract tests and four migrated-PostgreSQL adapter tests), plus eight
subtests; **zero skips**. Package duration: 29.594 seconds. `make lint` runs the
repository's full `go vet ./...` check and completed successfully with no output.

Financial coverage includes all five registrations/recovery paths; 105 matching
Invoices and exact summaries; independent grants on empty and nonempty exports;
absence of Invoice Export and cross-facet grants; requester isolation/expiry;
Assigned Invoice summary contributor loss; original receipts, Applications,
refunds and their reversals through installed settlement commands; multi-source
refund provenance without cash multiplication; legacy unknown Payment/Credit
entry time; period-independent current Credit; real conversion reversion with
retained historical Invoice identities; explicit summary-only ownership and
allocation-reference tampering; whole-artifact denial with redacted status and
recovery; transactional storage failure; and generation-time grant revocation.
Summary tampering deliberately leaves CSV bytes, digest and detail references
intact, demonstrating that totals independently recheck their source tuples.

Earlier attempts are retained in the same directory: `financial-race-attempt1.log`,
`financial-durable-attempt2.log`, `financial-race-attempt3.log`, and
`financial-race-attempt4.log`. Their failures were fixture errors: omitted web
audit channel, a noncanonical Payment reversal reason, direct Invoice hiding
without a reverted conversion cycle, and direct status mutation outside
statusflow. Fixtures now use the explicit web actor, `external_return`, actual
Convert/Revert and FinalizeInvoice commands. The focused durable re-run passed
in `financial-durable-attempt5.log` before the complete package regression run.
Initial non-DB checks are retained as `non-db-tests.log` and `vet.log`.

Full `make test`, browser/HTTP/API acceptance, upgrade and witnessed concurrency
proof remain coordinator/acceptance-owner work; the adapter package results do
not substitute for those gates. No commit, push or staging was performed.

### Independent-review follow-up

Two additional real-PostgreSQL race tests passed (exit 0, zero skips):

```sh
source /tmp/opencode/issue99-evidence-20261123/env.sh
export FREEFSM_TEST_DATABASE_URL="$ISSUE99_EXPORTS_DATABASE_URL"
export GOFLAGS=-p=2 GOMAXPROCS=2
go test -race ./internal/exports -run '^TestFinancialExport(CombinedSelectiveCreditExportRevocation|RunningWorkerInterruptionAndRecovery)$' -count=1 -timeout=10m -v
make lint
```

- `TestFinancialExportCombinedSelectiveCreditExportRevocation`: creates a
  nonempty Ready combined artifact, revokes **only** `credit.export` through
  SaveRole, verifies every other financial/Credit View/settlement grant remains,
  and proves whole-artifact status/download/recovery denial without sensitive
  metadata or bytes. A fresh independent settlement export still succeeds.
- `TestFinancialExportRunningWorkerInterruptionAndRecovery`: a test-local
  PostgreSQL trigger blocks the real worker at ordinal 2 after its header and
  first financial detail insert. The test witnesses committed `Running` plus
  the actual ungranted advisory lock in `pg_locks`, then cancels the worker's
  context (the installed shutdown/interruption path). It verifies Running remains
  durable and publication rolls back to zero records. A fresh Service/worker
  recovers that same receipt, publishes the complete combined CSV, commits exactly
  one generation audit, and never regenerates Ready work. This is witnessed
  in-process worker interruption/restart, not an OS-kill/reboot claim; no test
  fabricates Running with an UPDATE.

Logs: `/tmp/opencode/issue99-export-adapter-logs/financial-selective-restart-race.log`
(3.771 seconds) and `make-lint-followup.log` in the same directory. Together with
the preceding complete package run, **23 distinct top-level adapter-package
tests**, including **12 financial tests**, passed; eight package subtests passed,
with zero skips. The last two tests are follow-up runs, not a claimed single
23-test invocation. No production changes were needed for these review gaps.
Financial-specific witnessed authorization races at acceptance/generation/download
remain assigned to the acceptance owner.
