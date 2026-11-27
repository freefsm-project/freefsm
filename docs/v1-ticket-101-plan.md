# Ticket #101 — Lease report exports plan

Status: planning only; no implementation or acceptance claim. Baseline: `implement/v1-101`, `e703bc55a0ffbfe890b8f20fb37f4afae9c886a5`.

## Approved interface refinement

Coordinator accepted both versioned operational/Billing contracts and GET/POST `/leases/report/export`. Independent review approved the architecture with the refinements below. **[Interfaces](v1-ticket-101-interfaces.md) is the authoritative implementation contract and ownership handoff** wherever earlier exploratory detail below differs. No product blocker remains; runtime agent is preparing PostgreSQL.

- One selected-date agreement position drives lifecycle, coverage, rent, ending predicates and periods. Corrected history, inclusive termination and scheduled cancellation agree across all outputs. Missing imported termination facts yield unavailable historical position, not current Terminated projected backward. Unprovable filter membership fails actionably; dependent amounts/totals are unavailable, never zero.
- Past worker failure history is unavailable because owning failure rows are overwritten/deleted. Current unresolved failure uses owning semantics, not the pending-message attempts table. Historical pending/failed filters are rejected actionably; historical retained-generation absence is `ungenerated`, never asserted pending/no failure. Candidate dates are bounded by the 30-day generation window plus relevant retained past facts, not indefinite future expansion.
- Required operational dates/ending range suffice: no new operational queue selector. Billing adds only pending/failed predicates. Positive review/void keeps compatible Assigned Invoice/Pricing access; negative generation queues require complete Invoice/Pricing/archive authority.
- Explicit operational mode does not invoke financial projection, even with Billing grants. Capture one transaction timestamp/company date/timezone. Freeze exact 27/59 ordered columns, stable revision/line keys, obligation-revision linkage, unavailable versus blank/zero, and child order per interfaces; engine row_count differs from matching_lease_count.
- A may narrowly extract shared entitlement helpers in `lease_coverage.go`/`lease_billing_worker.go`. C owns registry/recovery wiring previously reserved to coordinator; B does not edit those files. D owns named `TestLeaseExport...` acceptance cases listed in interfaces.
- Final sessions/ownership: A `ses_decb0c5bfffe7W5QOF98X16H3c` authorization and narrow billing helper; B `ses_decb0016affeWFnSIOFH1ZPMk3` adapter/reference files and adapter tests; C `ses_decb02b61ffe1vTq5TK5ktQYXX` handlers/UI/catalog/recovery/public docs; D `ses_decb306caffet4tBvZs9jPNCPm` all handler `lease_exports_*test.go` AND browser script. Interfaces contains the exclusive final list.
- Final review correction: fresh temporal validation runs only for new acceptance/generation/review; Ready status/download recheck grants, selected-resource authority and captured refs without re-evaluating today's date/queues. An explicit/empty-as-of Ready pending/failed CSV survives company midnight until Ready+24h unless authority is lost. Interfaces specifies the narrow optional `ValidateAcceptance` hook; C owns its invocation in `internal/exports/exports.go`, B its adapter method, A fresh validation. Recovery may require revised-date review but cannot invalidate Ready bytes. D adds `TestLeaseExportReadyAcrossCompanyMidnight`.
- Current queues include nonarchived finalized Scheduled initial charges and Terminated retained owed periods subject to shared entitlement/cancellation, not only Active lifecycle. Retained owed periods outside selected-date +30 use `not_yet_eligible`, distinct from unavailable historical retry status; bounded enumeration is unchanged.

## Authority and scope

Read root `AGENTS.md`, `docs/agents/domain.md`, `CONTEXT.md`, ADRs 0001–0003, issue #101 and parent #18. Normative requirements: LEA-02–06, PER-10, DISC-04–05, plus AUTH-02–05 and TEST-01–06 where applicable. ADR 0001 forbids a parallel settlement ledger; ADRs 0002–0003 preserve Invoice identities and distinguish manual and effective statuses. This plan introduces no ADR conflict.

#74 (Lease position report) and #96 (durable CSV workflow) are closed and their implementations are present. #94 (restore sessions/mail) and #97 (master-record exports) remain open and are absent from the supplied base: neither is a dependency or deliverable here. Use the existing export service, authorization fences, report projection, settlement facts, browser fixtures and production routes. Do not resurrect the retired generalized qualification harness or require its artifacts as proof.

Keep Lease coverage, Asset Placement, Lease Billing terms, generated Invoice snapshots, and ordinary Invoice settlement distinct. Exporting reports must not generate/adjust/void Invoices, move Assets, create coverage, or infer historical facts missing from storage.

## Findings and intended design

### 1. Share the existing report transaction, not paginated HTTP reads

`internal/authorization/lease_report.go` currently opens its own `WithinSnapshotPermission` transaction and collects only a page, although totals are evaluated over all matching Leases. Extract its transaction-consuming projection so both the browser report and a new export snapshot entry point use the same selection and financial calculations. Follow `authorization.TimeExportSnapshot`, not repeated calls to `Service.LeaseReport`: repeated pages would mix snapshots and truncate or duplicate results under concurrent writes.

Keep the public browser report paginated at 25/50/100. Add an internal all-matching iteration path, with stable `document_number,id` ordering (both reversed for descending), that emits complete Lease rows and contributor references without a magic page-size limit. Domain authorization belongs in `authorization`; CSV encoding belongs in `exports`. Use the export engine's supplied `pgx.Tx` and Principal exclusively; no pool reads or nested service transactions during snapshot generation.

Preserve the documented date contract: an explicit company date is an effective position using corrected domain history, not a reconstruction of everything known at that recording time. Current defaults resolve once inside the generation snapshot; record both selected company date and generation instant/timezone. A review explains that acceptance queues work and generation captures the consistent snapshot. Generation/status/adjustment recording cutoffs and effective settlement dates keep their existing distinct meanings. Future rent schedules must be labelled, never added to selected-date totals.

### 2. Close report-specific projection gaps

Evidence and AC mapping are in `v1-ticket-101-reconciliation.md`. Extend the existing projection only where necessary:

- Ending range (`ending_from`, `ending_through`), validated as ordered ISO company dates, using inclusive termination when present and fixed-term end otherwise; clearly distinguish those two dates in output.
- Operational dates expose starts, entering Month-to-Month and ending, alongside existing lifecycle/placement filters and ending range. First monthly transition is fixed-term end + one day; current renewal dates retain calendar-anchor clamping. No extra operational queue selection is required.
- Authorized generation-failure/manual-review selection alongside existing void/ungenerated conditions. Preserve complete Invoice/Pricing/archived-Invoice authority for negative generation knowledge; hidden generation must never decide a visible queue, count or rank. Include pending generation inside the 30-day pre-due window with the existing owning billing eligibility logic; distinguish not-yet-eligible, pending, failed, generated, void and not-owed facts rather than labelling all absence as failure.
- Billing output includes per-Asset effective rent/tax and retained before/after Billing revision provenance (effective date, recording time, reason, actor only when independently visible). Existing `RentSchedule` contains aggregate rent only; `LeaseReportInvoice` exposes only recorded total, not captured tax/line provenance. Read retained commercial generation snapshots for initial/rent/charge/tax fields; never reconstruct historical generated amounts from today's terms.
- Preserve existing adjustments, Invoice status and settlement provenance. Extend the browser report's relevant detail presentation alongside export so identical selections have consistent meaning. No new Lease balance, legal assessment, automated adjustment, or report widget.

Recheck inherited edge cases with fixtures before changing them: imported terminated agreements with incomplete retained dates; factual notice correction/withdrawal; future coverage; scheduled Lease cancellation; historical dates after a later notice decision. If a required historic detail was never retained, show an explicit unavailable fact rather than fabricate it. Changes to owning Lease writers are justified only by an evidenced blocker to #101's report correctness.

### 3. Versioned fixed CSV contracts

Proposed local contracts: `lease-operational-A-v1` and `lease-billing-A-v1`, both using `lease.export` All Records plus `lease.view` All Records. Billing additionally requires `lease.billing.view` All Records. A Billing-authorized reader can deliberately choose the operational export: it contains no financial columns or financial predicates. Confirm these contract names and the fixed-column presentation with the coordinator before parallel implementation; the requirements do not mandate names or row layout.

Prefer a fixed `row_type` schema per contract over serializing an unrestricted Go struct or multiplying parent amounts across child rows:

- Operational kinds: `summary`, `lease`, `coverage`. Columns cover snapshot/company date/timezone; Lease ID/Number/title/lifecycle/archive; authorized Customer/premises identifiers and labels; commencement/fixed end/inclusive termination/first monthly transition/current renewal dates; covered Asset identity and coverage start/end; placement state; matching Lease count only on summary. Output distinct `unknown`, `unplaced`, `wrong_premises`, and placed states; do not flatten unknown to unplaced.
- Billing kinds: `summary`, `lease`, `rent`, `billing_revision`, `period`, `invoice`, `invoice_asset`, `settlement`, `adjustment`. Fixed columns cover the common identity/date fields and typed amount/provenance fields, including original generated total/tax versus current selected-date rent, scheduled rent, period kind/due/state, generation identity/time, manual/effective Invoice status, review flag, immutable revision before/after values and reason, settlement kind/identity/effective/reversal date, and adjustment kind/target/dates/amount/reason. Where multiple revision lines exist, emit one row per line; do not expose inaccessible Asset or actor IDs inside nested payloads.
- A summary row counts unique matching Leases and sums selected-date in-force rent once per Lease. Scheduled and future rent, Invoice totals and settlements are not added together. If additional totals are provided, each has its own documented unit/population; avoid accidental repeated child-row aggregation.
- Every blank/unavailable field has a documented meaning, especially an unauthorized related facet versus numeric zero. No File columns or File links are required by the existing report; do not add attachment export. If any link is added, independently authorize and reference its actual File/Invoice resource.

Finalize exact ordered column arrays in a small contract/documentation change before adapters and browser tests diverge. Freeze versioned kind/column/reference semantics once implemented. Retain engine CSV escaping, Unicode/newlines, exact cents formatting, and digest verification. Document contracts in `docs/lease-csv-exports.md` and link from `docs/lease-position-report.md`.

### 4. Whole-artifact authorization

Add a Lease export authorization/reference boundary in `internal/authorization/lease_exports.go`. Adapter options strictly decode allowlisted normalized filters; reject unknown/malformed values. Explicitly selecting Billing mode or any Billing filter without Billing authority fails before querying. Selected Customer/Asset/premises filters must validate independent current record access inside the fence even for zero matching rows (`AuthorizeSelection`). Export permission never implies related View, Pricing, Settlement, File, or archived access.

Private versioned references capture every facet actually disclosed or used for selected membership/totals: Lease ID and archived requirement; Billing mode; Customer/premises/Asset identities and archive visibility; Invoice identity and original Job/scope anchors where Assigned authority was used; Pricing and Settlement facets; independently disclosed Product User identity; complete-generation prerequisite when a negative predicate or queue requires it. Historical associations need references to the originally disclosed related record, not merely today's Lease relationship. References for summary contributors must cover all contributors even when no detail field seems sensitive. Empty-result selections still carry their selected-input and facet prerequisites.

Recheck both original authorization anchors and current resource eligibility. Do not recompute or silently filter the prepared bytes: any lost requisite row, facet, base/Billing/Export grant, scope, archived visibility, or linked sensitive authority blocks the entire artifact through the existing engine. An artifact originally generated without a facet is not blocked merely because an unrelated grant changes; it is not enriched when a grant is gained. Domain edits alone do not invalidate authorized immutable bytes. Strict reference validation fails closed for missing/malformed/version-mismatched references.

Use `exports.Product` for both HTTP and worker registration. Existing engine already provides Pending/Running recovery, atomic Ready publication, requester ownership, 24 hours from Ready, integrity checks, audit and whole-artifact blocking. Do not fork it. Add Lease kinds to the existing authorized recovery selection/link allowlist so expiry/failure can lead to a fresh review and new operation ID; denied recovery never reflects hidden filters or IDs.

### 5. Browser integration and field API exclusion

Add browser-only GET/POST `/leases/report/export` (or confirm the route with coordinator before coding), registered through `registerExports`. Review shows mode, All Records scope, full filters, all-matching behavior, as-of semantics, fixed columns, and independent sensitive facets. POST uses the existing operation UUID and redirects to `/exports/{id}`. Use production middleware/CSRF and existing export status/download pages. Gate links by actual Export + compatible View, not Role names; do not block ordinary report browsing for readers without Export.

Carry filters through review, error correction, cancellation/back, status recovery, and regeneration. Remove browser pagination from export selection rather than exporting only the current page. Preserve report preference semantics, errors with retained authorized inputs, and zero/no-match distinction. The field API must have no Lease report/export endpoint, no export receipt/download escape, and no Lease identity/rate expansion: retain only the synchronized coverage Boolean and `as_of` observation.

## Parallel ownership and integration order

No subagents are spawned by this planning task. For the coordinator's later implementation, establish contracts first, then independent ownership:

| Workstream | Exclusive files | Dependencies / handoff |
| --- | --- | --- |
| A: report projection/domain | `internal/authorization/lease_report.go`, `lease_report_billing.go`, new `lease_exports.go`; new `lease_export_projection_test.go` | Own query types, shared transaction reader, all-matching selection, Billing additions and reference checks. Publish signatures and semantics first. |
| B: durable adapter | new `internal/exports/leases.go`, `lease_references.go`, `leases_test.go`, `leases_integration_test.go` | Can implement contract encoding against agreed A types in parallel; cannot own A files or registry. |
| C: browser/report UI | new `internal/handlers/lease_exports.go`; existing `lease_report.go`; `internal/templates/lease_report.templ`, new `lease_export.templ` and corresponding generated files; public export/report docs | Agreed filter/contract types and route; consumes B definition. Own all affected templ generation and regeneration review. |
| D: acceptance | all new `internal/handlers/lease_exports_*test.go`; `internal/handlers/testdata/browser/lease-exports.cjs` | Real fixtures/production routes; can prepare fixture scenarios in parallel, assertions finalize after A/B/C. D owns browser script; C supplies UI selectors. |
| C: integration wiring | `internal/exports/catalog.go`, `recovery.go`; `internal/handlers/exports.go`, relevant recovery URL switch in `financial_exports.go` | Exclusive C ownership alongside UI; B does not edit these files. Coordinator reviews integration. |
| Coordinator: docs/qualification | export docs and reconciliation | Final checks and evidence; contract coordination. |

Land conceptual steps in this order: (1) contract/signatures, (2) shared projection/ref checks, (3) adapters and UI, (4) registry/recovery wiring, (5) adversarial verification. No new schema or permissions are expected. If durable data is genuinely missing, stop to agree an additive migration rather than retrofit invented history. Do not implement #97's export types to obtain related identity references.

## Verification and release gates

All scenarios below are required future work, not results from this planning session. Reuse `permissionHTTPFixture`, current Lease fixture writers and disposable migrated PostgreSQL; browser paths must traverse real routing/authentication/CSRF and persist real export jobs. Exact cases per AC are in reconciliation.

1. Real HTTP/DB: custom-role matrix, base-only comparison under hidden Billing changes, complete all-page output, filtered summary arithmetic, generation snapshots/revisions/adjustments/settlement, archive history, exact boundary dates, malformed filters/references and selected-input access.
2. Deterministic races: gate real transactions/locks, not timing sleeps. Export snapshot versus term/rent/coverage/notice/settlement commit must yield wholly before or wholly after rows and totals. Grant or assignment loss before acceptance/generation/download must yield the existing fence's valid serialization; no mixed authority. Two workers/retry/cancelled generation produce one Ready set and audit, or no published records. Same operation replay versus changed intent is verified.
3. Fault injection in disposable schema: force failure after some emitted rows and force accepted/generated/downloaded audit failures. Assert rollback of effects/records/audit at each boundary, durable failure or recoverable Running, no partial successful CSV. Corrupt/remove one stored record to prove integrity failures send no CSV. Restore missing functionality is not a ticket dependency.
4. Browser: base-only and Billing review, filter/order persistence, export >100 matching Leases, navigation away/back and durable status, requester-only download, authorized expiry regeneration, denied no-leak recovery, 320px responsive keyboard/error journey. Exercise an actual Chromium download and parse the CSV independently.
5. API: authenticated mobile bearer with even Administrator grants gets no report/export/status/download route; supplied filters/actions cannot add one. Existing field indicator/reference-client tests prove only `covered`/`as_of`, with no offline date inference. Web cookie API probes must not substitute for mobile bearer tests.
6. Preservation: compare existing identifiers, generation financial blobs, revisions, coverage/notice/placement events, grants, and settlement records before/after exports (only export/audit state changes). Existing installed-fixture tests remain in the full suite; no new permission grants or historical backfill.

Commands to run after implementation, using an actual explicitly identified disposable PostgreSQL database:

```sh
make lint
FREEFSM_TEST_DATABASE_URL='<disposable PostgreSQL URL>' go test -race ./internal/authorization ./internal/exports ./internal/handlers -run 'TestLease(Report|Export)|TestFieldLease' -count=1
BROWSER_TESTS=1 FREEFSM_TEST_DATABASE_URL='<disposable PostgreSQL URL>' go test -race ./internal/handlers -run 'TestLease(Report|Export)' -count=1 -v
FREEFSM_TEST_DATABASE_URL='<disposable PostgreSQL URL>' make test
```

Inspect browser scripts' Node/Playwright and browser binary prerequisites before running; required browser tests skipping is not acceptance. Record actual expanded commands, exit status, passed/failed/skipped cases and any environmental blocker. Full `make test` includes `-race`; additional blanket reruns only for changed code/failures. Do not present these command templates as executed evidence.

## Questions / potential blockers for coordinator

- Contracts and route are confirmed; exact handoff is in interfaces. No further product clarification is required.
- A usable disposable PostgreSQL URL and Node/Playwright browser installation must be identified for final verification. Their availability has not been tested in this planning session.
- Generation queue reporting must share the domain's 30-day eligibility and retained disposition semantics. A report-only approximation would be a correctness blocker; inspect existing billing history/source functions before extracting the narrow helper.
- Notice history and imported terminated records may lack enough facts for every historical date. Add adversarial fixtures and report unavailable facts where necessary; do not silently claim all inherited cases conform based on source inspection.
- The schema is expected unchanged. Any discovered need to mutate Lease/Invoice history or backfill dates is a design escalation, not permission to broaden scope.
