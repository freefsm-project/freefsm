# Ticket 98 durable CSV contracts

## Registration and integration API

`exports.Product(pool)` is the shared HTTP/worker registry. The new definitions
use explicit `ExportScope: authorization.Assigned` (`J`) or
`authorization.All` (`A`):

| Definition | Versioned kind | Required Export and compatible View |
|---|---|---|
| `Workload` | `workload-{J|A}-v1` | Job |
| `Jobs` | `job-operational-{J|A}-v1` | Job |
| `EstimateCohorts` | `estimate-cohort-{J|A}-v1` | Estimate and independent Invoice Export for Invoice columns |
| `Documents{DocumentType: "estimate"}` | `estimate-list-{J|A}-v1` | Estimate |
| `Documents{DocumentType: "invoice"}` | `invoice-list-{J|A}-v1` | Invoice |

The existing Customer, activity, audit, Timesheet and GPS registrations remain.
Options are the corresponding authorization selector's JSON filter, without
pagination. Unknown fields and invalid filters are rejected. Scope is fixed by
the versioned definition, never widened by a broader View grant.

## Snapshot and lifetime

Acceptance queues intent, not rows. Generation selects every matching row in the
engine-supplied transaction. Report rows, totals and CSV selection share domain
selectors, normalized filters, one transaction timestamp and company timezone.
CSV describes generation-time current state, never historical stages or the
acceptance-time state. Output ordering includes an ID tie-break.

Every size, including zero rows, follows Pending → Running → Ready or Failed.
Selected Customer/assignee inputs and selected facets are authorized even when
no rows match. Failed queries, iteration, emission or audit do not publish a
partial artifact. Generation and publication use the existing atomic engine.

Artifacts are requester-only and available for 24 hours. Download rechecks the
entire immutable reference set and selected inputs under the authorization fence;
loss of any row, original/current assignment anchor, projected related-record
authority or conversion-cycle invariant blocks the entire artifact. The bytes
remain the generation snapshot; download does not silently regenerate rows.

`Service.RecoverySelection(ctx, actor, id)` returns
`RecoverySelection{Kind, Options}` for a new same-filter review of a Failed or
Ready receipt. It is restricted to these five operational outputs. Under the
authorization fence it checks requester/company, current Export/View grants and
selected inputs, and every original Ready snapshot reference **even after
expiry**. Blocked artifacts or any lost row/facet return no selection metadata.
Failed artifacts have no rows but still require current selected-input authority.
Missing/requester-mismatched receipts return NotFound; denied recovery is
Unavailable. Recovery never extends download expiry or reuses the old operation
ID; the new review must accept a new operation. Legacy export redaction is
unchanged.

Operational Job, Workload and document-list exports contain no pricing, amounts,
balances or financial calculations. Cohorts describe the current state of the
original Estimate creation cohort. Hidden sources are provenance, not actionable
Estimate links; an active counterpart requires independent Invoice authority.

## Exact version-1 columns

Column order is immutable for each versioned kind. Instants use RFC3339Nano
(including their UTC offset); `due_date` is a calendar date, `YYYY-MM-DD`.
Booleans are `true`/`false`. Absent optional values are empty cells. Document
numbers are the retained display labels. Names are authorized projections;
unavailable related-record names remain empty, not inferred from IDs.

**Job operational**:

```text
as_of,timezone,job_id,title,status,category,customer,assignees,schedule_start,schedule_end,due_date
```

**Workload**:

```text
as_of,timezone,job_id,title,status,category,customer,assignees,schedule_start,schedule_end,due_date,scheduled,unscheduled,repair,overdue,completed
```

Both contain one row per distinct Job. `assignees` contains all projected names
in user-ID order, separated by `; `, or `Unassigned`. This is a display cell,
not a delimiter-safe machine identity array. Disabled retained participants
have the domain's ` (unavailable)` suffix. Workload CSV does not duplicate rows
for the report's assignee groups. Overall counts may be computed from distinct
CSV Jobs and measure flags; report group totals are nonadditive. Overdue and
completed measures can overlap schedule measures. Repair is explicit rather
than silently treating incomplete/invalid schedules as valid scheduled work.

**Estimate list and Invoice list** (same ordered column names, separate kinds):

```text
as_of,timezone,document_id,document_number,title,created_at,status,category,job_id,customer
```

Invoice status is the effective operational status. There are no amounts,
pricing JSON, settlement state or balance columns. List rows exclude archived
and conversion-hidden documents. Customer-only documents can appear in All
scope; they never qualify for Assigned scope through a linked Customer alone.

**Estimate cohort**:

```text
as_of,timezone,estimate_id,estimate_number,created_at,status,retained_category,conversion_hidden,active_conversion,invoice_id,invoice_number
```

Exactly one row per original Estimate, selected by its original creation date.
`retained_category` is the Estimate's current semantic category, independently
of `active_conversion`. Active conversion means the exact unreverted cycle.
Revert/reconvert retains the Estimate identity/date and replaces the active
Invoice; it never duplicates the Estimate. Hidden source records carry no
actionable link. Invoice ID/number require independent Invoice View and Export
at the selected scope, including for an empty CSV. References retain source and
counterpart original/current Job anchors, hidden state and exact cycle identity
(including absence). Cycle changes block an old artifact.

The shared engine uses standard CSV quoting for commas, quotes and newlines.
Spreadsheet formula-leading cells (including after whitespace/control characters)
are prefixed with a single quote; existing leading quotes are doubled. No
spreadsheet formula wrapper is used.

## Filters and stable sorting

All JSON option names below are snake_case; default values come from the same
normalizers used by the report/list. There is no page, limit, offset, scope,
optional financial-column or include-archived option in these filter payloads.

| Output | Accepted filters | Sorting |
|---|---|---|
| Job operational | `search`, `status_id`, `customer_id`, `sort` | Only/default `schedule_desc`: schedule start descending (SQL NULLs first), creation instant descending, ID descending |
| Workload | `schedule_from`, `schedule_to`, `due_from`, `due_to`, `customer_id`, `assignee_ids`, `unassigned`, `category`, `measure`, `sort` | Only/default `schedule_asc`: start ascending, missing starts last, ID ascending |
| Estimate list | `search`, `status_id`, `customer_id`, `valid`, `sort` | Default `created_desc`; also `created_asc`, `title_asc`, `number_asc` |
| Invoice list | `search`, `status_id`, `customer_id`, `sort` | Same document sorting |
| Estimate cohort | `created_from`, `created_to`, `customer_id`, `category`, `conversion`, `sort` | Same document sorting, on original Estimates |

Document/cohort `created_desc` orders creation then ID descending;
`created_asc` orders both ascending; `title_asc` orders lowercase title then ID
ascending; `number_asc` orders numeric document number then ID ascending.
Search is trimmed, case-insensitive literal substring matching: Job title or
subtitle, document title. Status filters use current status IDs (effective
Invoice status); categories use semantic keys, never display labels.

Estimate `valid` is empty or `current`; current includes no expiration and
valid-through dates on/after the generation company-local day. Invoice does
not accept `valid=current`. Cohort `conversion` is empty, `active` or `none`.
Workload `measure` is empty or a comma-separated conjunction of `scheduled`,
`unscheduled`, `repair`, `overdue` and `completed`. For example,
`overdue,unscheduled` selects Jobs satisfying both flags. The normalizer sorts
and deduplicates predicates; drilldowns add a predicate instead of replacing
the existing selection. Mutually exclusive predicates yield no matching rows.
Assignee IDs are positive, sorted and deduplicated; `unassigned`
cannot be combined with selected assignee IDs.

Date bounds are inclusive company-local dates. Creation periods use a half-open
instant interval from the first local midnight through midnight after the last
date. Schedule selection intersects valid intervals using start < upper bound
and end > lower bound, preserving DST day length; unscheduled and repair backlog
remain eligible. Due bounds omit Jobs with no due date. Overdue means due date
before the captured company-local day, excluding Completed and Canceled.

## Adapter verification (2026-11-22)

With `/tmp/opencode/issue98-evidence-20261122/env.sh` sourced (explicit disposable
PostgreSQL URL), `GOMAXPROCS=2 GOFLAGS=-p=2`:

- `go test -race ./internal/exports -count=1 -v` — exit 0; real durable Job,
  Workload, document and cohort tests, including real conversion cycles, empty
  selected-input/facet denial, revoked grants, archive rechecks, concurrent
  idempotency, generation-time edits, CSV escaping and atomic storage failure.
- `make lint` — exit 0.
- After adding the final Assigned-scope test and exercising conjunctive measures:
  `go test -race ./internal/exports -run 'Test(JobOperationalAssignedExportDoesNotInheritAllView|WorkloadDurableDistinctJobsAndFlags)$' -count=1 -v`
  — exit 0, both named tests executed.

Logs: evidence `logs/exports-race.log`, `logs/exports-make-lint.log`, and
`logs/exports-final-scope.log`. These are focused adapter results; full integrated
`make test`, HTTP/browser and ticket acceptance gates are coordinator-owned.

Recovery follow-up: `go test -race ./internal/exports -run '^TestRecoverySelection' -count=1 -v`
— exit 0 against the same disposable DB. Three tests cover expired recovery and
whole-snapshot row loss, requester mismatch, real Failed preparation with lost
selected Customer authority, blocked receipts and empty cohort Invoice Export
revocation. Log: `logs/exports-recovery.log`.
