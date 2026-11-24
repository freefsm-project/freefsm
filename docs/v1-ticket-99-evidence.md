# Issue #99 — integrated quality evidence

## Scope and provenance

This report records integrated checks owned by the quality-gate agent on
2026-11-23, with final inventory/cleanup on 2026-11-24. Feature implementation,
independent review, exports acceptance and browser acceptance have separate owners.

**Result: final integrated full-suite gate PASSED (attempt 03).** Exit 0,
70m16s; 1,667 top-level passes, zero failures, 130 top-level skips. Nested
results: 4,909 passes, zero failures, 38 skips. Source hashes were unchanged
throughout the final run. Post-fix generation, lint, compile and ten focused
race tests also passed. Failed attempt 01 and environment diagnosis are retained
below. Owner-executed final acceptance passed 16 top-level tests and 35 subtests,
zero failures/skips, including the five-export browser journey. The coordinator
reports independent review accepted AC1–11. No commit, stage, push or issue closure
was performed by this agent.

- Worktree: `/home/adam/repos/freefsm/.worktrees/v1-spec-18/ticket-99`.
- Environment: `/tmp/opencode/issue99-evidence-20261123/env.sh`.
- Evidence root: `/tmp/opencode/issue99-evidence-20261123`.
- Full-suite database: `postgresql://issue99_app@127.0.0.1:37141/freefsm_issue99?sslmode=disable`.
- Installation and backup admin URLs: `postgresql://issue99_admin@127.0.0.1:37141/postgres?sslmode=disable`.
- This agent does not use the focused, acceptance, exports or dashboard databases.
- `BROWSER_TESTS=0` for the integrated suite; browser execution is separate evidence.

## Integrated attempt 01

Logs and metadata are under `logs/integrated-01/` relative to the evidence root.
`start.txt`, `head.txt`, `status-before.txt`, `diff-before.patch` and
`source-before.sha256` identify the initial dirty implementation state, including
untracked source content hashes. Other owners were actively working during this
attempt; subsequent changes were accounted for in later checks below.

Review handoff during attempt 01: the coordinator reported that malformed
POST-body filter parse errors can be swallowed by nosurf/cached `PostForm`.
The UI owner was implementing strict pre-CSRF financial-export decoding and a
regression test. Attempt 01 does not establish final verification of that fix;
the later checks record its completed state with fresh timestamps/source hashes.

| Check | Command after sourcing env.sh | Status |
|---|---|---|
| Generation | `GOFLAGS=-p=2 make generate` | Exit 0; 5s; completed 15:22:43Z |
| Lint | `GOFLAGS=-p=2 make lint` | Exit 0; 15s; completed 15:22:58Z |
| Compile | `GOFLAGS=-p=2 make BUILD_DIR="$ISSUE99_EVIDENCE_ROOT/build" compile` | Exit 0; 39s; completed 15:23:37Z |
| Full race suite | `FREEFSM_TEST_DATABASE_URL='postgresql://issue99_app@127.0.0.1:37141/freefsm_issue99?sslmode=disable' BROWSER_TESTS=0 GOFLAGS='-p=2 -count=1 -timeout=120m' make test` | Exit 2; 440s; completed 15:30:49Z |

Each gate retains its complete `.log`, exact `.command` and `.result` (exit status,
elapsed seconds and UTC completion). Failed attempts are retained. Generation
content changes are captured in `generate-source-changes.diff`; this agent will
report unexpected changes rather than restore or overwrite another owner's work.

Generation exited successfully but was **not clean**: three unrelated generated
templates changed (`assets_show_templ.go`, `customer_children_templ.go`,
`customers_show_templ.go`), totaling 176 insertions and 176 deletions. Their diff
is preserved as `unexpected-generated.patch` and reported to the coordinator;
they were subsequently proven generator-only and restored as documented in the
final inventory section. The same five-second interval also saw edits to
export/acceptance test files by concurrent owners, captured in the hash diff;
those are not attributed to the generator. All completion timestamps above are
UTC on 2026-11-23. `build.sha256` identifies the compiled artifact at completion.

## Integrated attempt 02 — malformed-body fix

The coordinator reported the UI fix complete at **15:25:16Z**, affecting
`middleware/auth.go`, the new financial-export form middleware/tests,
`handlers/financial_exports.go` and presentation tests. This agent's follow-up
logs are under `logs/integrated-02-post-body/`, with before/after source hashes,
exact commands, results and the new binary hash. All dates are 2026-11-23 UTC.

| Check | Exit | Duration | Completed |
|---|---:|---:|---|
| `GOFLAGS=-p=2 make generate` | 0 | 4s | 15:26:15Z |
| `GOFLAGS=-p=2 make lint` | 0 | 0s (rounded) | 15:26:15Z |
| `GOFLAGS=-p=2 make BUILD_DIR="$ISSUE99_EVIDENCE_ROOT/build" compile` | 0 | 16s | 15:26:31Z |
| Focused middleware/handler race tests | 0 | 39s | 15:27:11Z |

Generation changed no source content in this attempt. Focused tests ran with
`FREEFSM_TEST_DATABASE_URL=''` because these ten tests do not use a database,
avoiding interference with the active full suite. The exact anchored selection
is in `focused.command`: `TestFinancialExportBodyBeforeCSRF` plus all nine tests
in `financial_report_presentation_test.go`. **10 top-level passes, 0 failures,
0 skips**; original output and count/name inventory are retained in `focused.log`
and `focused-counts.txt`. This independently covers the cached-malformed-body
regression and pre-CSRF middleware cases, not the separately owned real-DB browser
acceptance.

During this attempt, the core owner added `financial_reference_history_test.go`
and edited `financial_reports_integration_test.go`; see `source-changes.diff`.
Those tests were outside this focused selection and are not claimed verified by
this attempt. Later core-test completion is substantiated below and by attempt 03.
Attempt 01 subsequently completed with the failures below.

## Test accounting and skips

Attempt 01 produced **498 top-level passes, 2 top-level failures, 8 top-level
skips**; nested results: **2,811 passes, 1 failure, 0 skips**. These are observed
verbose-log result lines, not a complete successful suite. The handlers package
failed to build, so its tests did not execute and are neither passes nor skips.
`test-counts.txt` and `test-skips.tsv` retain the accounting and original reasons.

All eight skips, categorized:

| Category | Tests | Reason |
|---|---|---|
| Browser opt-in (4) | `TestInstallationBrowserRecoveryAndCoreWorkflows`, `TestBackupBrowserWorkflow`, `TestInteractionRecoveryNoticeBrowser`, `TestDocumentLineEnterBrowser` | Browser tests disabled for this invocation; documented Chromium/dependency opt-in |
| Subprocess-only helper (3) | `TestAuthorizationRestartProbe`, `TestBackupCrashChild`, `TestExactSourceHistoryInitializer` | Direct entry points skip outside their parent-driven subprocess mode |
| PostgreSQL version negative case (1) | `TestStartupRejectsUnsupportedServerBeforeBootstrap` | Requires disposable PostgreSQL older than 16 |

### Retained failures and exact reproduction

Source `env.sh` and execute the exact full-suite command in the attempt 01 table
(also retained in `test.command`). No fixture/code repairs were made by this agent.

1. `internal/backup`: `TestBackupCreateUnsupportedLayout/extensions` failed at
   `preflight_integration_test.go:33`: `extension "backup_preflight_fixture" is
   not available (SQLSTATE 0A000)`. Parent test failed; other two subtests passed.
   Focused reproduction: `GOFLAGS=-p=2 go test -race ./internal/backup -run
   '^TestBackupCreateUnsupportedLayout$' -count=1 -v` with the same sourced admin URL.
2. `internal/exports`: `TestFinancialExportDurableFiveFacetsHistoryAndUnknown`
   failed at the then-current `financial_integration_test.go:224`: `invoice hide
   requires reverted conversion cycle (SQLSTATE P0001)`. Focused reproduction
   against that captured source state: `GOFLAGS=-p=2 go test -race ./internal/exports
   -run '^TestFinancialExportDurableFiveFacetsHistoryAndUnknown$' -count=1 -v`.
   The adapter owner's later corrected test execution is attributed below; it
   does not erase this failed attempt.
3. `internal/handlers` did not build: `vet: internal/handlers/financial_exports.go:62:24:
   undefined: middleware.DecodeFinancialExportForm`. Source files changed during
   compilation; attempt 02 successfully built/tested the completed middleware fix.
   This attempt did not run handlers; attempt 03 subsequently supplied that coverage.

`test-source-changes.diff` captures changes during the full suite. This run began
before the middleware completion and core/adapter test additions, so it is not
final-state validation. The logged `panic: web panic` is from a passing panic
recovery test and is not counted as a separate suite failure. Failures were reported
to the coordinator immediately on completion notification.

## Separately owned acceptance

The core owner's `docs/v1-ticket-99-reconciliation.md` records nine focused tests
passing with zero skips in `logs/core-A-final-focused-rerun.log`, plus earlier
failed attempts. This is attributed evidence, not an independent rerun by this
agent. Completed HTTP/browser/export acceptance and independent review results
are attributed below; earlier in-progress logs were never treated as passes.

### Core review follow-up (owner-executed, logs inspected)

The coordinator reports the core review changes are test-only and complete.
Inspection of `logs/core-A-review-history-second.log` shows **13 top-level
passes, 0 failures, 0 skips**. `logs/core-A-review-strict-refund.log` separately
shows **1 pass, 0 failures, 0 skips** for
`TestFinancialReportImmutableCashFIFOAndPeriodIndependence`; this rerun overlaps
the focused suite and is not a fourteenth distinct test. These cover strengthened
exact-one event/provenance assertions and four archived/hidden/reference cases.
The owner also reports successful lint (`logs/core-A-review-make-lint.log`, empty
output); see their command/results account in the reconciliation document. This
agent did not independently execute these focused runs or observe their shell
exit statuses; the reported success is attributed to the core owner/coordinator,
with the test outcomes corroborated from retained logs.

Comparison against `integrated-01/source-before-test.sha256` confirms
`internal/authorization/financial_reference_history_test.go` was absent at full
suite start and `financial_reports_integration_test.go` has changed since then;
`internal/services/financial_reports_test.go` is unchanged. The owner-executed
focused reruns provide the subsequent coverage, rather than a claim that the
older full-suite source snapshot included these additions. No production-code
change was reported in this handoff.

### Adapter review follow-up (owner-executed, logs inspected)

Evidence: `docs/v1-ticket-99-export-contracts.md` and
`/tmp/opencode/issue99-export-adapter-logs/`. Inspection of `exports-race-final.log`
shows 21 top-level passes; `financial-selective-restart-race.log` shows two further
passes. The union of test names is **23 distinct top-level package tests,
including 12 financial tests**, with zero failures/skips in these two runs.
This is a union across invocations, not one 23-test run; overlapping earlier
attempts are not counted again. Owner-reported exits are 0. Earlier failed
attempts remain in that directory and are explained in the owner's contract doc.

The two later tests cover selective `credit.export` revocation denying the whole
Ready combined artifact and actual Running-worker interruption/recovery with
exactly one audit. `financial_integration_test.go` differs from attempt 01's
start snapshot; the coordinator reports the final additions are test-only.
These later owner-executed runs substantiate the changed tests, without changing
the failed status of integrated attempt 01.

### Final HTTP/browser acceptance (owner-executed, completed logs inspected)

| Retained log under `logs/` | Outcome | Package duration |
|---|---|---|
| `acceptance-E-fifth.log` | Failed: 13 top-level passes, 3 failures; 35 subtest passes; no skips | 77.684s |
| `acceptance-E-sixth-targeted.log` | Passed: 3 top-level tests, zero failures/skips | 12.080s |
| `acceptance-E-final.log` | Passed: **16 top-level tests + 35 subtests**, zero failures/skips | **66.399s** |

The final run includes `TestFinancialAcceptancePersistedBrowserFiveExports`.
Its log records all five actual CSV downloads and receipt recovery after browser
restart. The mandatory `BROWSER_TESTS=1` guard plus execution output corroborates
browser execution. Race mode, invocation and successful shell exit 0 are
owner/coordinator-attested: no contemporaneous command/result sidecar was found,
and this report does not invent one. Go's final `PASS` and package `ok` are directly
observable. The final lint exit 0 is also owner-reported; retained logs include
`acceptance-E-lint-final.log` and `acceptance-E-lint-complete.log`.

Reconstructed **reproduction command**, not a preserved original invocation
(the exact original timeout/flag spelling is not independently known):

```sh
source /tmp/opencode/issue99-evidence-20261123/env.sh
FREEFSM_TEST_DATABASE_URL="$ISSUE99_ACCEPTANCE_DATABASE_URL" \
  BROWSER_TESTS=1 GOFLAGS=-p=2 go test -race ./internal/handlers \
  -run '^TestFinancialAcceptance' -count=1 -timeout=30m -v
```

Fifth-run failures were the persisted-browser test, all-row/exact-total/escaping
test, and earlier-installation preservation test. Owner test-only corrections
awaited receipt navigation and coordinated click/download without increasing the
20-second timeout; checked CSV numeric-ID ordering rather than labels; and seeded
actual allocations/applications in the upgrade fixture. Earlier failed/interrupted
attempts remain retained. The three targeted successes overlap the final 16 and
are not counted as additional distinct tests.

Before final generator cleanup, all **2,007** current source files matched both
successful full-suite manifests, including the final acceptance sources. Thus
there is no unverified later acceptance-test delta relative to attempt 03.

Integrated `git diff --check` exited 0 at 15:31:55Z, with output/status retained
in `logs/integrated-01/final-diff-check.{log,result}`.

## Diagnosis and integrated attempt 03

Tight focused reproductions used the exclusive default database and sourced
admin URLs; logs/results are under `logs/integrated-diagnosis/`:

| Reproduction | Exit | Duration | Completed UTC |
|---|---:|---:|---|
| Backup extension subtest only | 1 | 2s | 15:32:37Z |
| Previously failing durable five-facet export test | 0 | 10s | 15:32:47Z |
| Handler malformed cached-body test (including package compilation) | 0 | 4s | 15:32:51Z |
| Complete backup unsupported-layout test, first environment attempt | 1 | 5s | 15:33:37Z |
| Complete backup unsupported-layout test, corrected environment | 0 | 6s | 15:33:52Z |

The initial backup reproduction repeated the exact missing-extension error.
Ranked hypotheses were missing fixture files, wrong installation/search path,
and unreadable/misnamed files. Inspection found PostgreSQL 18.6 using
`extension_control_path=$system` with no available `backup_preflight_fixture`.
The repository already supplies the empty non-superuser extension in
`internal/backup/testdata/extension/`; no new fixture or production code was needed.

Copied those two files into the evidence root's `extension-fixtures/extension/`
and set **only the issue99-owned cluster's** `extension_control_path` to
`$system:/tmp/opencode/issue99-evidence-20261123/extension-fixtures`, followed by
configuration reload. The cluster-local prior `postgresql.auto.conf` is preserved
in the diagnosis directory. No host shared extension directory was modified,
no cluster restart occurred, and no extension was installed in another owner's
database. Initial provisioning incorrectly used a comma separator, producing
`component in parameter "extension_control_path" is not an absolute path`;
that failed test/log is retained. Changing only the separator to a colon made
the original parent test and all three subtests pass (zero skips), establishing
the environment prerequisite as the cause.

The previously failing export fixture now passes on current owner-corrected
source, and the handler package compiles and its regression passes after the
completed middleware change. This agent edited neither implementation nor tests.

Full-suite rerun completed under `logs/integrated-03-final/`, using the same
explicit database, `BROWSER_TESTS=0`, race detector, uncached test count and
120-minute per-package timeout. It has fresh start/source snapshots and retains
its complete command/log/result. Attempt 01 remains failed historical evidence.

### Final full-suite results

- Start: **2026-11-23 15:34:02Z**; finish: **16:44:18Z**.
- Exit **0**; duration **4,216 seconds (70m16s)**.
- Top-level: **1,667 passes / 0 failures / 130 skips**.
- Nested: **4,909 passes / 0 failures / 38 skips**. A parent pass may contain
  skipped subtests; no skipped subtest is counted as passed.
- **19 packages passed**; 50 packages reported no test files (not test passes).
- Before/after source manifests are identical (`test-source-changes.diff` empty).
- Complete output: `test.log`; command/status: `test.command`, `test.result`;
  exact skip names, reasons and levels: `test-skips.tsv`; counts: `test-counts.txt`.
- Post-run `git diff --check` exited 0; see `diff-check.log` and `.result`.

All observed final-run skips are categorized here; the TSV names every skipped
test/subtest and preserves its actual reason:

| Category | Top-level | Nested | Reason |
|---|---:|---:|---|
| Browser opt-in | 101 | 23 | `BROWSER_TESTS=0`; requires separate real-browser execution |
| Protected offline key | 18 | 15 | Requires `FREEFSM_OFFLINE_KEY_TESTS=1` with actual Secret Service |
| Subprocess-only helpers | 8 | 0 | Direct helper entry points skip outside parent-driven subprocess mode |
| PostgreSQL negative-version case | 1 | 0 | Requires disposable PostgreSQL older than 16; owned cluster is 18.6 |
| Backup scale opt-in | 1 | 0 | Explicit backup scale acceptance opt-in not enabled |
| Accessibility opt-in | 1 | 0 | Requires `ACCESSIBILITY_TESTS=1` and browser/dependency setup |
| **Total** | **130** | **38** | **None counted as passes** |

Final run includes the stable middleware fix and later core/adapter/acceptance
test sources. It covers database-backed tests with installation/backup admin
URLs configured. Browser and other opt-ins above remain distinct from this
successful non-browser suite; final issue99 browser acceptance must be attributed
to its separate owner and must not be inferred from this result.

Independent auditor qualifications forwarded by the coordinator: acceptance's
second-row failure is server-side, not an application `Rows.Err` injection;
upgrade proof is genuinely schema 124 to current; browser sessions reinject
cookies, proving receipt persistence rather than authentication-cookie persistence.
The 15 witnessed grant-DML races cover five export kinds at acceptance,
generation and download; archive/assignment transitions have separate coverage
and are not represented as those same 15 races. The coordinator reports final
independent reviewer `ses_df6f7458efferwkNxvGPIu9dql` accepted **AC1–11**, with no
additional tests warranted. This is attributed review, not a new review by this
quality-gate agent.

## Final inventory and generator cleanup — 2026-11-24

Proof retained in `logs/integrated-04-inventory/generator-churn-proof.txt`:
the three unrelated generated files matched HEAD byte-for-byte before attempt
01, their `.templ` sources have no changes, and every changed line only added
`internal/templates/` to `templ.Error.FileName`. The changes were unrelated
diagnostic-path churn, not required for issue99. Using the patch tool, restored
only those 176 lines (30 assets-show, 51 customer-children, 95 customers-show).
The files now match HEAD exactly. No unfamiliar/user changes were overwritten.

The original diff remains at `logs/integrated-01/unexpected-generated.patch`;
the exact inverse patch is retained at
`logs/integrated-04-inventory/restore-generator-churn.patch`. Running root-level
generation again can recreate these diagnostic path differences; the earlier
successful generation is recorded truthfully, not claimed clean against HEAD.

After cleanup, **2,004 of 2,007** source hashes match the successful full-suite
state. The only differences are these three restored generated files, enumerated
in `source-final-vs-full-suite.txt`; `source-final.sha256` captures final content.
Acceptance and feature sources remain identical to the successful suite state.

Affected check after restoration:
`source /tmp/opencode/issue99-evidence-20261123/env.sh; GOFLAGS=-p=2 make BUILD_DIR="$ISSUE99_EVIDENCE_ROOT/build" compile`
exited **0**, **16s**, completed **2026-11-24 15:24:25Z**. The command, complete
log, result and binary hash are retained in `logs/integrated-04-inventory/`.
No whole-suite rerun was needed for diagnostic filename-only restoration.

Final `git diff --check` exited **0** at **2026-11-24 15:25:50Z**; its log/result
are retained in the same directory. Evidence inventory was checked against
`git status --short`: **53 paths (11 modified, 42 untracked)**, none staged.

Exact remaining worktree changes below (`M` = modified, `??` = untracked), also
retained as `logs/integrated-04-inventory/final-status.txt`. Other owners' feature,
test and document artifacts were preserved; this agent's owned worktree document
is `docs/v1-ticket-99-evidence.md`. No migrations or dependency files changed.

```text
 M internal/exports/catalog.go
 M internal/exports/recovery.go
 M internal/handlers/exports.go
 M internal/handlers/router.go
 M internal/handlers/work_exports.go
 M internal/middleware/auth.go
 M internal/services/dashboard.go
 M internal/services/dashboard_settlement_test.go
 M internal/templates/dashboard.templ
 M internal/templates/dashboard_templ.go
 M internal/templates/navigation.go
?? docs/v1-ticket-99-evidence.md
?? docs/v1-ticket-99-export-contracts.md
?? docs/v1-ticket-99-interfaces.md
?? docs/v1-ticket-99-reconciliation.md
?? internal/authorization/financial_reference_history_test.go
?? internal/authorization/financial_reports.go
?? internal/authorization/financial_reports_integration_test.go
?? internal/exports/financial_activity.go
?? internal/exports/financial_adapters.go
?? internal/exports/financial_contracts.go
?? internal/exports/financial_contracts_test.go
?? internal/exports/financial_integration_test.go
?? internal/exports/financial_provenance.go
?? internal/exports/financial_references.go
?? internal/exports/financial_rows.go
?? internal/handlers/financial_acceptance_aging_test.go
?? internal/handlers/financial_acceptance_authority_test.go
?? internal/handlers/financial_acceptance_browser_test.go
?? internal/handlers/financial_acceptance_concurrency_test.go
?? internal/handlers/financial_acceptance_failures_test.go
?? internal/handlers/financial_acceptance_history_test.go
?? internal/handlers/financial_acceptance_population_test.go
?? internal/handlers/financial_acceptance_recovery_test.go
?? internal/handlers/financial_acceptance_sources_test.go
?? internal/handlers/financial_acceptance_test.go
?? internal/handlers/financial_acceptance_upgrade_test.go
?? internal/handlers/financial_exports.go
?? internal/handlers/financial_report_filters.go
?? internal/handlers/financial_report_presentation_test.go
?? internal/handlers/financial_reports.go
?? internal/handlers/testdata/browser/financial-reports.cjs
?? internal/middleware/financial_export_forms.go
?? internal/middleware/financial_export_forms_test.go
?? internal/services/dashboard_settlement_integration_test.go
?? internal/services/financial_activity.go
?? internal/services/financial_credit.go
?? internal/services/financial_receivables.go
?? internal/services/financial_reports.go
?? internal/services/financial_reports_test.go
?? internal/templates/financial_reports.go
?? internal/templates/financial_reports.templ
?? internal/templates/financial_reports_templ.go
```
