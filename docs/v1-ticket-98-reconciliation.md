# #98 preparation and acceptance reconciliation

Prepared 2026-11-22. The inspection/gap sections below describe the exact base;
the frozen contract records implementation coordination. Actual check results
and acceptance disposition belong in `docs/v1-ticket-98-evidence.md`.

## Live gate and exact base

- First write: `gh issue edit 98 --repo freefsm-project/freefsm --add-assignee @me`.
  Immediately before claim, #98 was OPEN, unassigned, and native
  `issue_dependencies_summary.blocked_by` was 0 (three total dependencies).
  #37, #54 and #96 were CLOSED. Claim now resolves to `awstephan`.
- Dedicated branch: `implement/v1-98`.
- Worktree: `/home/adam/repos/freefsm/.worktrees/v1-spec-18/ticket-98`.
- Exact base: `b0d9280b0463956400068d3fb9b079923aaf49d5`, local
  `integration/v1-spec-18`, clean at inspection.
- Read-only `git ls-remote --heads origin` verified remote integration at
  `d39235859fc0c6f1a7434e3e91425f60f507390e`; local integration is 16 commits
  ahead, including the latest #93 Payment/projection repairs.
- Ancestor checks passed for `implement/v1-37`, `implement/v1-54`,
  `implement/v1-96`, remote integration, and `23da4af` (the accepted integration
  commit named in #96's closure comment). #96 implementation `800a2fd` and
  evidence commit `eb6e338` are included.
- #94 and #97 remain OPEN. Their branch tips `679411f` and `f88f58e` are NOT
  ancestors of the selected base. Neither implementation was incorporated.
- Main's `MM CONTEXT.md`, untracked `.agents/` and `skills-lock.json` were left
  intact. Existing worktrees and `/tmp/opencode/issue97-evidence-20261122/` were
  preserved. No tracker edit beyond claim, commit, push, fetch, merge, reset or
  rebase was performed.

## Normative sources and existing seams

Read root AGENTS, tracker/domain instructions, this base's CONTEXT, ADR 0002 and
0003, live #18 and #98, and #96's closure/evidence. Normative requirements:
DISC-04/05, PER-12, JOB-02/03, COM-02/06, CFG-02; additionally AUTH-05,
DISC-06 and API-01 constrain grants, failure presentation and field API exclusion.
ADR 0003 explicitly supersedes ADR 0002's old status-granted conversion wording.

No Workload or cohort implementation was found under `internal/`.
`internal/exports/catalog.go` currently registers Customer, activity, audit and
Timesheet/GPS definitions only. New adapters should use the existing durable
engine, not a generalized harness or #97's unmerged master export code.

Relevant existing code:

- `internal/authorization/schedule.go`: fenced, authorized active-Job selection,
  half-open interval intersection, explicit incomplete/unscheduled rows, equal
  assignees, independently gated Customer/Location/Invoice projections.
- `internal/handlers/schedule_projection.go`: company-local due-date overdue,
  Completed/Canceled exclusions, explicit Unassigned and repair presentation.
  Today is currently calculated in presentation; new report calculations need
  one captured as-of instant shared by rows and summaries.
- `internal/conversion/conversion.go`: retained Estimate creation identity,
  `estimate_invoice_conversion_cycles`, `reverted_at IS NULL` active cycle,
  source snapshots, hidden source/result records, reconversion with new Invoice.
- `internal/authorization/commercial_history.go`: independent pricing facet and
  first-link projection precedent; general parent access rejects hidden records.
  Cohort provenance therefore needs a purpose-specific authorization projection.
- `internal/handlers/{jobs,estimates,invoices}.go` and corresponding services:
  existing paginated operational lists. Job/Estimate handlers use search,
  status_id and customer_id; Estimate also has `valid=current`. These are not
  yet shared report/export selection contracts. Do not export the current page
  or independently reconstruct silently different filters.
- `internal/exports/{exports,worker,download}.go`: immutable versioned Definition,
  transaction-supplied Snapshot/Recheck, selected-input authorization extension,
  durable queue, atomic artifacts/audit, current whole-snapshot authorization,
  requester isolation and 24-hour availability.
- `internal/exports/timesheets.go` and `internal/authorization/time_exports.go`:
  useful precedent for separately registered Assigned/All kinds, validated
  filters, shared report projection and sensitive reference rechecks.
- `internal/authorization/catalog*_test.go`: Job/Estimate/Invoice Export already
  exist as independent J/A grants with compatible View. Do not introduce or
  auto-grant redundant report permissions merely to implement this ticket.

## All eleven acceptance criteria — baseline gaps and required proof

| # | Reconciliation and required work | Required proof |
|---|---|---|
| 1 | **Gap:** no Workload report. Add current scheduled, unscheduled, overdue, completed summaries, rows and matching drill-downs; shared schedule/due/Customer/assignee/category filters. Reuse operational schedule semantics, not map or pricing payloads. | Real HTTP/DB filtered rows, totals and links agree; zero matches differs from unavailable. |
| 2 | **Gap:** no report grouping. Select each Job once before grouping; emit each matching assignee group once, explicit Unassigned, and prominently label group totals nonadditive. Never sum groups for overall totals. | Multi-assignee Job, duplicate joins, disabled retained assignee, unassigned Job and scoped users. |
| 3 | **Partial seam:** schedule SQL uses start < range-end and end > range-start and retains unscheduled/incomplete rows; presentation uses company-date due < today and excludes Completed/Canceled. Share/codify report equivalent without changing recorded instants. | Cross-midnight, DST 23/25-hour days, exact boundaries, due today/yesterday, unscheduled overdue, Completed/Canceled; archive excluded from current Workload. |
| 4 | **Gap, facts available:** cohort must filter Estimate original `created_at`, join at most the active cycle (`reverted_at IS NULL`), and count each Estimate once through revert/reconvert. Do not count every cycle or use Invoice creation dates to select sources. | Create/convert/revert/reconvert real documents, verify original cohort identity and exactly one active conversion. |
| 5 | **Gap:** explicitly label current state of creation cohort, never historical as-of stage. Group `statuses.category_key`, not labels. Hidden Estimates appear only through authorized provenance; no ordinary actionable tombstone links. Independently authorize counterpart before exposing it or including restricted conversion measures. | Rename/reclassify labels, hidden-source ordinary access denial, counterpart grant/assignment loss, no historical-stage claim. |
| 6 | **Gap:** five product outputs are needed: Job operational, Estimate list, Invoice list, Workload and Estimate cohort CSV. Versioned fixed columns and documented filters/sort; operational Job output has no pricing. Any selected document financial columns require compatible pricing/financial facets at selection and recheck, including zero rows. | Separate contracts, independent grants, sensitive column denial, exact quoting/escaping, > one page, same report/list ordering with ID tie-break. |
| 7 | **Partial grants only:** J/A type-specific Export/View exist. New queries and references must apply each independently; Customer-only documents never qualify via a linked Customer; archived Jobs cannot serve as Assigned anchors; counterpart authority is not inherited. | J export + A view remains J; A export needs compatible A view; assignment/archive/grant changes; inaccessible source/counterpart cannot leak count or identifiers. |
| 8 | **Engine available, adapters absent:** normalize filters and stable sort once for report and CSV. Rows and summaries must come from one supplied transaction/as-of; do not call pool-backed reads inside Snapshot. All sizes use Pending/Running/Ready/Failed. Reuse durable full-download recheck; fail incomplete query rather than publishing partial/zero results. | Snapshot concurrency, > page, restart, empty, expiry/requester isolation, one-row loss blocks entire artifact, row iteration/query/audit/storage failures, recovery UI preserving filters. |
| 9 | **Existing atomic boundary:** export Accept uses WithinPermission and immutable audit; generation/publication and download audit are fenced. New adapters must recheck selected filters, row/counterpart/facet scope and archive/domain state in that transaction. No new independent write path. | Wait behind Role/assignment/archive/conversion changes; same-operation replay vs changed intent; audit failure rolls back acceptance/publication and discloses no bytes. |
| 10 | **Likely no domain migration required:** facts and export tables/grants already exist. If indexes/schema additions are needed, additive only. Preserve identities, creation dates, cycles and existing Role grants; never backfill invented historical stages. | Genuine earlier-schema upgrade fixture, retained values/relations/grants, new kinds do not alter historical artifacts. |
| 11 | **Not yet satisfied:** no #98 feature/tests implemented or executed during preparation. Reuse real migrated DB/production authenticated HTTP and focused persisted-browser conventions. API proof includes bearer export/report exclusion, not adding exports to the field API. | Run required `make lint` and explicit disposable-DB `make test`, focused race/HTTP/browser evidence and denial/failure/concurrency assertions. Record exact commands, exit status, skips and test names. |

## Proposed work ownership and sequencing

## Frozen implementation contract (2026-11-22)

The five outputs use explicit Assigned/All scope, current active records, stable
ID-tied sorting and generation-time snapshots (not acceptance-time or historical
stages). Authorization selectors consume the supplied `pgx.Tx`, capture
`transaction_timestamp()` and company timezone once, and return rows, totals and
normalized filters together. Web wrappers use the same selectors. All selected
filters/facets are checked even for empty results.

Workload owns `WorkloadFilter`, `WorkloadReport`, `WorkloadSnapshot`, normalization,
reference rechecks and a fenced service wrapper in `authorization/workload.go`.
It retains invalid/incomplete schedules as explicit repair backlog, absent
schedules as unscheduled, and valid half-open intersections as scheduled.
Overdue/completed overlap schedule measures; overdue uses company-local dates.
Distinct Jobs form overall totals; selected assignees bound groups, while rows
retain all authorized assignee names. Drilldowns retain every filter plus measure.

Commercial owner owns `EstimateCohortFilter`, `EstimateCohortReport`,
`EstimateCohortSnapshot` and `DocumentListFilter`, `DocumentListSnapshot` with
normalizers, fenced web wrappers and reference rechecks. Cohorts select original
Estimate creation dates; hidden provenance independently requires source Estimate
and active counterpart Invoice authority, each with its own active assigned Job
in Assigned scope. Unauthorized provenance is omitted before totals. Retained
Estimate category and Active conversion are separate; hidden sources have no
actionable link. References bind original/current anchors and exact active cycle.
Invoice fields in cohort CSV additionally require independent Invoice Export.

Job operational owner owns `JobOperationalFilter`, `JobOperationalSnapshot` and
reference checks. Operational selectors preserve search/status/customer and
Estimate `valid=current`, effective Invoice status, pagination only after full
selection, and stable ordering. Operational CSV excludes pricing; document CSV
uses fixed operational columns (no financial amounts), avoiding optional-column
ambiguity. Exact column arrays are documented alongside adapters.

All exports have separate Assigned/All version-1 registrations with existing
type-specific Export and compatible View grants. Adapters use the durable engine
and selected-input checks; no new grants/schema facts or generalized harness.
Ownership: domain workload/Job files, commercial domain files, export adapters
and catalog, and web handlers/templates/routes are disjoint implementation units.
Signatures may be refined by their owner before consumers begin, with this
contract updated to reflect the resulting public API.

Active implementation session roster (coordination only):

- Lead: `ses_dfaf41ab0ffe2PqTiPD97gmyNG`.
- Workload/Job domain: `ses_dfaf34e35ffeEl3BUSMhMQmypX`.
- Commercial domain: `ses_dfaf31193ffe5tHXxnUO27rPQN`.
- Export adapters/catalog/contracts: `ses_dfaf2df63ffeZLMfiwmgFJ6pNT`.
- Web/routes/templates/list integration: `ses_dfaf2b1e9ffeqJ4E464HBvi4kU`.
- Runtime provisioning/HTTP/browser acceptance: `ses_dfaf2737affepLs6itX61AG5zz`.
- Race/failure HTTP acceptance: `ses_dfaf0500fffeLrbqvDL2i3mWYo` (only
  `work_reports_concurrency_test.go` and `work_reports_failures_test.go`).

Publish API contracts early through the lead; consumers do not edit another
owner's files. All owners use this worktree, with no VCS mutations.

Agree the normalized filters, fixed columns, scope/counterpart rules and shared
snapshot signatures before parallel edits. The following are proposed new file
names, not files already implemented.

1. **Workload domain owner:** `internal/authorization/workload.go`,
   `job_operational_exports.go`, focused authorization tests. Own normalized
   Workload selection, summary/group semantics and Job operational row/reference
   projection. Consult schedule code; coordinate any extraction of shared
   interval/due logic rather than concurrently editing `schedule.go`.
2. **Commercial report owner (parallel with 1):**
   `internal/authorization/estimate_cohorts.go`, `document_list_exports.go`,
   focused tests. Own creation-cohort/provenance authorization and shared
   Estimate/Invoice list/export selection. Existing conversion writes remain
   the source of facts; no substitute lifecycle implementation.
3. **Export adapter owner (after signatures):**
   `internal/exports/{workload,estimate_cohorts,jobs,documents}.go`, versioned
   fixed columns/references and contract docs. Single owner of additions to
   `internal/exports/catalog.go`; preserve all existing registrations.
4. **Web owner (after signatures, parallel with 3):**
   `internal/handlers/work_reports.go`, report/export templates and focused JS
   only if needed. Own review/filter/refresh/status/link UX. Single integration
   owner for shared route/nav/list handler/template changes, including
   `internal/handlers/exports.go`. Generated templ files belong to this owner.
5. **Acceptance owner:** ticket-local `work_reports*_test.go` / export HTTP tests,
   browser scripts under `internal/handlers/testdata/browser/`, any additive
   migration fixture and final evidence. Build focused fixtures using current
   production router; no retired generalized harness or #97 dependency.

Integration coordinator resolves shared files and runs final checks once all
units are composed. Important review decisions: exact financial columns/facets,
explicit archive options, scope defaults, and the conservative hidden-source /
current-counterpart authorization policy. These are not grounds to change the
approved ticket scope or implement #94/#97.

## Runtime prerequisites and check commands

Inspection found Go 1.26.10, Node 22.22.2, PostgreSQL tools 18.6 and
`/usr/bin/chromium-browser` 157.0.8103.78. `initdb`, `pg_ctl`, `psql` exist.
Main's `internal/templates/testdata/browser/node_modules` exists and can be
provided via NODE_PATH; `require.resolve("playwright-core")` succeeded there.
An initial probe for `playwright` failed; the actual browser scripts use
`playwright-core`. `ent`/`templ` are present in `$HOME/go/bin`, which the Makefile
adds to PATH for generation.

Provision a **new ticket-owned disposable PostgreSQL cluster/database**, UTF8,
with a free port chosen at runtime, outside every preserved #97 evidence path.
Suggested root `/tmp/opencode/issue98-evidence-20261122/`; provide owned tmp/build
directories and adequate PostgreSQL/spool capacity. Do not target shared data.
Full backup tests also require a disposable cluster-admin URL with role/database
creation rights and the repository backup fixture extension configured as its
test documentation requires. Do not reconfigure another ticket's cluster.

Commands below are a proposed execution recipe, **not executed results**:

```sh
export GOMAXPROCS=2
export TMPDIR=/tmp/opencode/issue98-evidence-20261122/tmp
export GOTMPDIR="$TMPDIR"
export FREEFSM_TEST_DATABASE_URL='postgresql://<owned-user>@127.0.0.1:<owned-port>/freefsm_issue98?sslmode=disable'
export FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://<owned-admin>@127.0.0.1:<owned-port>/postgres?sslmode=disable'
export NODE_PATH=/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules

GOFLAGS=-p=2 make generate
GOFLAGS=-p=2 make lint
GOFLAGS=-p=2 make BUILD_DIR=/tmp/opencode/issue98-evidence-20261122/build compile
BROWSER_TESTS=0 GOFLAGS='-p=2 -count=1 -timeout=60m' make test
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
  GOFLAGS=-p=2 go test -race ./internal/handlers \
  -run '^Test(Workload|EstimateCohort|OperationalListExport)' -count=1 -v
```

The final focused regex must match the tests actually implemented; verify it
does not run zero tests. Full suite browser opt-outs do not establish browser
acceptance; run the focused browser suite separately. Record opt-in backup,
platform and subprocess-helper skips truthfully. Preparation ran no DB/browser
suite and provisions no runtime service.

Preparation check: `git diff --check` exited 0; the sole worktree addition is this
untracked reconciliation document. No feature quality/acceptance test pass is
implied by this whitespace check.
