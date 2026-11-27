# #106 direct acceptance execution

## Freeze revoked — latest result is interim evidence

After `acceptance-final-01`, independent review found literal `q=0`/`search=0` incorrectly classified as an absent filter, plus a Retry-at-100 interaction accessibility failure. The coordinator revoked freeze; C owns both production corrections and the existing interaction test. The passing run below remains valid for its recorded source snapshot, **not final closure**.

Added `TestDiscoveryLiteralZeroItemNoMatchHTTP` and actual browser checks for both literal-zero aliases, no-match wording and Clear filters recovery. These now pass in `acceptance-correction-01` after C's correction. Prior passes did not cover them; no full focused-suite restart was needed.

### Corrected affected scope — acceptance-correction-01

```sh
umask 022
bash "$E/commands/acceptance-focused.sh" acceptance-correction-01 \
  '^(TestDiscoveryLiteralZeroItemNoMatchHTTP|TestDiscoveryPersistedBrowser|TestAccessibleInteractionPersistedBrowser)$'
```

Exit 0: **3 top-level PASS, 5 including subtests, 0 FAIL / 0 SKIP**, 23.671s. Before/after source manifests compare identical (`cmp` exit 0). The expanded Chromium journey, both literal `q=0`/`search=0` HTTP subtests, and ordinary interaction Retry regression all executed successfully.

The zero assertions require the semantic `data-list-empty="no-matches"` state and accept the valid “No matches for these filters” wording. They retain no-first-record, actual Clear filters recovery and archived-record noninfluence checks. Browser artifacts: `$E/browser-artifacts/acceptance-discovery-1795753880197719523`. Matching exact command/log/counts/exit/manifests use prefix `acceptance-correction-01`.

The coordinator owns renewed freeze and full-repository qualification.

### Forced CAS-conflict Retry — acceptance-correction-forced-01

```sh
umask 022
FREEFSM_INTERACTION_STALE_GET=1 bash "$E/commands/acceptance-focused.sh" \
  acceptance-correction-forced-01 \
  '^(TestDiscoveryLiteralZeroItemNoMatchHTTP|TestAccessibleInteractionPersistedBrowser)$'
```

Exit 0: **2 top-level PASS, 4 including subtests, 0 FAIL / 0 SKIP**, 10.703s. Before/after manifests compare identical (`cmp` exit 0). This exercises the real stale-GET/CAS-conflict interleaving, explicit Retry filters recovery, remembered preference state, visible rows and subsequent navigation assertions. The two literal-zero HTTP subtests also passed. Matching artifacts use prefix `acceptance-correction-forced-01`; the environment flag and child umask above supplement the expanded Go command saved by the runner.

All D additions and requested affected regressions have now executed successfully. The original complete 24-test run plus these stable affected-scope results provide the issue-specific handoff; no full-repository qualification is inferred.

## Previously frozen result — acceptance-final-01

**PASS: 24 top-level tests, 63 including subtests; 0 failures, 0 skips.** All tests in the numbered AC matrix below passed in `acceptance-final-01`, with race detection and real PostgreSQL/Chromium enabled. Exit 0; Go package duration 168.471s. No discovery tests were excluded.

```sh
source /home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/env.sh
bash "$E/commands/acceptance-focused.sh" acceptance-final-01
# Expanded:
BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' \
  go test -race ./internal/handlers -run '^TestDiscovery' -count=1 -v -timeout=15m
```

Before/after source manifests compare byte-identical (`cmp` exit 0). Both have SHA-256 `595bf16f3b043d200edaa2e46a2a9c33b29dcb1a2a3eafe418a4d2da0e07cc66`. Manifest scope is Go, templ, SQL, JS and CJS files under `internal` and `cmd`; evidence documentation edits are outside that source snapshot.

Evidence handoff (all paths relative to `$E`):

- `logs/acceptance-final-01.log`: complete named test/subtest outcomes.
- `commands/acceptance-final-01.command`: exact expanded invocation.
- `results/acceptance-final-01.{exit,counts,started,finished,source-before,source-after}`.
- `browser-artifacts/acceptance-discovery-1795752791138645083/`: Chromium **151.0.7922.34**, browser log, trace ZIP and screenshots.

Explicit final successful sensitive cases under `TestDiscoverySensitivePreferenceCommitOrdersHTTP`: `billing/revoke_first=false`, `billing/revoke_first=true`, `archived/revoke_first=false`, `archived/revoke_first=true`, `relation/revoke_first=false`, `relation/revoke_first=true`. All executed after fixture repair, including write-order, unchanged state/revision on denial, restored/shared-query authorization and intrinsic Reset assertions.

The final browser log confirms actual Contact/Location Next–Previous–Back identities, Item no-match/Clear filters versus empty authorized scope, hidden-record noninfluence, Customer/Item/Lease persisted state, combined Back/scroll/fresh data, production re-login, CSRF denial, delayed-response ordering, mobile/keyboard, network loss, revoked authority and expired session. All ten dedicated-list HTTP subtests also passed.

At the time of this run, all D additions executed successfully. Subsequent review findings and new assertions are listed above. Earlier failed attempts and source drift are retained below as history. Full-repository build/test/lint qualification belongs to the runtime owner and is not claimed by this targeted result. Its `git diff --check`, JavaScript syntax and changed-Go formatting checks passed.

## Ownership and environment

Acceptance-only implementation: `internal/handlers/discovery_*_test.go` and `internal/handlers/testdata/browser/discovery-persisted.cjs`. No production source, existing tests, Git/GitHub state or other worktree changed by this agent.

Environment explicitly sourced for every executed database/browser command:

```sh
source /home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/env.sh
```

This supplies disposable PostgreSQL app/admin URLs, browser executables, Node module resolution, durable `E` and disk-backed short `TMPDIR`. Browser tests additionally require `BROWSER_TESTS=1`. The real `permissionHTTPFixture` migrates unique PostgreSQL schemas and uses persisted sessions and production routing. Browser tests wrap that handler in production BrowserCSRF and serve actual static assets. API tests use real persisted mobile bearer sessions and `apiv1.NewRouter`.

## Executable inventory

All names below are present in source (status is reported separately):

| Requirement | Direct tests |
|---|---|
| Ten identities, independent facets, authority before rank/count/empty wording | `TestDiscoveryTenTypesProductionHTTP`, `TestDiscoveryFacetAndTenantNoninterferenceHTTP`, `TestDiscoveryAssignedScopeBeforeRankingHTTP`, `TestDiscoveryBuiltInScalarFacetAuthorityHTTP` |
| Active scalar values, literal matching, retired exclusions, no Comment/File bytes | `TestDiscoveryLiteralAndRetiredScalarHTTP`, `TestDiscoveryRetiredOptionOnActiveDefinitionHTTP`, `TestDiscoveryCommentsAndFileBytesExcludedHTTP` |
| Global rank, exact/prefix/elsewhere, stable ties/pages, no per-type limit | `TestDiscoveryGlobalRankAndPaginationHTTP` (138 mixed Customer/Item rows, 25/50/100, full expected ordered identity sequence) |
| Customer authorized relationship labels | `TestDiscoveryCustomerRelationshipAuthorityHTTP` |
| Lease base/Billing-only isolation and lifecycle/queue filters | `TestDiscoveryLeaseProjectionAndFilterIsolationHTTP` |
| Archive opt-in/grants/badges and hidden conversion | `TestDiscoveryMalformedAndRestoredAuthorityHTTP`, `TestDiscoveryConversionHiddenNeverOrdinaryHTTP` |
| All ten main lists: 101 identities each, 25/50/100, repeated deterministic paging, POST state and fresh-session restore | `TestDiscoveryTenCollectionsPageSizesHTTP` |
| Server state, URL whole override, explicit empty, Reset, own-user isolation, no GET writes/audit/domain effects | `TestDiscoveryServerStateURLResetHTTP` |
| Malformed filters, current authority, required candidate/custom-field/state dependency failure, failed preference write | `TestDiscoveryMalformedAndRestoredAuthorityHTTP`, `TestDiscoveryWholeSearchFailureHTTP`, `TestDiscoveryPreferenceWriteFailureIsWholeFailureHTTP` |
| Actual PostgreSQL lock-observed concurrency | `TestDiscoveryQueuedGrantRevocationHTTP`, `TestDiscoveryAssignmentSnapshotCountAndRowsHTTP`, `TestDiscoveryQueuedSaveCannotResurrectResetHTTP`, `TestDiscoveryGrantLossBetweenReadAndPreferenceCommitHTTP`, `TestDiscoverySensitivePreferenceCommitOrdersHTTP` |
| Field API: Item identity/Cost isolation/SKU denial/current revocation; no general search or Lease surface; unknown filter rejection | `TestDiscoveryFieldAPIFacetAndOfficeBoundary` |
| Populated upgrade preserves complete rows/sequences, actual conversion/reversion, archived source, retired value, Comment/File, Lease and custom Role | `TestDiscoveryPopulatedUpgradePreservesHTTPHistory` |
| Actual browser Back/page/scroll/fresh DB, production login/new context, URL/Reset/isolation, Customer/Item/Lease dedicated persistence, CSRF denials, 320px/coarse-pointer/keyboard, delayed genuine response, network loss/revocation/session expiry | `TestDiscoveryPersistedBrowser` |

The browser script logs a PASS checkpoint only after its assertions complete; overall success requires the Go test to exit successfully and its final persisted-state assertion to pass. Trace ZIP, screenshots and browser log are retained in unique `$E/browser-artifacts/acceptance-discovery-*` directories. This is Chromium-specific ticket evidence, not a release browser-matrix claim.

### Numbered AC-to-test matrix

| #106 AC | Executed test names (all prefixed `TestDiscovery`) |
|---|---|
| 1: authorized types/facets and entry points | `TenTypesProductionHTTP`, `FacetAndTenantNoninterferenceHTTP`, `BuiltInScalarFacetAuthorityHTTP`, `AssignedScopeBeforeRankingHTTP`, `FieldAPIFacetAndOfficeBoundary` |
| 2: active scalar search and excluded content | `LiteralAndRetiredScalarHTTP`, `RetiredOptionOnActiveDefinitionHTTP`, `BuiltInScalarFacetAuthorityHTTP`, `CommentsAndFileBytesExcludedHTTP` |
| 3: global ranking, deduplication and pagination | `GlobalRankAndPaginationHTTP`, `TenTypesProductionHTTP`, `FacetAndTenantNoninterferenceHTTP`, `TenCollectionsPageSizesHTTP` |
| 4: Customer relationship search | `CustomerRelationshipAuthorityHTTP` |
| 5: Lease projection and domain filters | `LeaseProjectionAndFilterIsolationHTTP`, `SensitivePreferenceCommitOrdersHTTP`, `PersistedBrowser` |
| 6: archive/conversion exclusions and list controls | `ConversionHiddenNeverOrdinaryHTTP`, `MalformedAndRestoredAuthorityHTTP`, `TenCollectionsPageSizesHTTP`, `LiteralZeroItemNoMatchHTTP`, `PersistedBrowser` |
| 7: own-user durable state, whole URL override, Reset | `ServerStateURLResetHTTP`, `TenCollectionsPageSizesHTTP`, `QueuedSaveCannotResurrectResetHTTP`, `PersistedBrowser` |
| 8: fresh authorization and bounded failures | `MalformedAndRestoredAuthorityHTTP`, `WholeSearchFailureHTTP`, `PreferenceWriteFailureIsWholeFailureHTTP`, `GrantLossBetweenReadAndPreferenceCommitHTTP`, `SensitivePreferenceCommitOrdersHTTP`, `PersistedBrowser` |
| 9: noninterference and preserved domain/audit state | `FacetAndTenantNoninterferenceHTTP`, `BuiltInScalarFacetAuthorityHTTP`, `AssignedScopeBeforeRankingHTTP`, `ServerStateURLResetHTTP`, `FieldAPIFacetAndOfficeBoundary` |
| 10: additive populated upgrade/history preservation | `PopulatedUpgradePreservesHTTPHistory`, `ConversionHiddenNeverOrdinaryHTTP`, `LiteralAndRetiredScalarHTTP` |
| 11: direct persisted API/browser/concurrency evidence | `QueuedGrantRevocationHTTP`, `AssignmentSnapshotCountAndRowsHTTP`, `QueuedSaveCannotResurrectResetHTTP`, `GrantLossBetweenReadAndPreferenceCommitHTTP`, `SensitivePreferenceCommitOrdersHTTP`, `FieldAPIFacetAndOfficeBoundary`, `PersistedBrowser` |

This maps acceptance evidence, not exhaustive permutations of every scalar field on every entity or a full release browser matrix. Actual outcomes and source stability are recorded below; the initial fixture-design document contains proposed scenarios beyond the executed inventory.

## Executed attempts

These are implementation iterations on changing peer source, **not frozen-source final qualification**.

| Attempt / exact command after sourcing environment | Actual result / durable logs |
|---|---|
| `CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers -run '^TestDiscoveryFieldAPIFacetAndOfficeBoundary$' -count=1 -v -timeout=5m` | Exit 1, compile failure while peer templates were incomplete; also exposed fixture's nonexistent Contact ent archive setter. No tests passed. `logs/acceptance-api-initial.log`, `results/acceptance-api-initial.exit`. |
| `CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers -run '^TestDiscovery' -skip '^TestDiscoveryPersistedBrowser$' -count=1 -v -timeout=10m` (initial) | Exit 1, compile failure: Item TaxRate builder requires string. No tests passed. `logs/acceptance-http-initial.log`. |
| Same command (round 2) | Exit 1. Several real tests passed, but fixture assumptions failed: creation redirects to list; relationship tie chooses alphabetic label; dates display localized; multiselect uses `[]` form name. Incorrect Reset barrier deadlocked because WithinActor uses an **exclusive** 190019 fence. Stopped only owned test PID 575858 with SIGQUIT; stack trace and failed results retained in `logs/acceptance-http-round2.log`. Not accepted. |
| Same command (round 3) | Exit 1; **18 top-level PASS, 1 top-level FAIL**, including subtests 41 PASS / 11 FAIL / 0 SKIP. All combined/API/state/fault/race tests in that compilation passed. Ten-collection integration failed against still-in-progress list wiring: legacy 25-row caps/invalid-size fallback and missing expected Contact/Location destinations. `logs/acceptance-http-round3.log`, `results/acceptance-http-round3.exit`. Browser intentionally excluded by `-skip`, not counted as passed. Later migration test and additional assertions were not yet in this compilation. |
| `BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers -run '^TestDiscoveryPersistedBrowser$' -count=1 -v -timeout=6m` (initial) | Exit 1: fixture expected CSRF 403, production nosurf returns 400. Actual browser and automatic state POST ran. Failure trace retained. `logs/acceptance-browser-initial.log`. |
| Same browser command (round 2) | Exit 1: Back/scroll/refreshed DB and CSRF checkpoint passed; exact-label locator for implicit Sort label failed. Corrected to semantic prefix label match. `logs/acceptance-browser-round2.log`. |
| Same browser command (round 3) | **Exit 0, 1 PASS / 0 FAIL / 0 SKIP**, real Chromium journey: Back/page/sort/scroll/current data, CSRF denial, real login in new context, URL whole override, Reset, user isolation, delayed genuine response, 320px/keyboard, network failure, authority loss without transient row reinsertion, session revocation. `logs/acceptance-browser-round3.log`; artifacts `browser-artifacts/acceptance-discovery-1795750037461751701`. This predates adding representative dedicated Customer/Item/Lease browser journeys. |
| `CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers -run '^TestDiscoveryPopulatedUpgradePreservesHTTPHistory$' -count=1 -v -timeout=5m` | **Exit 0, 1 PASS / 0 FAIL / 0 SKIP**. `logs/acceptance-upgrade-initial.log`, `results/acceptance-upgrade-initial.exit`. |
| `CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers -run '^TestDiscovery' -skip '^TestDiscovery(PersistedBrowser\|TenCollectionsPageSizesHTTP)$' -count=1 -v -timeout=10m` | **Exit 0, 19 top-level PASS / 0 FAIL / 0 SKIP**, including subtests 42 PASS. `logs/acceptance-combined-round4.log`, `results/acceptance-combined-round4.exit`. Includes upgraded real history and strengthened no-audit, numeric/date/facet, exact tombstone identifier and empty-wording assertions. The two explicitly excluded tests are not claimed passed by this run; the later commit-boundary race was not yet compiled into this run. |
| `bash "$E/commands/acceptance-focused.sh" acceptance-persistence-authority-race '^TestDiscoveryGrantLossBetweenReadAndPreferenceCommitHTTP$'` | **Exit 1, 0 PASS / 1 FAIL / 0 SKIP: confirmed production commit-time gap.** Actual POST 204 with durable fixture event order `revoke,state`. `logs/acceptance-persistence-authority-race.log`, exact command/counts/source manifests under matching `commands/` and `results/` names. |
| `bash "$E/commands/acceptance-focused.sh" acceptance-retired-option '^TestDiscoveryRetiredOptionOnActiveDefinitionHTTP$'` | **Exit 0, 1 PASS / 0 FAIL / 0 SKIP**. Retired recorded option remains searchable on active definition, unselected option does not match, retired definition removes hit. `logs/acceptance-retired-option.log`, matching command/count/source manifests retained. |

Further executions append to this record after completion. No full repository `make test`, final `make lint`, native/release matrix, or runtime shutdown is claimed here. Coordinator owns final checks and shutdown.

Ten-collection tests additionally check explicit URL read does not mutate saved filters, malformed duplicate/page/sort/unknown parameters, Reset tombstone/default revisit, and independent collection grant loss before both GET and preference POST. The Contact/Location identity matcher was corrected to their existing top-level detail destinations. Dedicated browser Customer/Item/Lease state/Reset coverage passed in both complete implementation runs.

For future focused runs, `$E/commands/acceptance-focused.sh <unique-label> [test-regex]` records exact command, timestamps, numerical exit, test counts, full log and before/after source hashes; it rejects an existing evidence label and detects zero matched discovery tests. Default regex is `^TestDiscovery`, with browser opt-in enabled. This is a narrow direct-test invocation script, not a generalized fixture harness.

## Barrier correction

Production `WithinActor` serializes on exclusive transaction advisory lock 190019, including list-state methods and discovery. Correct concurrency tests observe actual database waiters with the fixture `application_name`:

- Revocation queued before authenticated search → search denies under current grants.
- Search paused on `custom_field_definitions` while holding authority fence; unassignment queues behind that reader → response count/page is exactly the old coherent snapshot; subsequent request is empty.
- Reset queued before a stale original-revision POST save → Reset commits; save reauthorizes but CAS conflicts; empty tombstone/revision persists.
- **Historical blocker, base-View regression now passing:** POST discovery reauthorization and `SaveListState` originally used separate authority transactions. The initial test observed unauthorized `revoke,state` success. C/B replaced this with atomic authorization/CAS; `TestDiscoveryGrantLossBetweenReadAndPreferenceCommitHTTP` passed in both complete runs. It captures actual preference/audit write order using schema-local triggers and accepts only authorized `state,revoke` success or `revoke`-only denial. Sensitive-case outcomes are recorded separately below.

The failed prototype attempted Reset while a blocked read already held the fence. It is retained as a failed fixture attempt, not product concurrency evidence. Current POST helper requests carry bounded contexts to avoid an unbounded fixture wait.

### Sensitive commit-boundary extension

`TestDiscoverySensitivePreferenceCommitOrdersHTTP` now covers six actual HTTP/DB cases: Billing queue, archived Lease, and explicit related-Customer permission loss, each with save-first and revoke-first ordering. It establishes a persisted original preference and revision before racing, captures actual preference/audit write order, accepts only authorized `state,revoke` success or `revoke`-only denial, verifies unchanged full state/revision on denial, checks rejected shared/restored filters after scope loss, then verifies intrinsic Reset still succeeds.

The original base-View test now uses Lease base View with an explicit Customer relation. Both save-first tests block the real same-company Customer target validation on the `customers` table, rather than requiring a full Item candidate query. This exercises C/B's implemented single-`WithinActor` authorization+CAS implementation without a nested `Discover` call. Explicit relation target authorization/existence executes in that same transaction.

The original failing Item-based commit-gap evidence and source manifests remain retained. Complete run 01 executed all six cases: archive and relation cases passed; both Billing cases failed during fixture setup because `lease_billing_period_failures` references a finalized Lease. The fixture now creates premises, company-owned equipment and billing terms, then uses production `SaveDraftLeaseDetails`, `SaveDraftLeaseBilling` and `FinalizeLease` before inserting the failure. No authorization assertions were weakened.

## Complete targeted runs

Command: `bash "$E/commands/acceptance-focused.sh" acceptance-complete-01`

Expanded command: `BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers -run '^TestDiscovery' -count=1 -v -timeout=15m`.

- Exit 1: 23 top-level PASS, 1 FAIL, 0 SKIP; including subtests 60 PASS, 3 FAIL, 0 SKIP.
- The only failed top-level test was `TestDiscoverySensitivePreferenceCommitOrdersHTTP`, with fixture-setup FK errors in `billing/revoke_first=false` and `billing/revoke_first=true`.
- All ten dedicated collection cases, complete Chromium journey (including Customer/Item/Lease), upgrade/API/failure tests and base-View commit race passed.
- Browser artifacts: `$E/browser-artifacts/acceptance-discovery-1795751229440602348`.
- `$E/results/acceptance-complete-01.source-before` and `.source-after` differ: C updated `internal/handlers/list_state.go` and `internal/handlers/list_state_collections_test.go`. Therefore this is not stable-source qualification.
- Full output, exact expanded command, timestamps, counts and numerical exit are retained under the matching `acceptance-complete-01` prefix in `$E/logs`, `$E/commands` and `$E/results`.

Command: `bash "$E/commands/acceptance-focused.sh" acceptance-complete-02` (same expanded command).

- Exit 1: 23 top-level PASS, 1 FAIL, 0 SKIP; including subtests 60 PASS, 3 FAIL, 0 SKIP.
- Only `TestDiscoverySensitivePreferenceCommitOrdersHTTP/billing/revoke_first=false` and `/billing/revoke_first=true` failed, before racing. The second production draft-details save omitted expected revision 1 and correctly received a conflict. Corrected the fixture to supply `Revision: 1`; finalization still requires DetailsRevision 2.
- Browser artifacts: `$E/browser-artifacts/acceptance-discovery-1795751494084427847`.
- Source manifests drifted: new `collection_pages*.go` selectors, changed `document_list_exports.go`, `customer_children.go`, `jobs.go`, `estimates.go`, `invoices.go`, and peer authority/spec tests. No frozen-source claim.
- Coverage gaps remaining at this checkpoint: Billing commit-race assertions did not execute; dedicated-list/browser scope needs rerun after A/B completion; B's Unicode boundary and explicit Lease base-facet corrections require their regression evidence. Ten-list paging success does not itself prove bounded SQL materialization.
- All logs/counts/exits/manifests remain retained under `acceptance-complete-02`. The corrected sensitive test was then qualified separately with label `acceptance-sensitive-03`.

Command: `bash "$E/commands/acceptance-focused.sh" acceptance-sensitive-03 '^TestDiscoverySensitivePreferenceCommitOrdersHTTP$'`.

- Exit 0: 1 top-level PASS; all six subtests PASS; 0 FAIL / 0 SKIP (7 PASS including the parent).
- Exact cases: `billing/revoke_first=false`, `billing/revoke_first=true`, `archived/revoke_first=false`, `archived/revoke_first=true`, `relation/revoke_first=false`, `relation/revoke_first=true`.
- Billing fixture correction is verified. All six cases executed write-order, state/revision, current-authority rejection and intrinsic Reset assertions.
- Before/after source manifests differ during concurrent peer implementation; this is focused implementation evidence, not frozen-source qualification. Matching log, command, counts, exit and manifests are retained under `acceptance-sensitive-03`.
- At this historical checkpoint, affected dedicated-list/browser scope still needed qualification after A/B completion. Both earlier Billing setup failures remain retained and explained. The independent reviewer subsequently recorded successful Unicode/base-facet HTTP regressions in `integration-spec-review.md`; those peer tests are not counted in D's discovery totals.

### Browser regressions added after independent spec review

`TestDiscoveryPersistedBrowser` now seeds 31 Contacts and 31 Locations, supplies their exact persisted destination identities to Chromium, clicks actual Next and Previous controls, and uses browser Back. Every page must contain exactly the independently seeded ordered identities (25 then 6), and each collection must have persisted state revisions afterward. This catches the missing HTMX pagination target that direct manually constructed GETs did not exercise.

The same browser journey now has Item Create authority and verifies filtered no-match wording excludes “Create your first”, a visible Clear filters link restores real rows, and an empty authorized current-Item collection offers first-record creation. Actual production archive/unarchive operations establish the empty visible scope. Inserting additional archived matching Items must leave both no-match and empty-scope result wording/controls exactly unchanged. The active Items are restored before the remaining combined-search journeys.

These additions passed `node --check internal/handlers/testdata/browser/discovery-persisted.cjs` and were gofmt-formatted. The earlier complete-01/02 Chromium PASS results predate these assertions. Their actual Chromium execution passed in the stable-source `acceptance-final-01` run; see the final result above.
