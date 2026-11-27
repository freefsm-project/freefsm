# Ticket #101 — completed acceptance evidence

Status: **completed; all eleven AC independently accepted; coordinator staging and runtime shutdown completed** (2026-11-26). Final reviewer `ses_de68e5d50ffe5sJdg3eEv0Fuak` approved after actual durable-artifact review and a current 2,144-file manifest match. The coordinator will restage this final documentation update and verify indexed source against the final frozen inputs. This documentation update does not rerun tests. See [reconciliation](v1-ticket-101-reconciliation.md), [plan](v1-ticket-101-plan.md) and [interfaces](v1-ticket-101-interfaces.md).

## 2026-11-26 addendum — evidence loss and authorized follow-up

Coordinator-reported restart status: `/tmp/opencode/issue101-evidence-20261125` vanished; its linked raw artifacts, main PostgreSQL data and logs are unavailable now. Repository changes and `/home/adam/.cache/freefsm-issue101-scratch-20261125` survived. Scratch survival does not establish recovery of the missing artifacts or main database.

All earlier results, review milestones, artifact links and statements that evidence is “retained” or “available” below are historical reported records, not freshly reverified results or claims of present artifact availability. In those records, `$ISSUE101_EVIDENCE_ROOT` denotes the old `/tmp` root. Preserve their failures, interruptions, counts and limitations; no recovery is claimed. The old `env.sh` command template is historical and cannot supply the rebuilt environment.

The user-authorized narrow Dispatch production contrast fix and fresh final qualification are complete. New durable evidence under `/home/adam/.cache/freefsm-issue101-evidence-20261126` is authoritative, not a recovered copy of the old `/tmp` evidence. Historical pending/blocker statements below are superseded by the latest results here.

## Latest authoritative final results — accepted

Provenance: durable-root `f-final-qualification-handoff.md` and `final-qualification-20261126T171148Z/` (F). Actual F/`summary.json` records the results below; exact commands, logs, numeric exits, UTC timestamps and before/after manifests accompany each gate. Final reviewer `ses_de68e5d50ffe5sJdg3eEv0Fuak` independently inspected artifacts and verified the current 2,144-file manifest match; all gates used byte-identical before/after manifests. External `acceptance-review.md` records the coordinator-supplied verdict and verified facts.

| Latest gate | Completed result | Durable evidence (relative to F) |
| --- | --- | --- |
| Repository race suite | Exit 0; 1,839 top-level + 5,030 subtest passes; 9 expected top-level skips; zero failures/race reports; all 69 expected package results | `full-race.{command,log,exit}`, `summary.json`, `expected-packages.txt` |
| Mandatory non-race scale companion | Exit 0; 1 pass, zero failures/skips; full 1 GiB (1,073,741,824 logical bytes); 1,074,932,630 bytes transferred each way; peak app + PG-child RSS 587,100,160 < 805,306,368 bytes | `backup-scale.{command,log,exit,json}` |
| Final-source PostgreSQL 15 rejection | Exit 0; 1 pass, zero failures/skips | `pg15.{command,log,exit}` |
| Final accessibility | Both browsers; 36 routes, 524 snapshots, 128 interaction states, 7,709 passing text measurements; zero violations | `accessibility/accessibility.json`, `full-race.log` |
| Ticket-specific acceptance | All 52 ticket-specific tests passed in final run, independently verified | `full-race.log` |
| Final quality gates | Build, lint, strict templ freshness, whole-tree gofmt and diff checks passed | `build-final.*`, `lint-final.*`, `templ-check-final.*`, `gofmt-final.*`, `diff-check-final.*` |

The race command was `GOFLAGS='-p=1 -count=1 -timeout=180m -skip=^TestBackupBrowserNginxScale$' make test`, with all required opt-ins enabled. **Exactly `TestBackupBrowserNginxScale` was excluded from race execution**, absent from run/pass/skip counts; its mandatory same-source non-race full-1-GiB companion passed. This is not every test passing under race instrumentation. Eight direct helper skips have passing parent/subprocess scenarios in `helper-parent-passes.json`; the ninth is the PG18 unsupported-version skip, reconciled by the final PG15 pass. No opt-in skips or missing packages remain.

The authorized Dispatch fix is five scoped CSS lines in `cmd/freefsm/static/css/interaction.css`; final measurements pass without weakened assertions, thresholds or scale. Independent source/standards reviews and final artifact review approved the result. Final mechanical reconciliation was independently verified: three generated templates (`assets_show`, `customer_children`, `customers_show`) differ only in templ error-location metadata; one existing assertion in `operational_activity_integration_test.go` has argument-spacing-only formatting. Retaining generated metadata satisfies strict freshness. See F/`final-reconciliation.json`, generated diffs and prebuild archive.

## Historical execution record — superseded, old raw evidence unavailable

All sections through the historical required-check templates below preserve earlier reports, failures and plans. Their present-tense blockers, pending gates and availability claims describe that historical point only; they do not override the accepted final results above.

## Approved scope and baseline

- Approved issue: `freefsm-project/freefsm#101`; parent #18. Approved exact base: `e703bc55a0ffbfe890b8f20fb37f4afae9c886a5`.
- Branch: `implement/v1-101`; worktree: `/home/adam/repos/freefsm/.worktrees/v1-spec-18/ticket-101`.
- Native dependencies #74 and #96 are completed and ancestral; #100 is completed and included. #94 and #97 remain open, assigned, and excluded.
- Authority: `/tmp/opencode/issue101-evidence-20261125/{base-ancestry.json,execution-contract.md,HANDOFF.md}`. Approved issue snapshots are retained there as `issue101-approved.json` and `prd18-approved.json`.
- Preserve prior worktrees, root modifications, and retained #99 evidence/runtime data. Do not revive the retired generalized harness. No commit, push, or issue closure is authorized.

## Disposable runtime and retained evidence

Evidence root: `/tmp/opencode/issue101-evidence-20261125` (`$ISSUE101_EVIDENCE_ROOT` after sourcing `env.sh`). Exact environment and executable paths live in that file; setup and shutdown instructions live in `HANDOFF.md`.

| Resource | Location / recorded state |
| --- | --- |
| PostgreSQL 18.6 | Loopback `59441`, database `freefsm_issue101`, restricted app role `issue101_app`; admin `issue101_admin` |
| Main data/socket/log | Evidence-root `pgdata/`, `socket/`, `logs/postgres.log` |
| Extension/browser tooling | `extension-fixtures/extension/`, `browser/`, `selenium/`; shared Chromium read-only, system Firefox |
| Disk scratch | `/home/adam/.cache/freefsm-issue101-scratch-20261125`; `tmp/` is TMPDIR; retained pointer `scratch-path.txt` and `disk-scratch` link |
| PostgreSQL 15.19 | Container `freefsm-issue101-pg15-1795620327`, loopback `37417`, database `freefsm_issue101_pg15`; scratch `pg15data/` retained |
| Outputs | Evidence-root `logs/`, `build/`; configured `accessibility/` and `backup-scale.json` are output destinations, not completed results |

Handoff records initial smoke success: app-role backup extension CREATE/DROP, Chromium and Firefox launch/page-title, and actual desktop Secret Service `ProtectedStore` create/save/read/update/delete/key-absence checks. Secret Service log: `logs/secret-service-crud.log`; disposable smoke key/cache were deleted. These establish prerequisites only. The desktop Secret Service is shared and must remain unlocked; do not stop it.

Disk prerequisite resolved with approximately 608 GiB free (`logs/disk-scratch-resources.log`). Memory pressure requires serialized checks (`GOMAXPROCS=2`, preferably `-p=1`), no competing suites or shared-app-DB tests. Runtime liveness has not been rechecked by this documentation task.

Independent readiness audit `ses_decab69bdffeFVkoaKToZzH186` records no identified environmental blocker in `$ISSUE101_EVIDENCE_ROOT/verification-readiness.md`; this is readiness, not a test result. `env.sh` now includes `FREEFSM_BACKUP_TEST_GB=1` (the separate archive branch) and `CGO_ENABLED=1`, alongside existing opt-ins. PostgreSQL server/dump/restore versions match at 18.6. Keep the >=1-GiB datasets unchanged; leave `FREEFSM_BACKUP_BROWSER_BYTES` and `FREEFSM_INSTALLATION_BINARY` unset for full verification. Serialize full tests; the 180-minute timeout is per package.

## Recorded failed attempt — not a test pass

The authorized PostgreSQL 15 focused attempt ended **2026-11-25T15:26:49Z**, exit **1**, elapsed **28 seconds**. Compilation was blocked during ongoing implementation:

```text
internal/authorization/lease_report.go:78:16: undefined: NormalizeLeaseExportFilter
internal/authorization/lease_report.go:78:43: undefined: leaseExportFilter
```

**Zero tests executed; zero passes; zero skips.** Full expanded command, output, and status: `$ISSUE101_EVIDENCE_ROOT/logs/pg15-focused.command`, `pg15-focused.log`, `pg15-focused.result`. No full suite ran in the supplied handoff. No retry or source correction was performed by this documentation task.

## Completed narrow adapter checks

Source: `$ISSUE101_EVIDENCE_ROOT/adapter-unit-handoff.md`. Captured rerun explicitly removed the database URL:

```bash
env -u FREEFSM_TEST_DATABASE_URL bash -o pipefail -c 'go test ./internal/exports -run "^TestLeaseExport(FixedContracts|StrictFilters|StrictReferences|MalformedRequiredReferencesAdapter|OperationalIsolationAndHistory|BillingRowsExactCentsAndProvenance|SummaryAndEmitFailure|UnavailablePositionAndOriginalRevision)$" -count=1 -v 2>&1 | tee /tmp/opencode/issue101-evidence-20261125/adapter-unit.log'
env -u FREEFSM_TEST_DATABASE_URL bash -o pipefail -c 'go vet ./internal/exports 2>&1 | tee /tmp/opencode/issue101-evidence-20261125/adapter-vet.log'
```

Both exited **0**: **8 pure tests passed, 0 failed, 0 skipped**, package time 0.011s; package vet succeeded without diagnostics (empty retained vet log). Exact logs are the two absolute paths above. Earlier uncaptured checks are described in the handoff, not additional retained proof. This initial narrow run included no database integration tests; later DB results follow. It does not qualify final source/full-suite acceptance.

## Historical completed intermediate results

Paths below are relative to `$ISSUE101_EVIDENCE_ROOT`. These completed results are not final acceptance. D run03, E green02 and F focused execution are complete; F exposed a preexisting Dispatch production-contrast blocker. F released the DB with no active tests or browser/driver processes reported in `f-final-handoff.md`.

| Check | Completed result | Retained evidence |
| --- | --- | --- |
| A domain | 19 top-level + 57 subtests passed, zero failures/skips; 16.611s | `logs/A-domain-final.log`, `intermediate-completed-checks.json`; companion `logs/A-vet-final.log` |
| A report denial | 1 top-level + 1 subtest passed, zero failures/skips | `logs/A-report-denial-final.log` |
| A selected-reference focused checks | 12 top-level + 12 subtests passed, zero failures/skips; includes original archived authority on empty Customer/Location/Asset selections | `logs/A-selection-reference-1.log`; companion `logs/A-selection-reference-vet.log` |
| B adapter DB latest 02 | Exit 0; 10 top-level tests + 2 integration subtests passed, zero failures/skips, no race diagnostics; 5.585s | `adapter-db-handoff.md`, `adapter-db-02.log`, `adapter-db-02.exit` |
| PostgreSQL 15 focused 02 | Exit 0; 1 test passed, zero skips; completed 2026-11-25T15:46:14Z, elapsed 16s | `logs/pg15-focused-02.command`, `.log`, `.result` |
| Repository intermediate build/lint 01 | Both exit 0; not final-source gates | `logs/integration-build-01.log`, `.exit`; `logs/integration-lint-01.log`, `.exit` |
| D acceptance run03 | Exit 0; 30 top-level + **52** subtests passed, zero skips; 106.898s | `d-handler-run03/{command.txt,test.log,exit-status.txt,summary.json,source-before.sha256,source-after.sha256}`, `d-final-handoff.md` |
| C shared-report regression05 | Exit 0; 9 top-level + 7 subtests passed, zero failures/skips; 21.585s | `c-report-regression05/{command.txt,test.log,exit-status.txt,summary.json,source-before.sha256,source-after.sha256}`, `c-report-final-handoff.md` |
| Parent compile/lint 02 | Both exit 0 | `logs/final-compile-02.{command,log,exit}`, `logs/final-lint-02.{command,log,exit}` |
| Changed-Go formatting | Exit 0; all 25 changed Go files clean | `logs/final-format-check.json` |
| E activity/interaction green02 | Exit 0; each test passed 10/10, **20 passes, zero skips**, package 100.795s | `logs/navigation-fix01-green02.log`, `logs/navigation-fix01-green02.exit` |

**D count correction and retained attempts:** the earlier 53-subtest figure was a derived-metadata regex error: `^\s+--- PASS` crossed a newline; `^[ \t]+--- PASS` correctly counts 52. Raw logs/manifests/status are unchanged. Retain run01, fix01, run02 and their failures, plus successful focused fix02. Adjacent01 was interrupted, not completed. Adjacent02 and adjacent-focused01 failed the retained unowed-period report assertion; their logs remain available.

**C supersession:** final shared-report fix preserves human-readable Reason after due date, separately shows canonical state, and retains the ordinary browser due-by-selected-date cutoff while explicit Pending/Failed selections include lookahead details. CSV/domain projection and tests were unchanged. Focused04 (1 pass, zero skips) and regression05 supersede the adjacent failure and C focused01/02 failures; diagnostic probe03 remains retained and its instrumentation was removed. Final D and C runs each have identical before/after manifests. Coordinator reports independent Spec and Standards approval of the final template change.

## Historical full-race-01 — failed; superseded by fresh final gates

Full all-opt-in execution completed **exit 2**, elapsed **6649.09s**. Before/after manifests cover **2031 identical source files**: `full-race-01-source-before.json` and `full-race-01-source-after.json`. Exact command/output/status/timing are retained in `logs/full-race-01.command`, `.log`, `.exit`, `.time`, `.started`, `.completed`. Failure artifact snapshot: `full-race-01-artifacts/` (`accessibility/` and `backup-scale.json`).

Exact line-based counts in `logs/full-race-01.summary.json`: **1836 top-level passes, 4 failures, 9 skips; 5030 subtest passes, zero subtest failures/skips**. Later focused successes do not rewrite these full-run results.

The command sourced `env.sh`, explicitly passed `FREEFSM_TEST_DATABASE_URL`, enabled browser/accessibility/offline-key/browser-scale/GB opt-ins at **1**, and used `GOFLAGS='-p=1 -count=1 -timeout=180m' make test`; backup byte-size and installation-binary overrides were unset. Environment supplies CGO and serialized runtime settings.

Four failed tests:

- `TestActivityExportPersistedBrowser`
- `TestBackupBrowserNginxScale`
- `TestSharedAccessibilityPersistedBrowser`
- `TestAccessibleInteractionPersistedBrowser`

All Lease checks and other packages passed; the repository suite is **not green**. Historical scope audit (2026-11-25, before E green02) by `ses_dec0b2a13ffeISo4Gjcp0xsuD6` established base-identical affected scripts/production paths, identified backup/accessibility stale oracles, strongly implicated Activity HTMX timing and left interaction diagnosis open. That last uncertainty is now resolved as described below; the historical failures remain retained.

**Latest E result and independent review:** `TestActivityExportPersistedBrowser` and `TestAccessibleInteractionPersistedBrowser` each passed 10/10 in green02. Reviewer `ses_dec0b2a13ffeISo4Gjcp0xsuD6` verified scope, preserved oracles and actual results, as reported by the coordinator. Interaction's cause is now proven: a preexisting native GET before HTMX settled, evidenced by an absent HX header versus the successful HTMX GET/503 scenario. Corrections are test-only; no production changes. Preserve intermediate failed logs, including the ambiguous-locator `logs/navigation-fix01-green.log`; corrected `logs/navigation-fix01-green02.log` is the latest completed focused result.

**Completed F results and independent review:** `f-final-handoff.md` and reviewer `ses_debb30f8bffeO89Ko1hD7KQD8X` establish the following focused outcomes:

- `f-focused03/backup.log` / `backup.exit`: **PASS**, non-race full **1 GiB** acceptance, 61.32s; logical bytes 1,073,741,824, transferred each way 1,074,932,630, peak app plus PostgreSQL-child RSS **585,916,416 bytes**, below the unchanged **768 MiB** budget. Inventory/digest, restored login, disabled email and restart/recovery assertions passed. `docs/backup-browser-acceptance.md` documents non-race memory acceptance; this does not establish race-instrumented memory acceptance.
- `f-accessibility07/test.log` / `exit`: completed race-enabled **FAIL**, 142.70s, **four grouped contrast violations across Chromium and Firefox**, with no race report. Both browsers completed 36 routes, 524 snapshots, 128 interaction states and 7,693 passing text measurements. Dynamic Subtasks and populated Dispatch controls were exercised; native Firefox keyboard date entry and persisted Dispatch save are not established.
- Actual preexisting product contrast blocks final acceptance: light status badge **2.44:1**, dark Dispatch text **1.7:1**, and dark status badge **1.53:1**, against **4.5:1**. Reproduce with an unscheduled Job with Customer/title at `/schedule?tab=dispatch&period=day`. Measurements/screenshots: `f-accessibility07/accessibility/`. Relevant production paths match base `e703bc55` by source comparison; no separate base execution is claimed.
- Independent review verified equal old/new measurement snapshots in `f-measurement-parity06`, no assertion/threshold/scale weakening, and the valid geckodriver `ServiceBuilder --allow-system-access` correction. No performance speedup claim is made. Completed focused03/accessibility05/accessibility07 runs retained identical before/after tracked-source manifests.

**Retained F failures/interruption:** `f-focused01/test.log` records both race tests failing: backup transfer/restore succeeded but RSS was **1,755,860,992 bytes**, above budget; accessibility hit its original five-minute deadline. `f-focused02/interrupted.txt` records server-restart interruption with no exit marker. `f-focused03/accessibility.log` failed at the 20-minute authorization-route deadline. `f-authorization-probe04/test.log` exposed the rejected Firefox capability flag; its temporary route restriction was removed. `f-accessibility05/test.log` completed with an authorization target/zoom stall, not a restart interruption. All evidence remains retained; latest focused results do not rewrite full01.

**Decision blocker and remaining gates:** a narrow Dispatch production contrast fix requires explicit user scope authorization. F made test-only changes, released the DB and reported no active tests/browser/driver processes. Full-race-02 is not ready-to-pass; frozen-source quality checks, PostgreSQL 15 gate, independent final AC acceptance, staging and shutdown remain pending. No full-suite green, staging, shutdown or commit is claimed.

Nine skips: eight helper direct entries (`TestAuthorizationRestartProbe`, `TestBackupCrashChild`, `TestExactSourceHistoryInitializer`, `TestAssetIdentityRestartProbe`, `TestProtectedAuditWorkerProcess`, `TestCustomerExportWorkerProcess`, `TestTransferRestartProbe`, `TestNotesReceiptRestartProbe`) and `TestStartupRejectsUnsupportedServerBeforeBootstrap` on PostgreSQL 18. Reconcile helpers with their parent scenarios; the final frozen-source PostgreSQL 15 rerun remains pending. Preserve all failed/superseded logs.

**Retained B failure and supersession:** `adapter-db-01.log` / `.exit` record exit 1: operational integration and nine unit tests passed, but Billing's fixed 104-record assertion used a stale Draft fixture expectation. Billing correctly emitted unavailable-generation period rows. The B-owned fixture now asserts 103 Lease rows plus summary, and exactly 103 correctly ordered/identified unavailable period rows for Billing. Latest 02 supersedes 01 after that fixture correction; no adapter emission/duplicate-selection defect was found. Exact commands and diagnosis are in `adapter-db-handoff.md`. The successful integration also checks durable reconstruction/replay, immutable bytes after Customer edits, and malformed archived-reference rejection.

**PostgreSQL 15 supersession limit:** focused 02 supersedes the initial compile-only failure above for the negative-version case. Unrelated source manifests changed during that run: retain `logs/pg15-focused-02.source-before.sha256`, `.source-after.sha256`, `.source-changes.diff`, and `.manifest-hashes.txt`. Repeat on final frozen source is still required; this is not final provenance.

**C generated cleanup and period-reason checks:** `logs/c-generated-cleanup-disposition.json` records restoration of three unrelated generated templates (`assets_show`, `customer_children`, `customers_show`) whose source matched base and differences were only filename/line metadata. Lease templates were preserved; both Lease templ checks and diff check exited 0. Inspection, applied diff/patch and remaining-file evidence are retained under `logs/c-generated-cleanup-*` and `logs/c-generated-cleanup.diff` / `.patch`. Period-reason build and vet exited 0; exact commands/results/output are `logs/c-period-reason-{build,vet,templ,diff}.{command,result,log}`. These are intermediate checks, not a final build/source freeze claim.

## Historical required-check templates — final gates now completed above

The templates below describe final-source gates, not additional execution claims. Completed runs are recorded above; after E/F fixes, final full-race-02 and frozen-source PostgreSQL 15 verification remain pending, with affected quality checks/review reconciled to final source. Source `env.sh` for the actual PostgreSQL 18 URL, admin URLs, all opt-ins, tooling and disk scratch. Capture each expanded invocation, combined log, exit status, pass/fail/skip counts and source state.

```bash
source /tmp/opencode/issue101-evidence-20261125/env.sh
GOFLAGS='-p=2' make BUILD_DIR="$ISSUE101_EVIDENCE_ROOT/build" build
GOFLAGS='-p=2' make lint
GOFLAGS='-p=1' go test -race ./internal/authorization ./internal/exports ./internal/handlers -run 'TestLease(Report|Export)|TestFieldLease' -count=1
BROWSER_TESTS=1 GOFLAGS='-p=1' go test -race ./internal/handlers -run 'TestLease(Report|Export)' -count=1 -v
BROWSER_TESTS=1 ACCESSIBILITY_TESTS=1 FREEFSM_OFFLINE_KEY_TESTS=1 FREEFSM_BACKUP_BROWSER_SCALE=1 FREEFSM_BACKUP_TEST_GB=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1 -count=1 -timeout=180m' make test
```

`make build` generates source; review resulting changes before final-source qualification. `make lint` is `go vet ./...`; `make test` runs verbose race-enabled tests. Required browser/DB skips are not acceptance. Separately repeat the negative-version case on final frozen source, using PostgreSQL 15 for both relevant URLs:

```bash
FREEFSM_TEST_DATABASE_URL="$ISSUE101_PG15_DATABASE_URL" FREEFSM_INSTALLATION_TEST_ADMIN_URL="$ISSUE101_PG15_ADMIN_URL" FREEFSM_BACKUP_TEST_ADMIN_URL='' GOFLAGS='-p=1' GOMAXPROCS=2 go test -race ./cmd/freefsm -run '^TestStartupRejectsUnsupportedServerBeforeBootstrap$' -count=1 -timeout=15m -v
```

Never run the full suite against PostgreSQL 15. Reconcile its expected main-suite skip separately; distinguish subprocess-only helper entry-point skips from missing coverage.

## Independent review milestones and completed acceptance

- [x] Independent final plan/contracts approval: `ses_decb37114ffepRHa8tSpvxUhhE`, recorded in `$ISSUE101_EVIDENCE_ROOT/plan-review.md`. Historical availability, coherent selected-date position, exact contracts/reference ownership, midnight-safe stored-artifact authorization and owning queue eligibility findings were resolved. No remaining normative conflict or additional user decision was identified. Document approval only; no implementation or acceptance approval.
- [x] Independent B adapter-code review: `ses_deca7e814ffexd06PxLhlCX5OU` approves adapter code only, as reported by the coordinator. No database verification or broader acceptance is implied.
- [x] Standards: `ses_deca69cb1ffe8J4j63rurDKpce` approves production code with no hard blocker; optional naming/discarded-work judgments are not blockers.
- [x] Spec: `ses_deca67b72ffenCJLwZLr386ypD` approves AC1–10 at source level; AC11 execution remains pending.
- [x] Domain: `ses_deca5315cffezhHMgLHxdx4Z1F` confirms both identified defects resolved.
- [x] Reference security: `ses_deca7e814ffexd06PxLhlCX5OU` approves the Selection delta.
- [x] Coverage: `ses_deca1c24fffe3xffDmw2UfJMRT` approves test coverage for all eleven AC, not their execution.
- [x] Documentation: `ses_dec903218ffe5PfQn96MQvjb0L` confirms all three corrections closed, as reported by the coordinator.
- [x] Additional adversarial authority: `ses_dec918b88ffeQuabbseuMySPJW` approves bounded AC1/6/7/8/9 review, as reported by the coordinator; not final execution or whole-ticket acceptance.

These independent review verdicts are coordinator-reported milestones. They do not substitute for final execution evidence or final independent acceptance.

- [x] Independent final-result review against all eleven AC: `ses_de68e5d50ffe5sJdg3eEv0Fuak`, APPROVED after actual artifact review.
- [x] Independent final-source re-review after fixes and generated-source reconciliation; current 2,144-file manifest match verified by the same reviewer.

| AC | Scope | Final independent acceptance |
| --- | --- | --- |
| 1 | Export/base View and independent Billing authority | Independently accepted |
| 2 | Current/as-of lifecycle and coverage | Independently accepted |
| 3 | Operational dates, coverage and placement | Independently accepted |
| 4 | Authorized Billing facts and provenance | Independently accepted |
| 5 | Immutable snapshots and exact June scenario | Independently accepted |
| 6 | Complete filters/order/prefiltered totals | Independently accepted |
| 7 | Archive/links and field-API exclusion | Independently accepted |
| 8 | Consistent durable CSV, requester/expiry | Independently accepted |
| 9 | Independent grants, fences and atomic audit | Independently accepted |
| 10 | Installation/history/grant preservation | Independently accepted |
| 11 | Real DB/browser/API/failure/race checks | Independently accepted |

## Final latest-result reconciliation — completed

The authoritative final table and reviewer approval above supersede historical compile, browser, contrast and freshness failures without rewriting them. Source identity is F/`final-source.sha256`; exact diff and execution are F/`final-diff.patch` and `run-gates.sh`. Acceptance, coordinator staging and dedicated runtime shutdown are complete, as recorded in the coordinator's final handoff below.

## Shutdown and staging — completed

- [x] Coordinator reports shutdown verified in durable-root `shutdown-verification.json`: PostgreSQL 18 has no PID, ports `59441` and `37417` are closed, and the PostgreSQL 15 container is exited. Both database data directories are retained. PostgreSQL 15 `PG_VERSION` was read as `15` via `podman unshare`; the host permission issue does not indicate lost data.
- [x] Coordinator staged 44 reviewed files and passed the cached diff check; durable-root `staged-paths.json` and `staged.diff` record that staged state before this final documentation update. User root changes remain unchanged and ticket #99 remains clean, per coordinator handoff.
- [x] Final shutdown, staging and reconciliation status recorded here. The coordinator will restage these documentation edits and verify indexed source matches final frozen inputs. No commit, push, or issue closure was performed by this task.

This final documentation task edits only this evidence file and the reconciliation document; verification is limited to a scoped document check, with no source edits, tests, git commands, subagents or issue mutations. Operational completion facts above are supplied by the coordinator's handoff.
