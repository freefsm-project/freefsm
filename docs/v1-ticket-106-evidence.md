# #106 — discovery and main-collection state evidence

## Status and scope

**Final qualification PASS, 2026-11-27.** Approved build, whole-repository race qualification with the explicitly separated scale test, both companions, and final independent security/SPEC reviews passed. Production/tests remain frozen. Runtime shutdown is verified complete; changes are staged by the coordinator, who will re-stage this documentation status update. This is #106 qualification, not an overall V1 release claim.

- Approved exact base and current HEAD: `623a8655fe44502db4078da79653e5bb3de67c1d`.
- Branch: `implement/v1-106`.
- Worktree: `/home/adam/repos/freefsm/.worktrees/v1-spec-18/ticket-106`.
- Scope: combined browser search plus the ten main Customer, Contact, Location, Asset, Job, Project, Estimate, Invoice, Lease and Item collections. Related sublists, pickers and report/export contracts remain distinct.
- Normative requirements: #18, with the eleven #106 acceptance criteria mapped below. This does **not** complete #105, which remains blocked by #24; neither issue is included in this qualification.
- No commit, push or issue closure performed. Changes were staged by the coordinator; commit/push require the user's request.

Prerequisite ancestry and actual source were checked for #30 (Custom Fields), #64 (Customer archive cascades), and #73 (Lease workspace). All are present at the approved base; that inspection is not a new blanket runtime acceptance of those tickets. See [prerequisites](issue-106-preflight/prerequisites.md), [approved contract](issue-106-preflight/implementation-contract.md), and [acceptance plan](issue-106-preflight/acceptance-plan.md).

## Runtime and source identity

Durable root, abbreviated **`$E`** throughout:

```text
/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520
```

Commands source the private `$E/env.sh`; database connection settings and environment contents are deliberately not copied here. This provides an isolated PostgreSQL **18.6** cluster, unprivileged application role and separate administrator test connections, unique fixture schemas, private browser tooling and disk-backed short `TMPDIR`. No other ticket's database/runtime is reused. Go is **1.26.11-X:nodwarf5**. See [runtime readiness](issue-106-preflight/runtime-readiness.md).

Actual issue discovery journeys ran on **Chromium 151.0.7922.34**. Final `TestSharedAccessibilityPersistedBrowser` also executed **Firefox 161.0**/geckodriver **0.36.0**, alongside Chromium, and passed; the final race log records both engines' measurements. This does not imply every discovery journey ran on Firefox. The existing purpose-specific production-router/API/browser fixtures were used, not the retired generalized harness.

Renewed whole-tree qualification is recorded under:

```text
$E/results/final-quality-sYZ5x4mb/
$E/results/final-build-q6qWMpDe/
```

The final build's `source-before.json`, `source-after.json` and final quality's `approved-source.json` have identical SHA-256:

```text
de27480e6a410bd996bb180303b02fc71f95ce3d0d347c57b4c0978bf7f6e278
```

This identifies the **manifest**, not a new Git commit. Generation produced no changes. Every final qualification command's before/after comparison found zero production/test changes; authorized changes to the collection-security and integration-SPEC review documents are recorded in per-command `.drift.json` files. The PG15 companion's relevant source hashes also match the approved final build (`approval-inspection.log`). **This authorized evidence-document update adds subsequent docs-only drift** for coordinator reconciliation; it does not change qualified production/tests.

Earlier scoped manifests remain useful for their own runs: `acceptance-final-01.source-{before,after}` matched (`595bf16f3b043d200edaa2e46a2a9c33b29dcb1a2a3eafe418a4d2da0e07cc66`), and the collection review manifests matched (`c1f756725387bbadd7d89cb0b7d9bdbb5d1ba7521ac33fbf32ef971b5a79e93a`). Their scopes and subsequent corrections differ; neither substitutes for renewed final-tree qualification.

## Implementation boundaries

- `internal/authorization/discovery*.go`: current-actor, browser-only discovery; per-type scalar allowlists; independently authorized relationships/facets; literal matching; global rank/count/page and authorized existence in one candidate statement. Current Lease Billing allocation terms include effective revisions and coverage history.
- `internal/liststate/query.go`: strict typed route parsing, explicit empty state, canonical URLs, 500-character query and 16 KiB state bounds, transient-field removal.
- `internal/authorization/list_state*.go`: current company/user/list storage, original-revision CAS and Reset tombstones. `SaveAuthorizedListState` performs fresh query-authority validation and the CAS write in one `WithinActor` transaction/fence. The callback uses its supplied transaction/principal, never nested public reads. Reset remains intrinsic after grant loss. No browsing/presentation business audit is emitted.
- `internal/database/migrations/126_list_state.up.sql`: additive empty preference table; no grants, domain rewrites or synthetic history.
- `internal/authorization/collection_pages*.go`: bounded Job/document/Contact/Location selectors preserving specialized projections. Authority fence precedes Repeatable Read; authorized counts and bounded page projection share a snapshot. Existing Asset/Project ordering gains identity ties; Customer, Item and Lease keep domain-specific contracts.
- `internal/handlers/{search,list_state,collection_list_specs}.go`, main handlers and router: strict URL-over-saved-state resolution, CSRF-protected state/Reset POST, atomic save validation, and all ten main routes. Legacy Lease remembered cookies are no longer authoritative.
- `internal/templates/{list_state,pagination,search_results,...}` and `cmd/freefsm/static/js/interaction.js`: one URL/scroll history owner, fresh Back/BFCache reads, latest-request ordering, explicit prior-query labeling, hidden uncertain/unauthorized rows, retained inputs and recovery. Contact/Location pagination uses working native links when no HTMX fragment target exists.
- Document collections use `NormalizeDocumentCollectionFilter` (UTF-8, 500 runes). Reports/exports retain `NormalizeDocumentListFilter` (UTF-8, historical 1,000-byte limit), including downstream authorization/snapshot validation.

## Eleven acceptance criteria

The exact tests below map the eleven criteria to focused evidence; the final whole-repository gate and reconciled skips are recorded separately below. Test names are fully qualified by their repository packages: discovery/HTTP/browser tests are in `internal/handlers`; state/domain tests are in `internal/authorization`.

| AC | Code and concrete evidence | Actual disposition |
|---|---|---|
| 1. Ten types, Item identity and independent facets | `discovery.go`, `discovery_sql.go`, browser navigation/entry gate; `TestDiscoveryTenTypesProductionHTTP`, `TestDiscoveryFacetAndTenantNoninterferenceHTTP`, `TestDiscoveryBuiltInScalarFacetAuthorityHTTP`, `TestDiscoveryAssignedScopeBeforeRankingHTTP`, `TestDiscoveryFieldAPIFacetAndOfficeBoundary` | Passed in **D1**. Tests assert actual identities, counts, independent facets, company/assignment boundaries and API denial. |
| 2. Authorized scalar fields; exclusions | Explicit discovery field manifest and active/applicable definition joins; `TestDiscoveryLiteralAndRetiredScalarHTTP`, `TestDiscoveryRetiredOptionOnActiveDefinitionHTTP`, `TestDiscoveryCommentsAndFileBytesExcludedHTTP`, `TestDiscoveryBuiltInScalarFacetAuthorityHTTP`; unchanged independent current-rent repro | **D1**, **E1** passed. Retired recorded option labels on active definitions remain searchable; retired definitions and Comment/File content do not. |
| 3. Exact/prefix/substring rank, stable global paging | `discovery_sql.go`; `TestDiscoveryGlobalRankAndPaginationHTTP` (138 mixed rows, sizes 25/50/100, expected ordered identities), `TestDiscoveryFacetAndTenantNoninterferenceHTTP` | **D1** passed. Hidden rows/facets do not influence visible ranking/count/empty results. |
| 4. Authorized Customer relationship matching | Customer relationship scalar branches and safe plain-text match labels; `TestDiscoveryCustomerRelationshipAuthorityHTTP` | **D1** passed. Related-record grants are independent; matching does not expand Customer authority. |
| 5. Lease domain filters and Billing isolation | `discovery_lease.go`, existing Lease workspace predicates, `list_state_query.go`; `TestDiscoveryLeaseProjectionAndFilterIsolationHTTP`, `TestDiscoverySensitivePreferenceCommitOrdersHTTP`, `TestCollectionLeaseExplicitBaseRequiresCurrentBaseViewHTTP` | **D1**, **B1**, **E1** passed. Explicit base rejects Billing-only actors; omitted facet retains legitimate Billing-only default. |
| 6. Paging/sort/archive and conversion exclusions | Main route specs, `collection_pages*.go`, templates; `TestDiscoveryTenCollectionsPageSizesHTTP` (101 records/type), `TestDiscoveryConversionHiddenNeverOrdinaryHTTP`, `TestDiscoveryMalformedAndRestoredAuthorityHTTP`, `TestDiscoveryLiteralZeroItemNoMatchHTTP`, `TestDiscoveryPersistedBrowser` | **D1**, **D2**, **A1**, **R1** passed. SQL bounds independently inspected/EXPLAINed. Existing dedicated archive modes preserved; combined search supplies explicit active/archived/all. |
| 7. User state, URL precedence, Reset and Back | Parser/storage/shared HTTP adapter/history JS; `TestDiscoveryServerStateURLResetHTTP`, `TestDiscoveryTenCollectionsPageSizesHTTP`, `TestDiscoveryQueuedSaveCannotResurrectResetHTTP`, `TestDiscoveryPersistedBrowser`, `TestMainCollectionStateHTTP` | **D1**, **D2**, **A1**, **S1** passed. Explicit URL is a complete override; GET does not write; page/scroll are transient; Reset revisions prevent resurrection. Actual Contact/Location clicks and Back are covered. |
| 8. Fresh authorization; malformed/failure recovery | Domain validators, atomic save and retained-state UI; `TestDiscoveryMalformedAndRestoredAuthorityHTTP`, `TestDiscoveryWholeSearchFailureHTTP`, `TestDiscoveryPreferenceWriteFailureIsWholeFailureHTTP`, `TestDiscoveryGrantLossBetweenReadAndPreferenceCommitHTTP`, `TestDiscoverySensitivePreferenceCommitOrdersHTTP`, `TestCollectionDocumentUnicodeQueryBoundaryHTTP`, `TestAccessibleInteractionPersistedBrowser` | **D1–D3**, **B1**, **R1** passed. Whole-query/preference failures do not masquerade as empty results; invalid UTF-8/501-rune collection queries reject without preference revision changes. |
| 9. Independent grants, commit safety and no browsing audit | `WithinActor`, `SaveAuthorizedListState`, domain validators; `TestListStateAuthorizedSaveFencesViewAndSensitiveFacet`, `TestDiscoverySensitivePreferenceCommitOrdersHTTP`, `TestDiscoveryServerStateURLResetHTTP`, `TestDiscoveryFieldAPIFacetAndOfficeBoundary` | **S2**, **D1** passed. Lock-observed View/archive/Billing/relationship revocation permits only authorized `state,revoke` success or `revoke`-only denial. Presentation state is deliberately unaudited under DISC-06. |
| 10. Non-destructive migration, preserved facts/grants | Migration 126; `TestListStatePopulatedMigrationPreservesFacts`, `TestDiscoveryPopulatedUpgradePreservesHTTPHistory` | **S1**, **D1** passed. Repeated migration compares pre-existing rows/sequences, actual conversion/reversion history, archived/retired values, Lease facts and recorded grants; no synthetic state/backfill. |
| 11. Real DB/browser/API/concurrency and required checks | Production session/router/CSRF/mobile-API fixtures; `TestDiscoveryQueuedGrantRevocationHTTP`, `TestDiscoveryAssignmentSnapshotCountAndRowsHTTP`, `TestDiscoveryQueuedSaveCannotResurrectResetHTTP`, commit races, API and browser tests above | Final build/checks and real-DB race suite passed: **1,899 roots + 5,159 subtests, zero failures, nine root skips** reconciled below. Nonrace 1 GiB scale and PG15 negative companions each passed. No overall release/native-platform claim. |

## Executed checks and exact commands

All groups used the private supplied environment. Commands are shown without connection contents; corresponding durable command records retain expanded invocations. Counts are per run, **not additive unique-test totals**.

| Group | Exact main command / durable record | Outcome |
|---|---|---|
| **D1** complete issue acceptance at its recorded freeze | `bash "$E/commands/acceptance-focused.sh" acceptance-final-01`; expanded: `BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers -run '^TestDiscovery' -count=1 -v -timeout=15m` | Exit 0: **24 roots, 63 including subtests, 0 fail/skip**, 168.471s. Before/after scoped manifests identical. Later zero/Retry corrections require D2/D3, not a retroactive claim that D1 covered them. |
| **D2** affected zero/Retry/browser correction | Child `umask 022`; `bash "$E/commands/acceptance-focused.sh" acceptance-correction-01 '^(TestDiscoveryLiteralZeroItemNoMatchHTTP\|TestDiscoveryPersistedBrowser\|TestAccessibleInteractionPersistedBrowser)$'` | Exit 0: **3 roots, 5 including subtests, 0 fail/skip**, 23.671s; source manifests identical. |
| **D3** genuine stale-GET/CAS-conflict Retry | Child `umask 022`; `FREEFSM_INTERACTION_STALE_GET=1 bash "$E/commands/acceptance-focused.sh" acceptance-correction-forced-01 '^(TestDiscoveryLiteralZeroItemNoMatchHTTP\|TestAccessibleInteractionPersistedBrowser)$'` | Exit 0: **2 roots, 4 including subtests, 0 fail/skip**, 10.703s; source manifests identical. |
| **A1** bounded selectors and main routes | `go test -race ./internal/authorization ./internal/handlers -run '^(TestCollectionPages.*\|TestMainCollectionStateHTTP\|TestMainCollectionPreferenceReauthorizesHTTP\|TestDiscoveryTenCollectionsPageSizesHTTP\|TestNavigationCollectionsFilterBeforePaginationHTTP)$' -count=1 -v -timeout=15m`; `go vet ./internal/authorization ./internal/handlers` | Exit 0: **7 roots + 29 subtests**, 0 fail/skip; vet 0. `$E/logs/collection-pages-frozen.log`, matching results/source hashes. Production SQL traces and EXPLAIN plans establish bounded projections, not constant-time scans. |
| **B1** final Unicode/export/Lease correction | `GOFLAGS=-p=2 go test -race -v ./internal/authorization ./internal/handlers -run '^Test(ListStateDocumentNormalizerUnicodeContract\|CollectionDocumentExportRetainsByteLimitHTTP\|CollectionDocumentUnicodeQueryBoundaryHTTP\|CollectionLeaseExplicitBaseRequiresCurrentBaseViewHTTP)$' -count=1`; `GOFLAGS=-p=2 go vet ./internal/authorization ./internal/handlers ./internal/exports` | Exit 0: **4 roots + 4 subtests**, 0 fail/skip; vet 0. `commands/logs/results/list-state-export-compatibility-final*` and `list-state-export-compatibility-vet-final*`. Export tests execute review, acceptance, worker and CSV download with matching records. |
| **S1** independent parser/storage/migration | `CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS=-p=1 go test -race ./internal/liststate ./internal/authorization -run 'TestParseStrictState\|TestURLPrecedenceAndCanonicalRoundTrip\|TestPersistentIsolationAndUnicode\|TestListState' -count=1 -v -timeout=10m` | Exit 0: **7 roots + 19 subtests**, 0 fail/skip. `$E/state-independent-review-4kUnN6nx/`; scoped manifests identical. This predates HTTP atomic-save composition; see S2/D1. |
| **S2** current atomic state seam | `GOFLAGS=-p=2 go test -race -v ./internal/authorization -run '^TestListStateAuthorizedSaveFencesViewAndSensitiveFacet$' -count=1` | Exit 0: **1 root + 3 subtests**, 0 fail/skip. `list-state-atomic-authority-2` artifacts include both discovery and dedicated-validator grant/facet rejection and intrinsic Reset. |
| **E1** independent current Lease terms correction | `go test -overlay "$E/discovery-review-overlay-578915.json" -count=1 -race ./internal/authorization -run '^Test(Discovery\|IndependentDiscoveryCurrentLeaseRent)' -v -timeout=15m` | Exit 0: **11 tests**, 0 fail/skip, 22.299s. Unchanged independent reproduction now finds effective revised rent and excludes obsolete original rent. `$E/discovery-independent-correction-20261127T032927Z-590145.log`. |
| **R1** independent collection/integration review | Exact commands and hashes in [collection security review](issue-106-preflight/collection-security-review.md) and [integration SPEC review](issue-106-preflight/integration-spec-review.md) | Collection final review: **6 roots + 14 subtests**, 0 fail/skip. Final unchanged zero/all-list overlays, ordinary Chromium browser pair and forced-conflict Chromium run: exit 0, no skips. Reviewed findings resolved in their scopes; final renewed whole-ticket review remains separate. |

D1/D2/D3 complete command/log/count/exit/source records use their labels under `$E/{commands,logs,results}`. Expanded Chromium artifacts include `$E/browser-artifacts/acceptance-discovery-1795753880197719523/`; earlier traces, including failures, remain retained. Full acceptance detail and historical counts are in [acceptance execution](issue-106-preflight/acceptance-execution.md).

## Independent findings, failures and corrections

Historical red runs remain red; a later passing run is separately identified.

| Finding/failure | Correction and retained evidence |
|---|---|
| Effective Lease Billing revision absent from search | Domain current-allocation replay added inside the candidate statement; original independent overlay unchanged and passing (**E1**). Review documents the active-allocation versus Invoice-entitlement distinction. |
| Read → revoke → preference write TOCTOU | Real HTTP race initially observed 204 with `revoke,state`. Domain query validation and CAS now share one fresh-principal transaction; **D1/S2** pass. Original `acceptance-persistence-authority-race` failure retained. |
| Five lists materialized full collections before Go slicing | Specialized bounded selectors and main-handler wiring; actual production SQL LIMIT/EXPLAIN assertions, authority/projection tests and **A1/R1** pass. Unpaginated report/export selectors retained. |
| Explicit Lease `facet=base` silently fell back to Billing | Shared read/commit validator requires base View; restoration/POST/atomic validator deny after revocation, omitted facet remains valid. **B1** passed. |
| 1,000-byte document limit rejected valid Unicode; initial fix narrowed legacy exports | Separate collection 500-rune and export 1,000-byte normalizers carried through domain authorization/snapshot calls. **B1** verifies 334/499/500 multibyte collection input and export 501/1,000 ASCII acceptance; oversized/invalid UTF-8 rejects. |
| Contact/Location browser Next links failed with empty HTMX target | Native-link fallback for no-fragment pagination; unchanged reviewer browser repro plus actual Contact/Location Next/Previous/Back identities pass (**D1/R1**). |
| Filtered/out-of-range lists showed first-record wording; literal `"0"` treated as absent | Shared no-match/empty/out-of-range recovery using authorized totals; zero-as-absent restricted to numeric filter sentinels. Independent overlays, actual browser Clear filters and hidden-record noninfluence pass (**D2/R1**). |
| Interaction Retry navigated before preference recovery; genuine CAS conflict exposed hidden rows | Synchronization/recovery assertions now wait for committed preference and visible rows, with an explicit competing POST/409/Retry variant. Existing Back/scroll/navigation assertions retained. **D2/D3/R1** pass; earlier traces preserved. |
| Sensitive Billing race fixtures failed before race execution | Fixture now finalizes the Lease via production commands and supplies expected details revision. Earlier FK/revision failures are not product passes; all six Billing/archive/relation order cases subsequently passed in D1. |
| Concurrent helper edits caused duplicate/missing parser compilation failures | One frozen `parseDocumentCollectionFilter` definition in `collection_list_specs.go`; failed builds retained, combined **B1** subsequently passed. |
| Legacy navigation test falsely matched `page=2` within `per_page=25` | Pagination assertion corrected rather than weakening authorization checks; actual navigation pre-pagination regression passes in **A1**. |
| Interim full-suite deploy failure: “symlink target modified” | Driver inherited `umask 077`; fixture target was already 0600 before installer execution. Installer rejected symlink and preserved mode/content. Approved base and current each failed twice at 077, passed twice at 022; deploy/module diff empty. Child test/build mask corrected to 022, private evidence parent remains 077. No deploy production/test change. `$E/results/list-state-deploy-diagnosis.md`, `qualification-umask-correction.md`; failed/interrupted `final-quality-Lr2LRqaB` retained. |

Earlier readiness/fixture attempts, intentionally excluded browser runs, source-drift runs and two skipped embedded browser regressions are recorded in the linked execution/review reports and raw logs. They are not counted as passes for omitted coverage.

### Repairs after the failed full-suite run

`final-quality-dnQIMR3T` remains **failed**, with **7,022 named passes, 24 named failures, nine named skips** (roots and subtests combined); package events were 19 pass, one fail, 50 no-test-file skips. Its complete `race-suite.log`, `race-suite.counts.json`, `failure-summary.md` and extracted logs remain intact. These totals are not combined with the final run.

| Repair | Verified behavior and evidence |
|---|---|
| Rejected post-save flash URLs / lost notification | Parser validates only recognized singleton UTF-8 flash, at most 500 characters and no controls, then removes it before Explicit/canonical/persistence. Flash-only restores saved filters; explicit empty search overrides; aliases/conflict rejection remain. Adapter preserves validated escaped display separately and suppresses rejected transport in context/recovery inputs. `TestFlashTransportDoesNotOverrideOrPersistQuery` and ten-collection `TestCollectionFlashTransportRestoresSavedStateHTTP` independently passed, 2 roots + 10 subtests; `$E/flash-final-independent-BdCsQ4dD/`. Earlier missing-display failures remain in `list-state-flash-integration-1`. |
| Native read failure/retry assertions | `TestSaveSafetyNativeInitialReadFailureHTTP` uses each route's valid sort, retains real table faults and 503/no-SQL/no-false-empty assertions, parses effective Retry form/link values and actually retries after restoration. Typed-field post-save redirects pass. `$E/logs/list-state-full-regressions-2.log`: 22 roots + 74 subtests pass, zero failures/skips; focused vet exit 0. |
| Populated Lease upgrade fixture replayed 126 over installed table | Fixture asserts `list_states` empty before dropping only that table, without CASCADE, matching its removal of migration markers 113 onward. Production migration 126 remains strict. Original domain/history/grants/sequences checks and repeated migration remain; both native Lease upgrade and #106 populated migration preservation pass. |
| Legacy bare-URL browser expectations | Item/navigation/detail scripts require exact canonical query entries, origin/path and no fragment/extra parameters; filtered Back and actual revoked 403 are retained. `$E/logs/browser-canonical-owned-after-2.log`: 3 roots + 10 subtests pass. |
| Lease remembered authority, Reset, archived Item and recovery | Repeated current-authority denial precedes explicit CSRF-protected Reset POST; GET review is nonmutating, tombstone/CAS assertions remain. Archived Item checks respect remembered state and actual form submission. Dashboard wording updated without weakening unavailable/recovery checks; DomainRepair script unchanged. Reset-review target gains existing navigation-link sizing, retaining 24×24 fine/44×44 coarse assertions. `$E/logs/ui-full-failures-final-04.log`: 7 roots + 19 subtests pass; independent SPEC subset: 4 roots + 9 subtests pass. |
| Firefox automation environment | Final runtime supplies `MOZ_REMOTE_ALLOW_SYSTEM_ACCESS=1`; final accessibility test executes Chromium and Firefox successfully. Earlier Firefox failure remains in the failed suite. |

Final independent [security review](issue-106-preflight/collection-security-review.md) and [SPEC review](issue-106-preflight/integration-spec-review.md) both approve their final scopes with **no remaining actionable findings**. Security manifests match at `3752ae998b68e7eff92e0c8385246dc3d2ab73fe4ebe49da016c5c0f466ad4fb`; SPEC verified `results/ui-full-failures-freeze.sha256` before/after its independent execution. Their historical pending/failing sections are superseded by their final appended dispositions, not erased. Whole-suite approval is supplied by the separate final runtime evidence below.

## Authoritative final qualification

Source: `$E/results/final-quality-sYZ5x4mb/qualification-summary.md`, `skip-ledger.md`, complete command/log/exit/count records and per-command manifests. Build: `$E/results/final-build-q6qWMpDe/`.

| Gate | Actual result |
|---|---|
| `make build` | Exit 0; generation unchanged; identical before/after manifests. |
| `make lint`, `templ generate -check`, gofmt empty-list check, `git diff --check` | Each exit 0. |
| Real-PG race `make test` | Exit 0; **1,899 root passes, 5,159 subtest passes = 7,058 named passes; zero failures; nine root skips, zero subtest skips**. |
| Packages | **20 pass, zero fail, 50 with no test files** (reported as package skips). |
| Full-size nonrace nginx scale | **1 root pass, zero fail/skip, 1 package pass**, same production/test source, 1 GiB. |
| PG15 unsupported-server negative | **1 root pass, zero fail/skip**, `$E/results/pg15-negative-nXGaM7Ac/`; relevant source hashes unchanged at final build. |

### Exact commands and opt-ins

The following transcribes actual command records with private database values replaced by environment placeholders. `$R` is the final quality directory. Browser/tool paths and private connection values come from `$E/env.sh`; the separate PG15 command uses `$PG15_DATABASE_URL` and `$PG15_ADMIN_URL` as redacted placeholders for its recorded connections, not PG18. Evidence parent umask is 077; qualification command children use 022.

```sh
source "$E/env.sh"
R="$E/results/final-quality-sYZ5x4mb"
export BROWSER_TESTS=1 ACCESSIBILITY_TESTS=1 FREEFSM_OFFLINE_KEY_TESTS=1
export FREEFSM_BACKUP_BROWSER_SCALE=1 FREEFSM_BACKUP_TEST_GB=1
export CGO_ENABLED=1 GOMAXPROCS=2 MOZ_REMOTE_ALLOW_SYSTEM_ACCESS=1
export ACCESSIBILITY_OUTPUT="$R/browser/accessibility"
export FREEFSM_BACKUP_BROWSER_REPORT="$R/browser/backup-report.json"
unset FREEFSM_BACKUP_BROWSER_BYTES FREEFSM_INSTALLATION_BINARY

(umask 022; GOFLAGS=-p=2 make BUILD_DIR="$E/build" build)
(umask 022; env GOFLAGS=-p=2 make lint)
(umask 022; templ generate -check)
(umask 022; bash -c 'git ls-files -z --cached --others --exclude-standard -- "*.go" | sort -zu | xargs -0 -r gofmt -l > "$1"; cat "$1"; test ! -s "$1"' _ "$R/gofmt-files.txt")
(umask 022; git diff --check)
(umask 022; env \
  FREEFSM_TEST_DATABASE_URL="$ISSUE106_DATABASE_URL" \
  FREEFSM_INSTALLATION_TEST_ADMIN_URL="$ISSUE106_ADMIN_URL" \
  FREEFSM_BACKUP_TEST_ADMIN_URL="$ISSUE106_ADMIN_URL" \
  GOFLAGS='-p=1 -count=1 -timeout=180m -skip=^TestBackupBrowserNginxScale$ -json' \
  make test)
(umask 022; env \
  FREEFSM_TEST_DATABASE_URL="$ISSUE106_DATABASE_URL" \
  FREEFSM_BACKUP_TEST_ADMIN_URL="$ISSUE106_ADMIN_URL" GOFLAGS=-p=1 \
  go test -json ./internal/handlers -run '^TestBackupBrowserNginxScale$' \
  -count=1 -v -timeout=30m)

# Separately retained PG15 companion (test.command):
FREEFSM_TEST_DATABASE_URL="$PG15_DATABASE_URL" \
FREEFSM_INSTALLATION_TEST_ADMIN_URL="$PG15_ADMIN_URL" \
FREEFSM_BACKUP_TEST_ADMIN_URL='' GOMAXPROCS=2 GOFLAGS=-p=1 \
go test -race ./cmd/freefsm \
  -run '^TestStartupRejectsUnsupportedServerBeforeBootstrap$' \
  -count=1 -timeout=15m -v
```

`make test` supplies race instrumentation. **`TestBackupBrowserNginxScale` was explicitly excluded from race**, then passed nonrace at the full 1 GiB size. This is not “every test passed under race.”

### Nine actual root skips

| Skipped direct entry | Separate coverage |
|---|---|
| `TestStartupRejectsUnsupportedServerBeforeBootstrap` | PG18 cannot exercise the unsupported-major negative; PG15 companion passed with unchanged relevant hashes. |
| `TestAuthorizationRestartProbe` | Subprocess helper; parent `TestReviewedUpgradeBlocksUnresolvedMappingsAndPreservesFacts` passed. |
| `TestBackupCrashChild` | Helper; parents `TestProcessCrashAndRepeatedInterruptedRollback` and `TestProcessCrashAfterCommitAndDuringCleanup` passed. |
| `TestExactSourceHistoryInitializer` | Helper; parent `TestExactSourceInstalledHistories` passed. |
| `TestAssetIdentityRestartProbe` | Helper; parent `TestAssetIdentityRestartHTTP` passed. |
| `TestProtectedAuditWorkerProcess` | Helper; parent `TestProtectedAuditGenerationDownloadFailuresAndRestartHTTP` passed. |
| `TestCustomerExportWorkerProcess` | Helper; parent `TestCustomerExportProcessCrashRecoveryHTTP` passed. |
| `TestTransferRestartProbe` | Helper; parent `TestTransferVerifiedWithoutPublicationHTTP` passed. |
| `TestNotesReceiptRestartProbe` | Helper; parent `TestNotesReceiptRetentionAndRestartHTTP` passed. |

The eight helper direct entries remain **skipped**, not relabeled as passes. There were no named opt-in skips. The scale exclusion is distinct from these nine observed skips. The 50 no-test-file packages are not named test skips.

## Final handoff

- Qualification and both independent final reviews: **passed**, with the explicit limits/skips above.
- Staging: **completed by coordinator** for `cmd`, `internal`, `docs/issue-106-preflight` and this evidence document. Coordinator reported `git diff --cached --check` and `git diff --exit-code` passed, with all changes staged and no untracked files at that point. HEAD remains the approved base. Staged changes exist; this is not a clean-repository claim. The coordinator will re-stage this subsequent documentation-only status update.
- Runtime shutdown: **verified complete**, 2026-11-27 10:24:54–10:24:55 UTC. Authoritative evidence: `$E/results/shutdown-hHD2n6bq/shutdown-summary.md`, command exits and before/after inventories. PG18 stop exited 0; subsequent `pg_ctl status` exited 3 (no server running), and `postmaster.pid` was absent. PG15 stop exited 0; container state is exited, Running=false, PID 0, ExitCode 0. Ports **45883 and 33945 are closed**. No owned descendants, browser/driver/app/test processes or live containers remain. Database data, logs, artifacts and the stopped PG15 container are retained; unrelated resources were untouched.
- This evidence update is authorized **docs-only manifest drift** after qualification. Production/tests and raw evidence remain unchanged by this task.
- No commit, push or issue closure performed.

Remaining evidence limits: discovery-specific journeys are Chromium-based, while the shared accessibility matrix also ran Firefox; no blanket native FreeBSD/reboot/release certification; no exhaustive permutation of every scalar field across every entity; no general latency guarantee from bounded row-materialization tests. This handoff is only for #106; #105 remains blocked by #24.
