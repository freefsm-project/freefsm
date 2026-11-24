# Issue 99 core interface contract

Approved policies from coordinator, 2026-11-23. Core owner A owns only new
`internal/services/financial_*.go`, new `internal/authorization/financial_reports.go`,
their focused tests, and these planning/interface documents. Other owners own
exports, handlers, templates and dashboard integration.

## Authorization (`internal/authorization`)

`FinancialReportKind` string constants: `FinancialAging` (`invoice-aging`),
`FinancialCustomers` (`customer-financial`), `FinancialSettlement` (`settlement`),
`FinancialCredit` (`credit`), `FinancialCombined` (`payments-credits`).

- `FinancialReportPermission(kind, exporting)` returns the primary fence permission.
- `AuthorizeFinancialReport(p, kind, exporting)` checks **all** fixed-column grants.
- `CheckFinancialSelection(ctx, tx, p, kind, customerID, includeArchived, exporting)`
  checks the fixed grants and selected Customer even when output would be empty.
- `CheckFinancialReference(ctx, tx, p, ref)` checks current **View** authority and
  all retained row/source anchors. Adapters also call `AuthorizeFinancialReport`
  with `exporting=true`; a reference is not a substitute for current Export grants.

All primary permission scopes are `authorization.All`. Aging and Customer financial
require `customer.financial.view`/`customer.financial.export`, Customer View and
Invoice View for each included contributor; **never Invoice Export**. An A finance
grant does not widen J Invoice View. Settlement uses only `settlement.view/export`
and prerequisites; historical Credit creation/applications/refund allocations are
settlement provenance, not current Credit. Credit uses only `credit.view/export`
and prerequisites; source Payment ID, funded amount and source date are intrinsic
provenance, not authority to show Payment method/reference/actor/notes/history.
Combined requires both settlement and Credit facets/exports, not financial Export.

`FinancialReference` binds one Customer and a facet-specific set of Invoice,
event or Credit identities. Summary rows retain all Invoice contributors in their
reference. Report-wide aggregate rows must serialize `report.References` and
recheck **every** member; never merely check Customer authority. Stored archive
visibility is retained in references and current archived state also rechecked.
Invoice refs retain original Job anchors and check current anchors. History refs
check immutable event ownership; they do not require ordinary source Invoice View.
No history/Credit row enriches an inaccessible source Invoice with title or link.

## Reporting (`internal/services`)

`FinancialReportFilter`: `CustomerID int64`, `From, To string` (inclusive company
dates; activity kinds only), `IncludeArchived bool`, `Bucket string` (Aging only),
`Sort string` (canonical stable sort below). JSON names are `customer_id`, `from`,
`to`, `include_archived`, `bucket`, `sort`.

- `NormalizeFinancialReportFilter(kind, filter)` validates/canonicalizes.
- `ReadFinancialReport(ctx, pool, actor, kind, filter)` opens a fenced repeatable-read
  transaction; office-only (API actor rejected).
- `FinancialReportSnapshot(ctx, tx, principal, kind, filter)` uses **only this tx**
  and returns `FinancialReport`. Export adapters call this inside the export engine's
  transaction after export authorization. No nested transaction or pool query.
- `RecordedNetPayments(ctx, queryer, companyID, from, until)` returns exact signed
  cents using half-open entry-time bounds. It includes unapplied receipts and
  archived history; authorization belongs to the calling dashboard/report module.
  Caller must use a tx when combining with other snapshot measures. The same cash
  event relation is used by report selection; no duplicate formula.

`FinancialReport` contains `Kind`, normalized `Filter`, `AsOf`, `Timezone`,
`Invoices []FinancialInvoiceRow`, `Customers []FinancialCustomerRow`,
`Activity []FinancialActivityRow`, `UnknownActivity []FinancialActivityRow`,
`Credits []FinancialCreditRow`, `Totals FinancialReportTotals`, and
`References []authorization.FinancialReference` for all contributing rows.
Each row carries its own `Reference`. Nonapplicable slices remain empty.

Totals are exact int64 cents; overflow fails rather than wraps. Names:
`TotalCents`, `SettledCents`, `ReceivableCents`, `InvoiceCount`, `Buckets [6]int64`,
`BucketTotals [6]FinancialAgingTotal` (InvoiceCount, TotalCents, SettledCents,
ReceivableCents), `NetPaymentsCents`,
`AvailableCreditCents`, `UnknownEventCount`, `UnknownAmountCents`,
`UnknownNetPaymentsCents`. UnknownAmount is the sum of event face amounts, explicitly
**noncash/nonbalance**; unknown Net Payments is signed unattributed cash, never a
dated-total component. Do not add any of these distinct measures together.

### Rows

- Invoice: ID, JobID, CustomerID, InvoiceNumber (numeric sort); Customer, Number
  (display label), Title; DueDate (ISO date or
  empty); TotalCents, SettledCents, DueCents; Bucket, Reference.
- Customer: ID, Name, Archived; InvoiceCount, TotalCents, SettledCents,
  ReceivableCents, Buckets [6]int64;
  Reference with every contributing Invoice. Includes authorized selected active
  Customers with zero receivables, without counting excluded Invoices.
- Activity: Kind, ID, OriginalKind, OriginalID, CustomerID, Customer, Archived;
  PaymentID, CreditID, InvoiceID (retained identities only); RecordedAt *time.Time,
  EffectiveDate string, AmountCents, NetPaymentsCents; `Allocations
  []FinancialSourceAllocation` with Kind, ID (persisted allocation identity),
  InvoiceID, CreditID, PaymentID, SourceDate and signed AmountCents;
  Reference. No current Credit balance, source Invoice title/link or method/notes.
- Credit: ID, PaymentID, CustomerID, Customer, Archived, SourceDate, RecordedAt;
  OriginalCents, AvailableCents; Reference. Includes only positive-current sources;
  historical exhausted/reversed sources belong in activity, not a second balance.

Activity kinds: `payment`, `payment_reversal`, `credit_refund`, `refund_reversal`,
`credit_creation`, `credit_application`, `application_reversal`. Original rows stay
immutable and present after reversal. Reversal rows have positive face AmountCents
and signed NetPaymentsCents (-Payment, -refund, +refund reversal; other kinds zero).
One event carries one cash contribution; allocation children never repeat it.
Allocation kinds/IDs follow the CSV contract: payment_invoice/Payment ID,
credit_application/Application ID, refund_credit/Refund ID + Credit ID. Reversal
allocations reuse persisted IDs with negative amounts; Credit creation has no
invented allocation or OriginalID (its PaymentID carries source provenance).
Credit creation recording time comes from original Payment entry evidence, never
the migration-generated Credit timestamp. SourceDate remains an effective date.

Unknown-time events are always separately available in `UnknownActivity`, even
with bounded dates, and excluded from `Activity`/dated totals. UI shows a clearly
labelled section and attribution notice; CSV emits them with empty timestamp and
an explicit unknown marker. Unbounded output includes both arrays; it must not
silently omit the unknown section. No effective-date or migration-time fallback.

### Filters/order/clock

Activity inclusive date bounds become half-open local midnights (AddDate, not
24-hour duration). All current Credit is independent of those bounds. Archived
history is explicit and needs Customer archived View; hidden/archived source
Invoices do not erase Customer-authorized financial events.

Canonical sorts: Aging `customer_invoice`; Customer `customer`; settlement/combined
`entry_asc`; Credit `customer_source`. Empty sorts normalize to these values; other
values reject. Event ties: recording instant, kind, ID. Unknown: kind, ID. Credit
order: lowercase Customer name, Customer ID, source date, Credit ID. Refund source
order: source date, Credit ID. Customer: lowercase name, ID. Aging follows Customer
order then numeric Invoice number, Invoice ID.
Aging buckets: `not_yet_due`, `1_30`, `31_60`, `61_90`, `91_plus`, `no_due_date`; indexes
0..5 respectively. Labels: Not yet due, 1–30, 31–60, 61–90, 91+, No due date.

AsOf/timezone are read once from the provided tx. SQL DATE values stay calendar
dates; never convert their UTC-midnight scan representation into company timezone.
Due today/future is current. Monetary reports are current snapshots, not historical
balance reconstruction. Fixed CSV columns/kind version names belong to adapter B.

## Adapter/UI obligations

Five export kinds share the module; five separate report pages are not required.
Check fixed grants at acceptance/generation/status/download including empty output.
Use each row's reference; aggregate references include every contributor. Recheck
is all-or-nothing. Do not suppress unknowns, multiply event amounts by allocations,
derive current Credit from filtered activity, or bypass the supplied transaction.
Use existing durable Pending/Running/Ready/Failed, 24h requester-only lifetime,
full-artifact blocking and authorized recovery. Core returns errors without partial
usable report; handlers retain filters and show failure rather than false zero.

This is the implementation contract; actual execution evidence will be added to
the reconciliation document after core tests. No new migrations are planned.

### Integration milestone

Core production types/selectors/reference checks are now defined and buildable.
CSV contract alignment: exact canonical sort/bucket/event keys, numeric
InvoiceNumber, full/bucket monetary totals, persisted Payment/Application/Refund
allocation identities with signed reversals, Credit RecordedAt from source Payment.
Types are authoritative in `financial_reports.go`; adapters may now integrate.
Ordinary links are safe only for `Invoices` and authorized Customers; activity
InvoiceID and source identities are provenance only, never detail-link eligibility.
The two-page handler arrangement maps view choices to the five kind constants;
call `AuthorizeFinancialReport` for choosing eligible views and `ReadFinancialReport`
for current fenced selection, rather than reproducing grants or SQL in handlers.

Core A implementation and focused proof are complete: seven migrated real-DB tests
plus two calendar/arithmetic tests passed under `-race`, zero skips. `make lint`
and production `go build ./internal/services ./internal/authorization` passed.
The dashboard owner repaired the temporary services/database test import cycle;
no owner files were altered here. Source/consumer allocation facts, partial/full
unwind, exact-cents FIFO, period-independent current Credit, unknown recording
time, A/J authority contributor loss, independent facets, API actor exclusion,
provided-tx MVCC, DST/bucket edges and failure-without-partial-output are covered.

Final focused log: `core-A-final-focused-rerun.log` (authorization 10.035s,
services 1.024s); quality log: `core-A-make-lint.log`, both under issue99 evidence
`logs/`. Earlier compile/fixture failures are retained, not claimed as passes.
The focused DB is released for other owners; core has no running DB test process.
Full-suite and HTTP/browser/durable-export acceptance remain coordinator/other-owner
work; this milestone claims only the core checks actually executed.
