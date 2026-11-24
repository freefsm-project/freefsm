# Issue #99 — reconciliation and proposed implementation plan

Prepared 2026-11-23. **Plan approved with corrections; core A implementation underway.**

## Resolved policy / implementation authorization

The coordinator approved the architecture and resolved all six original questions.
This section and `v1-ticket-99-interfaces.md` supersede the historical proposals
below; there are no remaining product blockers.

- Aging/Customer financial Export uses `customer.financial.export`, compatible
  Invoice **View**, and View prerequisites; never Invoice Export.
- Settlement uses settlement View/Export only with prerequisites. Historical
  Credit creation/applications/refund allocations are retained provenance, with
  no current balances and no Credit Export requirement.
- Credit uses Credit View/Export only with prerequisites. Source Payment ID,
  funded amount, availability and truthful source dates are intrinsic provenance;
  no Payment method/reference/notes/actor/history and no settlement grant.
- Combined requires settlement plus Credit Export and both View chains only.
- Undated events are explicitly visible, including under bounded filters, in a
  separate unknown-entry section with authorized attribution notice/counts/amounts.
  They never enter dated totals. CSV timestamp is empty with an unknown marker.
- Retained source Invoice IDs need no ordinary Invoice View or navigation. Actual
  Invoice monetary contributors require current Invoice View. Never enrich
  inaccessible/hidden source IDs with titles or detail links.
- Narrow shared dashboard Net Payments correction approved; another owner will
  integrate it. Five export kinds need only two report pages: invoice-aging with
  invoices/customers views; payments-credits with settlements/credits/combined.
- CSV owner's `docs/v1-ticket-99-export-contracts.md` freezes keys, sorts, buckets,
  event kinds and persisted allocation identities; core interface aligns with it.
- Core A owns new service/authorization files and focused tests only. Exports,
  handlers/router/navigation/templates and dashboard belong to other owners.
- Coordinator provisioned `/tmp/opencode/issue99-evidence-20261123/env.sh` and gave
  core exclusive use of `$ISSUE99_FOCUSED_DATABASE_URL` on port 37141. Default
  `freefsm_issue99` is reserved for the full suite. No migration is planned.

## Provenance and isolation

- First write: `gh issue edit 99 --add-assignee @me` (succeeded).
- Verified branch and path absent, then created `implement/v1-99` at
  `3b7d770d801cff170bb85fafef0a0577692eec2f` in
  `/home/adam/repos/freefsm/.worktrees/v1-spec-18/ticket-99`.
- Read root instructions (the only AGENTS.md in this worktree), issue-tracker and
  domain instructions, CONTEXT, ADRs 0001/0002/0003, live #99 and #18.
- Live #53 and #96 are closed; both implementation branch tips are ancestors of
  this base (`git merge-base --is-ancestor` exited 0 for each). #94 and #97 are
  open/assigned siblings, not dependencies; do not compose their branches.
- Normative text captured outside the repository at
  `/tmp/opencode/issue99-ticket-20261123.json` and
  `/tmp/opencode/issue99-prd18-20261123.txt`.
- Future evidence root: `/tmp/opencode/issue99-evidence-20261123/`; use newly
  provisioned issue99-owned PostgreSQL cluster/databases and browser profiles.
  No issue98 evidence, worktree, runtime or database may be changed.
- No feature code, migration, test fixture, commit, push, stage, or tracker closure
  performed. This document is the sole worktree change.
- Coordinator supplied independent risk review from
  `ses_df717a449ffe9h42Fl1RrWuKhM`; its constraints are incorporated below.
  This session has no tool for spawning subagents. The proposed detailed output
  contracts and unresolved policy questions still await plan approval.

## Normative contract

DISC-04/05 define fixed reports and durable exports; DISC-02 explicitly attributes
Net Payments to recording/entry instants in company timezone, not effective dates.
MAS-02 defines authorized active issued positive-due receivables with separate
Credit. FIN-01–04 require immutable receipts, allocations, compensating events,
exact cents, deterministic FIFO and atomic settlement. PER-07 makes financial,
settlement-history and Credit View independent; PER-12 makes their Exports
independent of operational Customer Export. API-01 excludes office reporting and
exports from the public field API even for a broadly privileged bearer user.
DISC-06 and UX-04 require explicit failures/retry/retained filters, never false zero.
TEST-01–06 require real PostgreSQL, production-routed HTTP, browser and applicable
API/failure/concurrency verification, plus the required full checks.

ADRs agree on immutable relational history, not a new ledger. ADR-0003's effective
status precedence supersedes older simplified wording in CONTEXT. No status label
comparison or current projection may replace transaction history.

## Acceptance reconciliation — all eleven criteria

These are code-inspection findings, not new execution evidence or acceptance passes.

| AC | Existing behavior at exact base | Gap and planned proof |
|---|---|---|
| 1 Independent financial/settlement/Credit exports and two reports | `055_authorization_expansion.up.sql` already catalogs `customer.financial.export`, `settlement.export`, `credit.export` with compatible View prerequisites. `exports/catalog.go` registers operational Customer, activity/audit, time and #98 adapters only. | Add Customer financial, settlement, Credit, Invoice Aging and Payments/Credits report export surfaces with explicit grants. Verify each grant independently through real HTTP and CSV; operational Customer Export never gains finance columns or totals, including empty results and crafted POSTs. |
| 2 Aging population and boundaries | `authorization/customer_receivables.go` filters active/nonhidden non-Draft/non-Void Invoices and intersects A/J Invoice View; immutable active allocations determine settled cents. Handler computes six buckets and skips nonpositive due. | No fixed Aging report/export. Existing test covers 0/1/31/61/91 and missing date, not every boundary. Build shared snapshot selector and test future/today/1/30/31/60/61/90/91, missing/zero-sentinel dates, Draft/Void/Paid/zero/archived/hidden/inaccessible. Check calendar dates in non-UTC zones/DST without shifting SQL DATE to the previous day. |
| 3 Period event history/provenance | Immutable `invoice_payments`, `customer_credits`, `credit_applications`, `credit_refunds`, `credit_refund_allocations`, `settlement_reversals` exist. Customer receipt/refund pages retain originals and attached reversal information. | No period report/event stream. Emit original events plus separate compensating rows with original/source IDs; include Payment, refund, three reversal kinds, Payment-funded Credit creation, Credit Application. Preserve refund source allocations and ordering; no destructive netted replacement. |
| 4 Current Available Credit independent of period | `creditAvailableSQL` and `CustomerCreditTotal` calculate current sources less active consumers, excluding reversed origin Payments. | Combined report absent. Select current sources at the same snapshot but without activity date predicates. Display a single Available Credit measure, separate from activity totals; changing period preserves its value and sources. |
| 5 Recording-time Net Payments | `services/dashboard.go:paymentTotals` uses `received_date` and excludes reversed Payments entirely; no refunds/refund reversals. Payment `created_at` is nullable for preserved legacy unknowns. Other event `created_at` fields exist. | Replace formula with entry-time Payment minus Payment reversal minus refund plus refund reversal. Reuse with report calculations, including unapplied receipts; applications/Credit creation have zero cash effect. Test backdating, company today/month half-open bounds, 100−20+20=100 then source reversal=0, and reversals entered in later periods. Unknown entry times must never be fabricated or attributed via effective date. |
| 6 Receivables and archived Customer history | Customer receivables already ignore Credit and calculate due from monetary facts. Receipt/refund contexts use independent financial facets and parent archived access. | Reuse conformant semantics; ensure report and export history do not drop events by joining only active Customers/Invoices. Archived history explicitly authorized and read-only; no mutation or reopening. Receivables aggregate only authorized eligible Invoices before totals. |
| 7 Exact conservation/history/concurrency | Settlement transaction functions already implement allocation, selected-source application, FIFO refunds and immutable reversal guards. `refund_history.go` orders source allocations by `source_date,id`. | Add report-specific reconciliation tests against real operations for partial/full settlement/full unwind and concurrent writers. Reconcile cash, settled amounts, each Credit source and refund allocations in cents, not only row counts or stored `settlement_state`. Avoid event×allocation join multiplication. |
| 8 One snapshot/durable CSV/current whole-artifact authority | `exports.Definition`, `WithinSnapshotPermission`, worker, status and download support repeatable-read selection, atomic Ready, requester-only artifacts, 24h expiry, per-row reference recheck, integrity/spool verification and audit. | Add financial adapters/references, all matching rows and snapshot-derived totals. Recheck every contributing source/Invoice/Customer and required facet, including summary contributors. Extend authorized recovery to new kinds. Test revocation at acceptance/generation/status/download, archived/assignment changes, requester isolation, expiry and no subset/metadata leak. |
| 9 Commit-time grants/invariants/atomic audit | Existing authority fence is acquired before establishing snapshot; acceptance/publication/download audit is transactional. Settlement commands already have installed fenced write paths. | Use installed paths, not legacy Role-based read helpers. Prove new adapter checks execute within fenced transactions and rollback audit/publication failures. Race actual grant/assignment/archive DML, not mocked principal changes. |
| 10 Preservation/no invented history/grants | Existing identities and normalized relations suffice; all three new export grants already exist. Migrations 090/093 preserve unknown actor/time/metadata; 092 permits unapplied receipts. | Prefer no migration. If indexes are justified, additive only and no historical rewrites/grant backfill. Test genuine older-installation upgrade with retained legacy unknowns, financial events, grants and historical export bytes; compare identities/relations/data before/after. Legacy Credit migration timestamp is not evidence of receipt entry time. |
| 11 Required tests/checks | Makefile `lint` runs `go vet ./...`; `test` runs `go test -v -race ./...`. Direct real-DB/session/browser/API/fault patterns exist in #98, without a generalized harness. | New focused #99 tests plus `make lint`, explicit disposable-DB `make test`, generation/build and browser gate. Record actual results and all skips. Nothing has been run or claimed passing for #99 yet. |

## Architecture and proposed seam

### Existing modules to retain

- `internal/exports/{exports,worker,download,catalog,recovery}.go`: use the durable
  module unchanged in meaning; versioned kinds/column contracts are immutable.
  Snapshot receives a `pgx.Tx` and principal, emits CSV plus authorization references.
  Ready is 24 hours from `ready_at`, not acceptance. A lost reference blocks all bytes.
- `internal/authorization/authorization.go`: `WithinSnapshotPermission` acquires
  the authority fence before repeatable-read; `WithinActor`/`WithinPermission`
  provide commit fencing. Use `CheckParentTransaction` and current facet/scope
  checks, with explicit handling for historical provenance versus ordinary links.
- `internal/services/commercial_financial.go:FinancialTotals`: exact canonical
  financial snapshots plus existing legacy arithmetic. Do not round float totals.
- `internal/settlement/{settlement,customer_receipts,refund_history,applications}.go`:
  immutable facts and installed commands are source truth. No financial writes
  should be introduced by reports; test setup should use real installed commands.
- `internal/handlers/{work_reports,work_exports,work_report_filters}.go`,
  `internal/templates/work_reports.*`: useful route/review/error/recovery patterns.
  Reuse durable export receipt UI; give financial tables appropriate typed data.

### Proposed deep reporting module

Place authorized financial report selection in new focused
`internal/services/financial_reports*.go` files, with small entry points for Aging,
Customer financial summaries, settlement activity and current Credit. Return typed
rows, exact totals, as-of/timezone, normalized filters and required references.
Expose transaction-taking snapshot functions for export adapters; ordinary report
entry points acquire the installed snapshot fence then call the same functions.
Keep authority/reference checks in focused `authorization/financial_reports.go`
where needed. Final names/interfaces are review work, not an approved code contract.

This placement avoids a real import cycle: settlement already imports services
for `FinancialTotals`, so DashboardService cannot import settlement to obtain a
shared Net Payments query. Keep recording-time cash-event selection/calculation in
services, shared directly by financial reports and the existing dashboard. Avoid
a second generic reporting engine, ledger, command bus or test harness.

Independent risk-review constraints: existing financial/settlement/Credit Export
grants and their View prerequisites suffice; no new permission family is needed.
Do not call `settlement/query.go:CustomerMasterSettlement` for independent report
surfaces: it requires both Credit and settlement View and mixes their content.
Every snapshot query must use the supplied transaction, including timezone,
parent/facet checks, source selection and totals; calling an existing pool-backed
helper from a snapshot would silently destroy its consistency guarantee.

Snapshot rules:

1. Validate strict documented filters and explicit selected-record access before
   querying totals. Financial exports are A scope; Invoice row access may still
   be limited to assigned Jobs where compatible View allows it.
2. Read `transaction_timestamp()` and company timezone once. This is a current
   generation/read snapshot, not a user-requested reconstruction of past balances.
3. Select authorized records first; derive summaries from exactly that set.
   Calculate outstanding from canonical Invoice total and active immutable effects.
4. Select period events by their own entry timestamps. Original events remain
   visible even when later reversed; compensating rows point to originals.
   Current Available Credit query never receives the activity-period predicate.
5. Read refund allocations separately or preaggregate them before joining events.
   An event has one cash contribution regardless of source count. Source detail
   has its own amount and must never repeat an additive event total.
6. Attach original/current Customer/Invoice/source identities and required facets
   to every emitted row. Summary references cover all contributors, not only the
   Customer. Authority loss blocks the full stored artifact even if fresh selection
   would simply omit that row. Data changes alone do not rewrite frozen CSV bytes.
7. Unknown legacy metadata remains explicitly unknown. Query errors/iteration
   failures fail the report/export rather than publish zeros or partial output.

### Product output decisions (approved)

| Output | Suggested route | Fixed contents / authority |
|---|---|---|
| Invoice Aging | `/reports/invoice-aging` | Authorized positive-due Invoice rows; `customer.financial.export` + financial/Customer View + compatible Invoice View. No Invoice Export. |
| Customer financial | Aging page `view=customers` | Per-Customer eligible Invoice totals with every Invoice contributor reference; same grants as Aging, no Credit/history fields. |
| Settlement activity | `/reports/payments-credits`, `view=settlements` | `settlement.export` + View prerequisites; immutable events and persisted Credit allocations, no current Credit and no Credit Export. |
| Current Credit | Payments/Credits page `view=credits` | `credit.export` + View prerequisites; intrinsic source identity/amount/dates only, no settlement grant. |
| Payments/Credits | Payments/Credits page `view=combined` | Exact union under settlement and Credit View/Export, no additional financial Export. |

Do not invent new permissions where the existing catalog suffices. Fix each kind's
field set and required grants; do not silently emit a reduced field set under the
same versioned kind. Reports can present independently authorized sections, but
an accepted combined export is all-or-nothing. Customer operational CSV remains
the existing fixed ten-column contract.

Proposed common filters: optional Customer, inclusive company-local `from`/`to`
entry dates for activity only, explicit archived-history inclusion, stable sort;
Aging optionally bucket. Final filter names/defaults and output columns must be
frozen in `docs/v1-ticket-99-export-contracts.md` before adapter implementation.
Use stable event `(entered_at,event_type,event_id)` order with documented unknown
placement; source allocations ordered `(source_date,credit_id)`.

CSV contracts should contain `as_of`, timezone, stable IDs, effective date and
entry timestamp as distinct columns, exact two-decimal USD strings, event kind,
original/compensating/source relationships, and explicit row type where mixed.
Summary rows must be clearly typed and nonadditive with detail; never sum balances,
cash effects and Credit movement as one amount. Existing formula-leading-cell
escaping applies even to negative numeric strings; document reversible decoding.
Header-only empties must still enforce all required View/Export grants.

## Original review questions — resolved

1. **Export intersections:** resolved by the fixed grant table above. Existing
   catalog grants suffice; Aging does not need Invoice Export.
2. **Unknown historical recording time:** migration 090 intentionally permits
   `invoice_payments.created_at IS NULL`; migration 042 explicitly records
   `entry_time_unknown` in `settlement_legacy_evidence.review_issues`. Thus existing
   code establishes unknown provenance and preservation, and PRD DISC-02 rules out
   effective-date substitution. Neither inspected source establishes the precise
   report UI/filter policy for undated history. Coordinator resolved this: always
   retain an explicit separate unknown section/count/amount notice for bounded and
   unbounded views; unknown events never enter dated totals or receive invented dates.
3. **Credit creation historical time:** migration 042 generated legacy Credit with
   default creation time while preserving nullable original receipt time. Treat
   Payment-funded creation as sourced from receipt entry evidence, not migration
   timestamp. Preserve stored facts; report unknown when original entry is unknown.
4. **Historical Invoice references:** complete unwind followed by reversion hides
   the Invoice. Monetary history must survive, but ordinary Invoice access/link must
   not revive. Approved Customer-authorized retained financial provenance (stable
   source identity without ordinary hidden-document link), with exact reference
   checks. Current document permission loss must not be confused with intentional
   hidden provenance that was authorized at generation.
5. **Credit View versus source details:** existing Customer card intentionally
   shows aggregate Credit under `credit.view` without settlement-history details
   (`TestCustomerReceivablesAgingAndIndependentVisibilityHTTP`). Preserve that
   distinction. Approved intrinsic source Payment ID, funded/available amount and
   truthful source dates under Credit alone; no method/reference/actor/full history.
6. **Dashboard scope:** #99 explicitly names DISC-02 and recording-time Net Payments;
   approved the narrow shared formula correction and labels/tests here, while leaving
   unrelated dashboard widget work to #105. This prevents report/dashboard disagreement.

## Ownership-friendly implementation work units

Arrange parallel agents after plan review; no branch composition. Each owner edits
only assigned files in this worktree. Agree typed snapshot/reference contracts first.

| Owner / work unit | Exclusive primary files | Dependencies / deliverable |
|---|---|---|
| A: financial facts and authorization | New `services/financial_reports*.go`, `authorization/financial_reports.go` and focused DB tests | Shared filter/row/reference contracts first; Aging, event stream, Credit, exact totals and scoped selection/recheck. Own any justified additive migration and upgrade fixture. |
| B: durable adapters | New `exports/financial_*.go`, adapter tests; `exports/catalog.go`, `exports/recovery.go` | Depends on A's contract; five fixed versioned exports, atomic snapshot publication, complete references, recovery. Own CSV contract document. |
| C: browser routes and presentation | New `handlers/financial_reports.go`, `financial_report_filters.go`, `financial_exports.go`; financial templates; `router.go`, `exports.go`, `work_exports.go` recovery dispatch, navigation | Depends on A/B contract; independent visibility, confirmation/current filters, durable receipt/recovery, explicit failures. Own generated templ output. Coordinate any reused report-template changes. |
| D: dashboard and Customer consistency | `services/dashboard.go`, `handlers/customer_receivables.go`, associated tests/templates | Wait for A's shared selectors; recording-time Net Payments and shared calendar/receivable semantics. Keep changes narrow to #99; coordinate existing authorization Customer selector edits with A. |
| E: acceptance proof | New `handlers/financial_reports*_test.go`, `financial_exports*_test.go`, `testdata/browser/financial-reports*.cjs`, evidence document | Independent fixtures via existing direct real-DB/session setup; HTTP/browser/API, source conservation, witnessed contention/failures/upgrade. No generalized harness. |
| Coordinator | This reconciliation, final integration inventory/check logs | Review contracts and ambiguity resolutions, organize independent review, execute final required gates, record exact results/skips. |

## Focused verification matrix

- **Population/cents:** every aging edge, leap/DST/company timezone, optional due
  date, Draft/Void/Paid/zero/archived/hidden exclusions, A/J Invoice visibility,
  free-text/custom-status labels, partial/full/unwind, exact cents and large totals.
- **Events:** unapplied receipt and overpayment; multiple Credit sources with
  deliberately different effective and entry order; partial/multi-source FIFO
  refund; application/refund/Payment reversal; old receipt/current reversal;
  100/20/20/100 example. Inspect original immutable IDs and per-source allocations.
- **Period/current:** today/current-month local bounds, backdated entered-today,
  period changing only activity, archived Customer history, legacy unknown dates,
  no double count through event/source join or a second unapplied balance.
- **Grants:** separate View and Export toggles, operational-only, Credit-only,
  settlement-only, absent Invoice scope, selected unauthorized Customer, archived
  grants, whole-artifact contributor authority loss, all grants checked on empties.
- **Durable behavior:** >100 matching rows (not page-only), sorted filtered rows,
  totals/as-of agreement, quoted/newline/formula input, requester isolation,
  Pending/Running/Ready/Failed, actual worker restart, exactly 24h expiry, authorized
  filter-preserving regenerate and redacted recovery after authority loss.
- **Concurrency:** actual settlement commands against same Invoice/source/refund,
  overlapping report/CSV snapshot; assert one coherent before or after state.
  Witness actual revocation/assignment/archive DML waiting at acceptance,
  generation and download fences. Do not rely on sleeps as race evidence.
- **Failure:** DB read/iteration errors after a first row, audit insertion failure,
  CSV publication/storage failure, transfer-spool failure/integrity corruption;
  no partial artifact, partial audit or sensitive status metadata; HTTP error with
  filters/retry and no false zero. Preserve actual failure logs.
- **Browser:** persisted Chromium session with real PostgreSQL and export worker;
  five review/confirm/status/download journeys, event/source links, period change,
  current Credit independence, denied controls and direct requests, reload/recovery;
  narrow viewport and keyboard access. Browser close/reopen proves durable receipts.
- **Field API:** real authenticated bearer through `api/v1.NewRouter` and office
  router, GET/POST finance report/export paths denied even for Administrator;
  no export jobs accepted or finance data exposed. This is an office-only feature,
  not a new field API endpoint.
- **Upgrade:** actual older-schema fixture through migration API, retained nullable
  actor/time evidence and legacy FinancialTotals, immutable originals/refunds/
  allocations/reversals and prior CSV bytes, identities, role grants unchanged.

## Required commands and environment plan

Commands below are **planned**, not executed results. Provision a fresh UTF8
PostgreSQL cluster with an unused port under the new evidence root; non-superuser
`issue99_app` owns `freefsm_issue99` and a separate `freefsm_issue99_focused`.
Use a distinct issue99 cluster admin only for installation/backup fixtures. Check
available binaries/ports before provisioning; do not reuse issue98 env.sh or DB.
`go`, `node`, `psql`, `initdb`, `pg_ctl` are on PATH; Chromium was not found on PATH,
so discover/install the available browser executable and Playwright dependency
without editing shared runtime configuration. Browser scripts need `TMPDIR`,
`NODE_PATH` where necessary and `BROWSER_EXECUTABLE`.

```sh
# After issue99-only env.sh provisioning; replace PORT with the checked free port.
export FREEFSM_TEST_DATABASE_URL='postgresql://issue99_app@127.0.0.1:PORT/freefsm_issue99?sslmode=disable'
export FREEFSM_INSTALLATION_TEST_ADMIN_URL='postgresql://issue99_admin@127.0.0.1:PORT/postgres?sslmode=disable'
export FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://issue99_admin@127.0.0.1:PORT/postgres?sslmode=disable'
GOFLAGS=-p=2 make generate
GOFLAGS=-p=2 make lint
GOFLAGS=-p=2 make BUILD_DIR=/tmp/opencode/issue99-evidence-20261123/build compile
FREEFSM_TEST_DATABASE_URL='postgresql://issue99_app@127.0.0.1:PORT/freefsm_issue99?sslmode=disable' BROWSER_TESTS=0 GOFLAGS='-p=2 -count=1 -timeout=120m' make test
FREEFSM_TEST_DATABASE_URL='postgresql://issue99_app@127.0.0.1:PORT/freefsm_issue99_focused?sslmode=disable' BROWSER_TESTS=1 GOFLAGS=-p=2 go test -race ./internal/handlers -run '^Test(FinancialReport|FinancialExport|InvoiceAging|PaymentsCredits)' -count=1 -timeout=30m -v
git diff --check
```

Also run focused real-DB service/authorization/export tests and affected Customer/
dashboard/settlement regressions. Align final focused regex with actual test names
and verify a nonzero count with zero applicable skips; do not silently miss tests.
Full `make test` already enables the race detector. Record full-suite opt-in skips
separately, and explicitly run every #99 browser test with `BROWSER_TESTS=1`.
Older evidence suggests the full suite may take over an hour; retain all failed
attempts and fix only evidenced regressions rather than weaken checks.

## Current handoff status

Independent risk review incorporated: grant reuse, independent readers, exact
recording-time events, transaction-only snapshots, contributor references,
whole-download revocation, undated legacy history and all requested test risks.
The plan and resolved policies are approved. Core A has implemented transaction-only
selectors/types, facet-specific references and shared recording-time cash formula.
Adapters/UI/dashboard/HTTP-browser acceptance are separately owned and underway.
Core execution results are recorded below; full required gates and independent
implementation review remain coordinator-owned.

Preparation check: `git diff --check` exited 0; `git status --short` showed only
this new document. Required feature quality/acceptance commands remain pending.

## Core A implementation and checks (2026-11-23)

Implemented new `authorization/financial_reports.go`,
`services/{financial_reports,financial_receivables,financial_activity,financial_credit}.go`,
`authorization/financial_reports_integration_test.go` and
`services/financial_reports_test.go`. No exports/handlers/dashboard files edited
by A, no migration, no historical facts or permissions rewritten. Interface and
CSV contracts agree on filters/buckets/order/event/retained allocation identities.

All commands sourced `/tmp/opencode/issue99-evidence-20261123/env.sh`; every DB
test explicitly used `FREEFSM_TEST_DATABASE_URL="$ISSUE99_FOCUSED_DATABASE_URL"`,
which resolves to issue99_app at 127.0.0.1:37141/freefsm_issue99_focused.
Default/full-suite database and issue98 runtime/evidence were untouched.

| Command | Result / evidence under issue99 `logs/` |
|---|---|
| `go build ./internal/services ./internal/authorization` | Exit 0, `core-A-interface-build.log` |
| `FREEFSM_TEST_DATABASE_URL="$ISSUE99_FOCUSED_DATABASE_URL" make lint` | Exit 0, `core-A-make-lint.log` |
| `FREEFSM_TEST_DATABASE_URL="$ISSUE99_FOCUSED_DATABASE_URL" go test -race ./internal/authorization ./internal/services -run '^TestFinancialReport' -count=1 -timeout=10m -v` | Exit 0, nine tests passed/zero skips, `core-A-final-focused-rerun.log`; authorization 10.035s, services 1.024s |

DB cases exercise installed real commands and immutable records: cents-exact FIFO
over two sources, partial refund/full unwind, Application compensation with original
allocation IDs, independent grants without unrelated Export facets, assigned Invoice
contributors and whole-summary reference denial after assignment loss, unknown
legacy entry evidence and local-day bounds, repeatable-read source changes committed
between reads, aging eligibility/amount totals and overflow failure returning no
partial report. Supporting tests exercise every aging edge and 23/25-hour dates.

Retained failures: initial test compile encountered dashboard owner's temporary
services→database→services cycle (owner repaired it); first DB fixture omitted web
audit channel and used nullable due date despite NOT NULL schema; second used an
invalid Payment reversal reason (fixed to canonical `error`). A malformed-Invoice
fault setup was correctly rejected by installed DB guards; replaced with two valid
receipts whose aggregate overflows exact int64 cents, proving no partial report
without bypassing guards. Logs preserve all attempts. These were fixture corrections,
not weakened production constraints or skipped acceptance.

Core A released the focused database after the successful final gate. Full
`make test`, persisted browser/HTTP/API routes, durable adapter failures/concurrency
and final independent implementation review remain separately owned; no whole-issue
acceptance is claimed here.

### Independent core review follow-up

Review reported no confirmed production defect and a medium-strength test issue:
conditional event assertions could pass if their target disappeared. Fixed with
`financeEvent`, requiring exactly one matching **ID and kind** before asserting
original provenance and persisted allocations for refund/refund reversal, Payment,
Application and Application reversal. Application reversal now retains the returned
effect ID rather than matching any event of that kind.

Added four real-DB tests in `authorization/financial_reference_history_test.go`:

- `TestFinancialReportOriginalAndCurrentJobReferenceTransitions`: original/current
  Job assignment intersection, movement to a newly authorized current Job, old
  artifact denial after original assignment loss while fresh references remain
  valid, original Customer mismatch, and current unlink denial. Installed DB guards
  reject changing the Customer of a currently linked finalized Invoice; test proves
  that rejection instead of fabricating an impossible mismatch.
- `TestFinancialReportArchivedCustomerHistoryAndRestoredReferences`: actual reviewed
  Customer archive after receipt unwind, default current exclusion, explicit archived
  history and archived-grant enforcement, no report-triggered reopen/new events,
  actual restore, and persisted archived-reference requirements after restoration.
- `TestFinancialReportHiddenInvoiceHistoryRetainsOnlySourceIdentity`: real conversion,
  authorized domain finalization, receipt and full reversal, paid Invoice archive/
  restore through installed DB lifecycle guards, and real conversion reversion.
  Settlement-only reader retains exact immutable source IDs/events/references without
  Invoice View or hidden title enrichment; ordinary hidden Invoice access is denied.
- `TestFinancialReportReferencesRejectWrongCustomerCompanyAndSource`: reject wrong
  Customer/foreign company, wrong Payment/Credit/Invoice IDs, wrong event kind and
  missing source, including explicit foreign Customer selection; foreign values
  cannot affect authorized rows or totals.

Commands sourced the issue99 environment and explicitly selected the focused DB:

| Command | Result / log |
|---|---|
| `FREEFSM_TEST_DATABASE_URL="$ISSUE99_FOCUSED_DATABASE_URL" go test -race ./internal/authorization ./internal/services -run '^TestFinancialReport' -count=1 -timeout=10m -v` | Exit 0, **13 tests**, zero skips (11 real DB + 2 unit); `core-A-review-history-second.log`, authorization 14.626s / services 1.023s |
| `FREEFSM_TEST_DATABASE_URL="$ISSUE99_FOCUSED_DATABASE_URL" go test -race ./internal/authorization -run '^TestFinancialReportImmutableCashFIFOAndPeriodIndependence$' -count=1 -timeout=5m -v` | Exit 0 after also tightening the original-refund match; `core-A-review-strict-refund.log` |
| `FREEFSM_TEST_DATABASE_URL="$ISSUE99_FOCUSED_DATABASE_URL" make lint` | Exit 0, `core-A-review-make-lint.log` |

First review-fixture attempt is retained in `core-A-review-history-first.log`: the
installed Job Customer guard correctly rejected a finalized-Invoice mismatch, and
the domain finalizer was initially called with web context but no form revision.
The corrected test asserts the actual guard and uses the authorized domain entry
point without claiming HTTP form coverage. No constraints were bypassed and no
production code changed in this follow-up. Focused DB is released again. Upgrade,
competing-consumer concurrency and after-first-row query/iteration fault acceptance
remain separately owned; this follow-up does not claim that broader coverage.
