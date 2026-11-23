# Issue #98 implementation evidence

Implementation and verification completed for #98, 2026-11-22. All eleven
criteria below have supporting evidence; final generation, lint, compile,
explicit-disposable-DB full suite, focused race/HTTP/persisted-browser checks and
independent implementation review are complete. No outstanding #98 blocker was
identified. The parent coordinator's separate final review is also clean; any
commit or tracker action remains separate and has not been performed.

## Scope and provenance

- Worktree: `.worktrees/v1-spec-18/ticket-98`, branch `implement/v1-98`.
- Base: `b0d9280b0463956400068d3fb9b079923aaf49d5`.
- Normative sources: live #98 and #18, root AGENTS, CONTEXT, ADRs 0002/0003,
  `docs/v1-ticket-98-reconciliation.md`.
- Evidence root: `/tmp/opencode/issue98-evidence-20261122/`.
- #94 and #97 remain excluded; no retired generalized harness is used.
- No commits, pushes, resets, rebases, merges or issue closure are authorized.

## Acceptance matrix

| # | Criterion | Evidence and result |
|---|---|---|
| 1 | Workload measures, filters, rows, matching drilldowns | `TestWorkloadFiltersBoundariesAndDrilldownsHTTP`, `TestWorkloadConjunctiveMeasureAndAssigneeDrilldownHTTP`: real HTTP totals, rows and actual links, including already-selected measure conjunctions. |
| 2 | Distinct overall Jobs, nonadditive assignee grouping, Unassigned | Same HTTP fixtures plus `TestWorkloadOperationalSnapshotScopeFiltersAndReferences` and `TestWorkloadDurableDistinctJobsAndFlags`: multi-assignee Job once overall, matching groups only, disabled retained names and Unassigned; CSV one row per Job. |
| 3 | Half-open schedules, DST/local dates, unscheduled and overdue exclusions | `TestWorkloadClassificationBoundariesAndRepair`, boundary HTTP fixtures and `TestWorkloadCompanyDateOverdueAndCategoryHTTP`: 23/25-hour days, exact boundaries, invalid/incomplete repair backlog, due today/yesterday, Completed/Canceled, archived exclusion. |
| 4 | Original Estimate creation cohort; convert/revert/reconvert identity | `TestEstimateCohortConvertRevertReconvertHTTP`, `TestEstimateCohortConversionIdentityScopeAndCycleRecheck`: real conversion services/HTTP, original creation/identity, two cycles/one active, one CSV source, exact-cycle invalidation. |
| 5 | Current semantic categories, authorized hidden provenance | Cohort tests verify current semantic category independent of labels/conversion, source hidden-route denial, no hidden Estimate link, independently authorized active Invoice link, counterpart grant/assignment loss before counts. |
| 6 | Five fixed-column outputs, documented fields, financial-facet boundaries | `TestOperationalListExportAllMatchingRowsAndCSVHTTP`: 135 rows for each operational type, same list order, header-only empty output and exact CSV escaping. Adapter tests cover Workload/cohort. `docs/v1-ticket-98-export-contracts.md` freezes columns/filters; CSV contains no pricing/amounts, cohort Invoice columns require independent Invoice Export even empty. |
| 7 | Independent Assigned/All grants and counterpart/anchor authorization | `TestOperationalListExportIndependentViewAndExportHTTP`, `TestOperationalListExportAssignedScopeAndWholeArtifactRevocationHTTP`, `TestJobOperationalAssignedExportDoesNotInheritAllView`, document/cohort domain references: J Export with A View stays J; Customer-only/archived anchors excluded; original/current anchors rechecked. |
| 8 | Shared filters/snapshot/order; durable exports and full recheck | `TestWorkloadCoherentSnapshotDuringSourceChange`, `TestOperationalListExportInterruptedWorkerRestartReplayHTTP`, real second-row query-iteration faults and `TestWorkReportsRecoveryPersistedBrowser`: coherent generation-time snapshot, no partial publication, restart/replay, all five Failed/expired receipt recovery journeys preserving scope/filter/sort, whole-artifact denial and requester isolation. |
| 9 | Fenced commit-time authority, atomic effects/audit | `TestOperationalListExportActualGrantRevocationBoundaries`, `TestWorkloadActualAssignmentAndArchiveBoundaries`, `TestEstimateCohortActualConversionReversionBoundaries`, `TestOperationalListExportAtomicFailuresHTTP`: witnessed actual DML/fence ordering at acceptance/generation/download; audit, publication/storage and transfer-spool failures disclose no partial bytes. |
| 10 | Non-destructive upgrades, preserved facts/identities/grants | `TestWorkReportsEarlierSchemaUpgradePreservesCohortFactsHTTP`: genuine migrations through 124, populated convert/revert/reconvert + historical artifact, then 125 onward; compares retained documents/created dates/cycles/operation admissions/Roles/grants/export records and exact historical CSV. No new schema migration or grants added by #98. |
| 11 | Real PostgreSQL/HTTP/browser/API, lint/full test/compile/race | Production-router session HTTP and persisted Chromium with migrated DB; `TestWorkReportsBearerCannotAccessOfficeReportsOrExports` verifies office/API exclusion. Generation/lint/compile passed; final combined race/browser gate passed all 22 top-level tests, zero skips (122.615s). Final full-suite rerun exited 0, all 19 tested packages passed; skip inventory is explicit below. |

## Commands and results

### Dedicated disposable database

Provisioned a new UTF8 PostgreSQL 18 cluster under the ticket evidence root,
listening on `127.0.0.1:56075`. Runtime `issue98_app` owns database
`freefsm_issue98` and is NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOREPLICATION/
NOBYPASSRLS with origin trigger mode. Distinct `issue98_admin` supplies the
installation and backup fixtures. The backup extension uses the ticket-local
`extension_control_path`. Provisioning output is retained in
`logs/provision.log` and `logs/postgres.log` under the evidence root.

All checks source `/tmp/opencode/issue98-evidence-20261122/env.sh`, including:

```sh
export FREEFSM_TEST_DATABASE_URL='postgresql://issue98_app@127.0.0.1:56075/freefsm_issue98?sslmode=disable'
export FREEFSM_INSTALLATION_TEST_ADMIN_URL='postgresql://issue98_admin@127.0.0.1:56075/postgres?sslmode=disable'
export FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://issue98_admin@127.0.0.1:56075/postgres?sslmode=disable'
```

The focused browser/race gate uses a second newly created app-owned UTF8 database
on the same dedicated cluster, `freefsm_issue98_focused`, to avoid artificial
cross-suite advisory-lock fixture interference. Installation/backup admin URLs
remain on the same disposable cluster. `logs/runtime-verification.log` records
UTF8, role flags, origin mode, absence of replication-mode SET privilege and the
backup fixture extension.

### Final integrated commands

All commands run at the ticket worktree root after sourcing `env.sh` above.

| Command | Result | Evidence log |
|---|---|---|
| `GOFLAGS=-p=2 make generate` | Exit 0 | `logs/final-generate.log` |
| `GOFLAGS=-p=2 make lint` | Exit 0, also after generated-metadata cleanup and both fixture repairs | `logs/final-lint.log`, `logs/final-lint-postreview.log`, `logs/final-lint-fixture-repairs.log` |
| `GOFLAGS=-p=2 make BUILD_DIR=/tmp/opencode/issue98-evidence-20261122/build compile` | Exit 0, also after generated-metadata cleanup | `logs/final-compile.log`, `logs/final-compile-postreview.log` |
| `FREEFSM_TEST_DATABASE_URL='postgresql://issue98_app@127.0.0.1:56075/freefsm_issue98?sslmode=disable' BROWSER_TESTS=0 GOFLAGS='-p=2 -count=1 -timeout=60m' make test` | Exit 2; retained failed attempt, superseded by repaired rerun | `logs/final-make-test.log` |
| `FREEFSM_TEST_DATABASE_URL='postgresql://issue98_app@127.0.0.1:56075/freefsm_issue98?sslmode=disable' BROWSER_TESTS=0 GOFLAGS='-p=2 -count=1 -timeout=120m' make test` | Exit 0; all 19 tested packages passed; 1,615 top-level PASS results, 167 skip entries | `logs/final-make-test-rerun.log` |
| `FREEFSM_TEST_DATABASE_URL='postgresql://issue98_app@127.0.0.1:56075/freefsm_issue98_focused?sslmode=disable' BROWSER_TESTS=1 GOFLAGS=-p=2 go test -race ./internal/handlers -run '^Test(Workload\|EstimateCohort\|OperationalListExport\|WorkReports)' -count=1 -timeout=20m -v` | Exit 0; 22 top-level tests passed, zero skips, 122.615s | `logs/final-focused-race-browser.log` |

The Makefile's full test target runs `go test -v -race ./...`. Final handlers
package time was 3833.006s; backup package time was 351.274s. Those are package
times, not an inferred total wall-clock duration. Both previously failing fixture
cases passed in the complete rerun. No feature production changes occurred after
the clean 22-case focused gate; subsequent edits were the two reviewed fixtures
and documentation.

### Actual skips and bounded claims

Every skipped test/subtest and its emitted reason is retained in
`logs/final-suite-skips.txt`; `logs/final-suite-summary.json` records package
outcomes and counts. The full suite's 167 skip entries comprise:

| Reason | Entries |
|---|---:|
| Full-suite browser opt-out (`BROWSER_TESTS=0`) | 123 |
| Secret Service/offline-key opt-in not enabled | 33 |
| Subprocess-only helper entrypoints (exercised separately by their parent tests) | 8 |
| Unsupported-server negative fixture requires PostgreSQL <16; this cluster is 18 | 1 |
| Backup/nginx browser scale opt-in not enabled | 1 |
| Accessibility opt-in not enabled | 1 |

The two #98 browser tests skipped by the full-suite browser opt-out were both
enabled and passed in the separate final 22-case race/browser gate, which had
**zero skips**. Database integration was not skipped for an unset URL. These
results do not claim the unrelated opt-in suites passed, a cross-browser matrix,
or native release/scale acceptance outside #98. No remaining #98 implementation,
test, or independent-review blocker is known.

### Focused development checks and retained failures

These are supporting evidence, not substitutes for the final gates:

- The first full gate found `TestSaveSafetyNativeInitialReadFailureHTTP` rejecting
  its Job/Estimate/Invoice URLs with 400. Its old fixture used unsupported
  `name_desc`, formerly ignored by those handlers. The fixture now selects each
  output's documented `schedule_desc`/`created_desc` sort; all eight native
  fault-injection cases pass with `-race`, retaining the 503, Retry and exact
  filter-retention assertions (`logs/web-list-regression-{red,green,lint}.log`).
  No production sorting contract was weakened. Backup
  `TestPendingActivityBlocksNewWorkAndReplaysExactly` hit its 90-second preflight
  deadline and reproduced alone: the storage-only fixture's `unused` hostname
  depends on external DNS. The captured stack confirms `net.cgoLookupHost`
  precedes pgx's connection timeout; `PGCONNECT_TIMEOUT=2` still reproduced the
  90-second failure. The storage-only fixture now uses a guaranteed missing Unix
  socket under its existing `t.TempDir`. An external Go overlay passed three
  race runs, then the real patched original test passed in 0.01s (package 1.058s)
  with no timeout override. No assertions, backup production code, host resolver
  or global configuration changed. Logs: `backup-pending-{isolated,stack,connect-timeout,local-socket-overlay,fixed}.log`.
  The handlers package additionally reached the configured 60-minute package
  deadline while tests were still progressing (the final running test had run
  one second); the next complete gate will allow a larger package budget.
  This failed full-suite attempt is not an acceptance pass.
  A duplicate owner-only backup package rerun was intentionally stopped (exit
  143, `logs/backup-package-fixed.log`) before the final whole-suite rerun to
  avoid redundant competing test processes; it is not claimed as a pass.

- Workload/Job domain race tests and authorization vet passed:
  `logs/workload-tests-race.log`, `logs/workload-vet.log`.
- Commercial selector/cohort race tests, including archived Customer projection,
  exact Context anchors and original/current Job–Customer matching, passed:
  `logs/commercial-domain-tests-race.log`, `logs/commercial-domain-anchor-*`.
- Full export package race tests and lint passed before the recovery addition:
  `logs/exports-race.log`, `logs/exports-make-lint.log`. Final scope/conjunction
  tests passed in `logs/exports-final-scope.log`; recovery API race tests and vet
  passed in `logs/exports-recovery.log`, `logs/exports-recovery-vet.log`.
- General acceptance iteration `logs/acceptance-fifth.log` passed 10 of 11 test
  names; the remaining fixture expected 404 for a hidden Estimate whose ordinary
  route correctly returns 403. After correcting that denial assertion,
  `TestEstimateCohortConvertRevertReconvertHTTP` passed in `logs/cohort-final.log`.
- All five outputs' Failed/expired persisted-browser regeneration and current
  grant/row-loss metadata denial passed in `logs/recovery-initial.log` (three
  top-level tests, 15.950s); both browser scripts passed `node --check`.
- Race/failure owner's non-race component runs passed across
  `logs/race-first-full.log`, `logs/race-corrected-contracts.log` and
  `logs/race-faults-complete.log`. Their `logs/race-final.log` run was interrupted
  by a 120-second tool timeout (exit 143); it is **not** a complete race pass.
  The final combined race gate above supersedes that interrupted attempt.
- Earlier logs retain compile-before-integration failures, required Invoice-date
  fixture omissions, stale plural cohort test paths, and initial assertions that
  were corrected. No failed or skipped attempt is represented as passing.

## Independent review

Design review supplied five mandatory constraints recorded in the reconciliation
contract. Post-implementation review is complete in
`docs/v1-ticket-98-independent-review.md`: two Standards findings and one Spec
finding were repaired and rereviewed; **zero outstanding concrete blockers**.
The reviewer independently exercised domain/adapter/HTTP/browser and recovery
checks. Their results supplement, rather than replace, the final gates above.
Both full-gate fixture-only corrections were independently accepted in review
addenda: valid per-output sort restores the intended read-failure path; missing
local socket preserves the unavailable-database path without external DNS.
The substantive failure/retry/filter-retention and backup activity/replay
assertions remain unchanged.

The parent coordinator additionally reported a clean bounded final review by
`ses_dfa9f074bffeCvoquPXA8SobAp`: no actionable high-impact findings, both
fixture-only corrections accepted without weakened assertions, and
`git diff --check b0d9280` passing. This is separate from the implementation
review documented in the repository.

Resolved findings include same-filter Failed/expired recovery under current
whole-snapshot authorization, configured Invoice date formatting, and preserving
compound Workload measures before error/principal paths. Lead review additionally
corrected measure intersection drilldowns, exact Customer/Job reference checks,
compatible export-scope choices, snapshot-consistent UI billing, and independently
authorized active Invoice links without actionable hidden Estimate links.

## Changed files and retained runtime

Exact tracked/untracked inventory: evidence `logs/final-git-status.log` (59 files;
18 modified and 41 new, including the preparation reconciliation document).

- `internal/authorization/{workload,job_operational_exports,estimate_cohorts,document_list_exports}.go`
  and their tests: shared selectors, filters, snapshot semantics and reference checks.
- `internal/exports/{workload,jobs,documents,estimate_cohorts,recovery}.go`, their
  tests and `catalog.go`: ten scope-specific registrations and authorized recovery.
- `internal/handlers/{work_reports,work_report_filters,work_exports}.go`, existing
  Job/Estimate/Invoice list handlers, `exports.go` and `router.go`: production web integration.
- `internal/handlers/work_reports*_test.go`, `operational_list_export_test.go`,
  `report_filter_unit_test.go`, and both `testdata/browser/work-report*.cjs`
  scripts: HTTP, browser, denial, concurrency, fault and genuine-upgrade evidence.
- `internal/templates/work_reports.{go,templ}`, list/export templates and generated
  Go files, `navigation.go`, `types.go`: report/review/recovery UI.
- `docs/v1-ticket-98-{reconciliation,export-contracts,independent-review,evidence}.md`.
- Two existing fixture-only repairs exposed by the full gate:
  `internal/handlers/save_safety_integration_test.go` uses valid per-output sorts,
  and `internal/backup/recovery_test.go` avoids external DNS for an intentionally
  unavailable storage-only database. Assertions and production behavior remain intact.

`make generate` succeeded. Three unrelated baseline-generated templates used a
different diagnostic filename convention; their filename-only churn was removed
without changing source templates or using Git mutation commands. Whitespace
validation is retained in `logs/final-diff-check.log` (exit 0).

The isolated PostgreSQL cluster, two disposable databases, logs and compiled
development binary are retained under the evidence root for coordinator review.
No other ticket's paths or databases were modified. No commit, push or tracker
closure was performed.
