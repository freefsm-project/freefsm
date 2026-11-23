# #98 independent post-implementation review

Review date: 2026-11-22. **Final source review: findings repaired and rereviewed.**
The execution notes below record the review-time state; subsequent coordinator
gate completion is recorded at the end and in `v1-ticket-98-evidence.md`.

## Scope and method

- Exact base: `b0d9280b0463956400068d3fb9b079923aaf49d5`; comparison is `git diff b0d9280` plus untracked files, exclusively in this worktree. No commits existed after that base when review began.
- Read AGENTS, CONTEXT, live #98/#18, and the frozen mandates in `docs/v1-ticket-98-reconciliation.md`. Reviewed domain selectors, all ten Assigned/All adapter registrations, web/list integration, and acceptance tests, including failure/concurrency/browser/API/upgrade paths.
- Parallel independent Standards and Spec reviews supplemented the primary audit. Production and test files remained read-only; only this review document was written. Findings were sent early to the lead through session API prompts.
- Generated-template churn initially consisted predominantly of diagnostic source-path shortening; unrelated generation churn was removed. Remaining generated changes accompany the modified list and export templates.

## Standards

1. **Resolved P2 — Failed/expired export receipts lacked filter-preserving recovery.** Initially, `internal/handlers/work_exports.go:93–100` sent all new outputs to a receipt without any same-selection review link. Repair verified in `internal/exports/recovery.go:23–75`, `internal/handlers/work_exports.go:27–72`, `internal/handlers/exports.go:116–120,140–147`, and `internal/templates/exports.templ:6–11,45–75`. Recovery rechecks requester/company, current selected-input/grant authority and every original Ready reference even after expiry. No selection escapes after a failed check; JSON receipts remain redacted. A new reviewed operation preserves scope/filter/sort without extending the old artifact lifetime. Independently executed recovery DB/HTTP/browser tests passed.
2. **Resolved P3 — Invoice list ignored configured date presentation.** Initially canonical SQL dates replaced the former configured presentation. `internal/handlers/invoices.go:88` now calls `displayReportDate` for both dates, preserving the civil date while honoring configured display formatting. No CSV date semantics were changed.

No heuristic code smell was promoted to a finding without a concrete consequence.

## Spec

1. **Resolved P2 — Unavailable compound-measure report silently changed the recovery form.** Initially, the combined option was added only after successful selection; a failure for `measure=overdue,scheduled` made the form default to “All matching Jobs.” `internal/handlers/work_reports.go:145–152` now prepares the canonical selection and compound option before scope/selector failure paths. This addresses DISC-06, #98 criterion 8, and the frozen filters-retained mandate. The independent reread additionally caught the noncanonical ordering/duplicate-input variant before principal loading; unconditional early normalization at line 145 is now verified too.

The initially reported plural cohort test routes have been corrected and are withdrawn, not counted as outstanding findings.

## Substantive audit coverage

| #98 criterion | Independent assessment |
|---|---|
| 1 | Workload rows and totals use the same classified population; measure drilldowns now intersect predicates instead of replacing them. Error-form preservation was repaired. |
| 2 | Distinct Job selection precedes grouping; selected assignees bound groups, all row assignees remain represented, Unassigned is explicit, and nonadditive labeling is prominent. |
| 3 | Valid schedules use half-open intersection; absent and invalid schedules remain explicit backlog. Civil due dates compare against one company-local as-of date; Completed/Canceled exclusions and 23/25-hour boundary tests are present. |
| 4 | Cohort bounds use original Estimate creation instants and company-local dates. Only `reverted_at IS NULL` cycles join; convert/revert/reconvert retains one source identity. |
| 5 | Semantic category keys drive grouping. Hidden sources have no ordinary detail link. Source Estimate and active Invoice qualify independently before rows or counts; no inherited counterpart permission. |
| 6 | Five versioned fixed-column outputs each register Assigned/All variants. Job and document CSVs omit amounts. Cohort Invoice fields require independent Invoice Export/View even for an empty result. Shared durable CSV encoding supplies escaping. |
| 7 | Explicit Assigned SQL remains assignment-bounded with All View; All Export requires compatible All View. Customer-only documents and archived Job anchors do not qualify. Rechecks bind original/current source and counterpart anchors plus exact cycle/hidden state. |
| 8 | Selectors consume the supplied transaction and company-bound clock; rows/totals share one snapshot. Operational lists and exports use shared normalized filters and stable ID-tied ordering; Invoice filtering joins effective status. Receipt recovery is now independently verified. `jobs.go:100–102` consumes the independently authorized `UIBillingTypes` populated inside the same snapshot, replacing the separate per-row transactions. |
| 9 | Adapters use the existing fenced durable engine, selected-input authorization, and whole-artifact recheck; no independent mutation/audit path was introduced. Failure tests cover acceptance/generation/download audit and second-row storage rollback. |
| 10 | No new migration or grant backfill. Independently executed earlier-schema fixture starts at migration 124, retains convert/revert/reconvert facts, grants and historical artifact bytes through forward migration. |
| 11 | Real PostgreSQL, production HTTP, persisted-browser, bearer exclusion, race and failure tests exist. Focused independent executions below passed; final lint/full suite/browser/race evidence remains coordinator-owned. Test presence alone is not acceptance. |

Selected Customer/status/assignee filters are authorized before row selection, including zero matches. Customer projection/recheck now consistently handles archived authority and exact Context Job anchors. Download references fail closed on cycle drift and original/current Assigned-anchor loss; source access does not authorize the counterpart. Company clocks are company-bound, and filter JSON tags are snake_case.

## Independent executions

Both commands sourced `/tmp/opencode/issue98-evidence-20261122/env.sh`, explicitly setting `FREEFSM_TEST_DATABASE_URL=postgresql://issue98_app@127.0.0.1:56075/freefsm_issue98?sslmode=disable`.

```sh
GOFLAGS=-p=2 go test ./internal/authorization ./internal/exports \
  -run 'Test(Workload|JobOperational|EstimateCohort|DocumentList)' \
  -count=1 -timeout=5m
```

Exit 0: authorization 7.668s; exports 6.369s. This selected domain/adapter subset is not the full suite.

```sh
GOFLAGS=-p=2 go test ./internal/exports \
  -run '^Test(DocumentOperationalDurableFixedColumns|OperationalExportFilterContracts|OperationalExportsSelectedInputDenialAndFailedPublication)$' \
  -count=1 -v -timeout=5m
```

Exit 0, 2.474s: all three named tests passed, including Estimate/Invoice subtests; no skips shown. The generation-failure warning is the intentional injected publication failure.

```sh
GOFLAGS=-p=2 go test ./internal/handlers \
  -run '^Test(WorkloadFiltersBoundariesAndDrilldownsHTTP|WorkloadConjunctiveMeasureAndAssigneeDrilldownHTTP|WorkloadCompanyDateOverdueAndCategoryHTTP|WorkReportsBearerCannotAccessOfficeReportsOrExports|WorkReportsEarlierSchemaUpgradePreservesCohortFactsHTTP)$' \
  -count=1 -v -timeout=5m
```

Exit 0, 5.297s: all five named real-DB/HTTP tests passed, including both DST days, bearer-route subtests and earlier-schema upgrade; no skips shown.

```sh
BROWSER_TESTS=1 GOFLAGS=-p=2 go test ./internal/handlers \
  -run '^TestWorkReportsPersistedBrowser$' -count=1 -v -timeout=5m
```

Exit 0, 4.593s: persisted Chromium journey passed, including filtered reports, reviewed exports, browser restart, durable receipt lookup, CSV bytes, reader denial and requester isolation. No skips shown. This is the implemented focused browser journey, not a cross-browser/accessibility matrix or failure-recovery browser proof.

`git diff --check b0d9280` exited 0. No full-suite, lint, or race pass is claimed by this independent review.

After the recovery repairs, independently executed:

```sh
BROWSER_TESTS=1 GOFLAGS=-p=2 go test ./internal/exports ./internal/handlers \
  -run '^Test(RecoverySelection.*|WorkReportsRecovery.*)$' \
  -count=1 -v -timeout=5m
```

Exit 0: exports 3.774s; handlers 15.889s. All three `RecoverySelection` tests and all three `WorkReportsRecovery` tests passed, no skips shown. The persisted browser covered all five output contracts, actual failed generation, expired Ready receipts, browser restart, exact selection retention, fresh reviewed intent and regeneration. HTTP tests withheld selection metadata after grant loss and after loss of an original expired-artifact row. Injected generation-failure and browser-navigation cancellation messages were logged; they did not fail the tests.

**Final summary:** Standards: 2 findings resolved (original worst P2), 0 outstanding. Spec: 1 finding resolved (P2), 0 outstanding. No additional concrete correctness/security blocker found in this bounded review. This is not ticket acceptance: coordinator-owned final lint/full disposable-DB suite/race gates must complete and be recorded separately.

## Full-suite follow-up: native initial-read failure fixture

Independently reviewed the test-only diff in `internal/handlers/save_safety_integration_test.go:89–99`. **Accepted: this corrects fixture input without weakening the native read-failure contract.** Jobs now use supported `schedule_desc`; Estimates/Invoices use supported `created_desc`. The existing Timesheet `start_desc` exception and other four cases are unchanged. Previously, unsupported `name_desc` caused the three new shared selectors to return validation HTTP 400 before reaching the injected missing-table failure.

All eight cases still rename their real source table, issue an authenticated native GET with search and page selection, and require HTTP 503, a Retry action, retained search, and no raw SQL/missing-table disclosure or false-empty text (`:83–108`). No fault injection, route, assertion, expected response, or case was removed or relaxed. Existing assertions check search retention, not an exhaustive sort/page round trip; that limitation predates this correction.

Inspected evidence logs under `/tmp/opencode/issue98-evidence-20261122/logs/`: `web-list-regression-red.log` shows precisely Jobs/Estimates/Invoices failing with 400; `web-list-regression-green.log` shows all eight passing and the three corrected paths reaching actual missing-relation failures. The owner reports the green execution used `-race`; this log alone does not encode its invocation. `web-list-regression-lint.log` is empty, so it alone cannot establish lint exit status. No new tests were launched by this follow-up review. No additional finding; backup/environment investigation and full-suite rerun remain separate coordinator gates.

## Full-suite follow-up: deterministic unavailable backup database

**Accepted: semantically equivalent for this fixture's intended failure, with external DNS removed.** The only diff in `internal/backup/recovery_test.go:24–25` changes `storageManager` from the unresolved `unused` hostname to an explicit PostgreSQL `host` query parameter naming an absent Unix-socket directory inside its unique `t.TempDir`, with SSL disabled for that local transport. It remains a syntactically valid connection configuration pointing to an unavailable database; it does not substitute a mock, reachable server, or malformed DSN. The private absent directory prevents accidentally selecting a running local PostgreSQL instance and avoids resolver/network timing.

`internal/backup/database.go:82–85` still executes real connection preflight and classifies its failure as `FailureDatabaseConnection`. `TestPendingActivityBlocksNewWorkAndReplaysExactly` (`internal/backup/activity_test.go:12–61`) is an activity-publication/recovery test, not a DNS test: its assertions still require failed operation/recovery-required disposition, exact actor/event identity, blocked new work while publication is pending, redacted durable evidence, and exactly-once replay of the identical event. None changed. Destination-change and other shared-fixture tests also retain their original assertions; this correction does not establish production DNS timeout behavior.

Inspected `backup-pending-stack.log` (15-second diagnostic timeout in `net.cgoLookupHost`), `backup-pending-connect-timeout.log` (90-second failure), `backup-pending-local-socket-overlay.log` (three 0.01-second passing executions), and `backup-pending-fixed.log` (the patched original test passes in 0.01 seconds; package 1.058s). Race flags/control environment are owner-reported rather than encoded in these output logs. No additional tests launched by this review, no production/global DNS changes, and no new finding. The owner's full backup package run and coordinator's full-suite rerun remain separate acceptance gates.

## Subsequent coordinator gate completion

Coordinator-recorded update after review: final full `make test` with explicit
disposable PostgreSQL exited 0 (19 tested packages, 1,615 top-level PASS results;
167 explicitly inventoried opt-in/platform/helper skips). The focused #98
race/HTTP/persisted-browser gate passed all 22 top-level tests with zero skips.
Generation, lint, compile and final whitespace checks passed. Exact commands,
logs, earlier failed attempts and skip boundaries are in
`docs/v1-ticket-98-evidence.md`. This update does not replace or expand the
independent reviewer's bounded findings above.
