# #106 C integration handoff (not whole-scope acceptance)

## Implemented

- Combined discovery uses A's actual `Discover` API with strict B parsing, empty-query listing, eligible types, Lease-specific filters, global size/sort/page controls and bounded errors.
- GET resolves URL whole-state override / server preferences / defaults without writing. Successful pages POST canonical query + original revision to `/search/state` with CSRF. POST reparses and reruns the read boundary before CAS save. Reset uses `/search/reset`.
- Shared adapter: `listState(authority,key,spec)` GET middleware; `saveListState(authority,key,spec,read)` protected POST; `resetListState(authority,key,spec)` protected Reset. Dedicated POST `read` must include the same `collectionRead` middleware as its GET where applicable.
- Shared templates provide state/Reset and size/sort controls, preserve effective state on paging/search, and emit current state in toolbar partials. Search and standalone Lease/Contact/Location layouts carry presentation controls directly.
- Browser behavior: canonical URL history, fresh Back/BFCache, request ordering, explicit previous-query labels, retained filters with hidden uncertain results, distinct preference failure, original-revision CAS, current-authority revalidation.
- Two narrow additions in `handlers/save_actions.go` exempt only the eleven collections' `/state` and `/reset` routes from business receipt middleware/form enhancement. Predicate is owned by the new list-state adapter. Presentation operations still use authentication/CSRF and fresh authorization.

## Dedicated integration after ownership split

Dedicated handler/service files were assigned to B; C has not edited them:

- `handlers/{customers,customer_children,assets,jobs,projects,estimates,invoices,leases,items}.go`
- Relevant existing service pagination adapters (and dedicated authorization read adapters if needed, explicitly coordinate ownership).
- New `handlers/collection_list_specs.go`, exposing `collectionListSpec(key string) liststate.Spec`.

Preserve legitimate filter names, projections and actions; consume strict 25/50/100 and deterministic sorts; retire Lease cookie authority. Contacts/Locations main collections are registered by `registerCustomerChildren` in `customer_children.go`, not separate contacts/locations handler files.

C retains router/templates/JS ownership. `registerCollectionList` now wires GET middleware and both POST routes for all ten collections. POST callbacks reproduce GET's `collectionRead` gates for Jobs, Projects, Assets, Estimates and Invoices. Customer, Contact, Location, Item and Lease reads retain their owning domain boundaries. B removes the two global base GET registrations from `registerCustomerChildren`; C registers them centrally.

Toolbar generic sort choices are filtered against the actual route validator (including document/schedule sort names). Toolbar filter/search forms and pagination carry complete effective state, excluding edited singleton controls and transient page. Contact/Location/Lease custom forms preserve sort and other nonedited keys. Clear/current links explicitly override saved state. Related sublists remain outside this middleware. Existing interaction-browser URL assertions now compare canonical URLs/parameters rather than bare `/customers`; retained-failure assertion uses the new explicit authority-unavailable wording.

### Commit-time authority correction

D demonstrated that separate authorized read followed by a preference transaction allowed `revoke,state`. HTTP now calls B's `SaveAuthorizedListState(..., validate func(pgx.Tx, Principal) error)` rather than ordinary Save. Search callback uses A's shared `ValidateDiscoveryQuery` under that same fence and transaction before CAS. Dedicated callbacks use B's `ValidateCollectionListQuery(ctx,tx,principal,key,values)` helper, also used by dedicated reads. No nested read transaction is used. The existing pre-read proves actual query availability only. Reset remains intrinsic after grant loss.

`GOFLAGS='-p=2' go test -race ./internal/handlers -run '^TestDiscoveryGrantLossBetweenReadAndPreferenceCommitHTTP$' -count=1 -v -timeout=3m` passed (1 test, no skips, exit 0), `$E/logs/ui-commit-authority-race-01.log`.

## Actual checks

Environment: `source /home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/env.sh`.
Logs/results below are under that `$E`; no full suite or runtime shutdown.

| Exact main command | Result / evidence |
|---|---|
| `GOFLAGS='-p=2' go test ./internal/handlers ./internal/templates ./internal/services -run '^$'` | exit 1, D in-progress test passed float to string SetTaxRate; `logs/ui-compile-01.log` |
| `GOFLAGS='-p=2' go test ./internal/handlers -run '^TestDiscoveryTenTypesProductionHTTP$' -count=1 -v` | initial exit 1 same compile error (`ui-ten-types-01`); rerun exit 0, 1 pass, no skips (`ui-ten-types-02`) |
| `GOFLAGS='-p=2' go test -race ./internal/handlers -run '^TestDiscovery(ServerStateURLResetHTTP\|MalformedAndRestoredAuthorityHTTP\|WholeSearchFailureHTTP\|PreferenceWriteFailureIsWholeFailureHTTP\|InFlightSaveCannotResurrectResetHTTP)$' -count=1 -v` | exit 1 during concurrent test rename; `logs/ui-state-http-01.log` |
| `GOFLAGS='-p=2' go test -race ./internal/handlers -run '^TestDiscovery(ServerStateURLResetHTTP\|MalformedAndRestoredAuthorityHTTP\|WholeSearchFailureHTTP\|PreferenceWriteFailureIsWholeFailureHTTP)$' -count=1 -v` | exit 0; 4 top-level tests + 17 subtests pass, no skips; `logs/ui-state-http-02.log` |
| `BROWSER_TESTS=1 GOFLAGS='-p=2' go test ./internal/handlers -run '^TestDiscoveryPersistedBrowser$' -count=1 -v -timeout=10m` | exit 0; 1 browser test pass, no skips; `logs/ui-browser-01.log`; artifacts path in log |
| `GOFLAGS='-p=2' go vet ./internal/handlers ./internal/templates ./internal/services` | exit 0 twice; latest `logs/ui-vet-02.log` |
| `node --check cmd/freefsm/static/js/interaction.js` | exit 0; `logs/ui-js-syntax-01.log` (later JS was also exercised by passing browser test) |
| `/home/adam/go/bin/templ generate` | initial syntax failure corrected; latest exit 0 `logs/ui-generation-03.log` |

Additional actual checks after ten-list wiring (before the commit-authority callback change):

| Exact main command | Result / evidence |
|---|---|
| `GOFLAGS='-p=2' go test -race ./internal/handlers -run '^TestMainCollection(StateHTTP\|PreferenceReauthorizesHTTP)$' -count=1 -v` | First run: state test passed all ten routes, denial test expected 403 instead of existing Contact/Location 404 (`ui-ten-list-state-01`). Corrected test preserves existing 404. Rerun exit 0, 2 top-level + 19 subtests passed, no skips (`ui-ten-list-state-02`). |
| `BROWSER_TESTS=1 GOFLAGS='-p=2' go test ./internal/handlers -run '^(TestDiscoveryPersistedBrowser\|TestCustomerMastersPersistedBrowser\|TestAccessibleInteractionPersistedBrowser)$' -count=1 -v -timeout=15m` | Customer Masters and Discovery (including dedicated Customer/Item/Lease) passed. Interaction failed on outdated bare URL expectation; `logs/ui-combined-browser-01.log`, exit 1. |
| `BROWSER_TESTS=1 GOFLAGS='-p=2' go test ./internal/handlers -run '^TestAccessibleInteractionPersistedBrowser$' -count=1 -v -timeout=15m` | After canonical-URL/status assertion update: exit 0, 1 pass, no skips; `logs/ui-interaction-browser-02.log`. |
| `/home/adam/go/bin/templ generate -check` | exit 0, no updates; `logs/ui-templ-check-01.log` |
| `node --check cmd/freefsm/static/js/interaction.js` | exit 0; `logs/ui-js-syntax-02.log` |
| `git diff --check` | exit 0 |

`GOFLAGS='-p=2' make lint` also passed (exit 0), `$E/logs/ui-make-lint-01.log`.

Individual `.exit` files are in `$E/results`. These are focused interim results, not frozen-source full acceptance.

## Latest complete focused attempt and correction

`BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers -run '^(TestDiscovery.*|TestMainCollection.*|TestCustomerMastersPersistedBrowser|TestAccessibleInteractionPersistedBrowser)$' -count=1 -v -timeout=15m`

- Exit 1; 26 top-level pass / 2 fail / 0 skip (including subtests: 80 pass / 6 fail / 0 skip).
- All three actual Chromium browser journeys passed.
- D's ten-collection 101-record/25–50–100 suite passed all ten subtests.
- Original commit-authority race, assigned-snapshot race, queued Reset race, API and populated migration tests passed.
- New D sensitive race Billing fixtures failed before exercising production: a draft Lease cannot be inserted into `lease_billing_period_failures`, whose FK references `lease_finalizations`. Reported to coordinator/D; C did not edit that fixture.
- C's child-denial expectations reflected an interim B 403 response; B subsequently restored the original 404 concealment. C now asserts 404 for Contact/Location and maps commit-time `ErrNotFound` to 404.
- Logs/counts: `logs/ui-focused-complete-01.log`, `results/ui-focused-complete-01-counts.json`.

After final denial mapping and parsed-navigation-link assertions:

`GOFLAGS='-p=2' go test -race ./internal/handlers -run '^(TestMainCollectionPreferenceReauthorizesHTTP|TestNavigationCollectionsFilterBeforePaginationHTTP|TestDiscoveryGrantLossBetweenReadAndPreferenceCommitHTTP)$' -count=1 -v -timeout=5m`

passed (3 top-level + 9 subtests, no skips), `logs/ui-current-authority-navigation-01.log`. Intermediate denial expectation churn is retained in `logs/ui-ten-list-denials-03.log` (exit 1); the latest run above is authoritative for that correction. Navigation assertions now parse actual anchor `page` parameters; `per_page=25` no longer produces a false page-2 assertion.

Final C static gates: `GOFLAGS='-p=2' make lint` and `/home/adam/go/bin/templ generate -check` both exit 0 (`logs/ui-make-lint-02.log`, `logs/ui-templ-check-02.log`, zero template updates). C production source is ready to freeze. Remaining cross-owner checks: D's finalized-Lease Billing race fixture correction/reexecution, and B's explicit `facet=base` permission check (reported: blank facet may auto-select Billing, explicit base must require base View). Neither is claimed passed here.

## C-owned changed files (complete inventory)

```text
cmd/freefsm/static/js/interaction.js
docs/issue-106-preflight/implementation-contract.md
docs/issue-106-preflight/ui-handoff.md
internal/handlers/list_state.go
internal/handlers/list_state_collections_test.go
internal/handlers/navigation_integration_test.go
internal/handlers/router.go
internal/handlers/save_actions.go
internal/handlers/search.go
internal/handlers/testdata/browser/interaction.cjs
internal/services/search.go
internal/templates/assets_index.templ
internal/templates/assets_index_templ.go
internal/templates/base.templ
internal/templates/base_templ.go
internal/templates/customer_children.templ
internal/templates/customer_children_templ.go
internal/templates/customers_index.templ
internal/templates/customers_index_templ.go
internal/templates/discovery.go
internal/templates/estimates_index.templ
internal/templates/estimates_index_templ.go
internal/templates/invoices_index.templ
internal/templates/invoices_index_templ.go
internal/templates/items_index.templ
internal/templates/items_index_templ.go
internal/templates/jobs_index.templ
internal/templates/jobs_index_templ.go
internal/templates/lease_index.templ
internal/templates/lease_index_templ.go
internal/templates/list_state.go
internal/templates/list_state.templ
internal/templates/list_state_templ.go
internal/templates/list_toolbar.templ
internal/templates/list_toolbar_templ.go
internal/templates/navigation.go
internal/templates/pagination.templ
internal/templates/pagination_templ.go
internal/templates/projects_index.templ
internal/templates/projects_index_templ.go
internal/templates/search_results.templ
internal/templates/search_results_templ.go
internal/templates/types.go
```

B owns dedicated handler/service/spec/storage additions; A owns discovery; D owns new discovery tests/scripts. Those are excluded from this C inventory. No Git mutations, full repository suite or runtime shutdown were performed.

## Final SPEC correction / C production freeze

The two findings in `integration-spec-review.md` are corrected:

- `PaginationLink` emits native semantic links when no fragment target exists, rather than invalid `hx-target=""` / `hx-sync=":replace"`. Contact and Location main collections can click Next/Previous and return through browser history. Targeted fragment pagination retains its existing HTMX contract.
- All applicable ten-list empty states now distinguish empty authorized view, filtered no matches, and out-of-range page. `ListEmptyState` uses only the existing **authorized current-query total**, row absence, page and explicit filters. It never probes hidden records or infers whole-collection emptiness from a filtered count. Only an unfiltered zero-total view can show a creation action; Item's “Create your first item” is restricted to that case. Archive-only empty views do not offer creation.
- Scope-preserving Clear removes search/status/type/lifecycle/ending filters but retains scope, customer/relationship context, archive/facet, sort and size. First-page recovery retains the complete query. Both recovery actions use ordinary navigation. Lease retains its existing domain-authorized existence distinction and gains out-of-range recovery.
- No A-owned handler, selector or DTO was edited for these corrections.

### Actual final checks

All commands source the same `$E/env.sh` above.

| Command | Result / durable log |
|---|---|
| `BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -overlay "$E/integration-spec-pagination-overlay-618121.json" -race ./internal/handlers -run '^TestIndependentContactPaginationBrowser106$' -count=1 -v -timeout=3m` | Reviewer reproduction unchanged; exit 0, 1 pass / 0 skips. `logs/ui-review-pagination-final-02.log` |
| `CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -overlay "$E/integration-spec-empty-overlay-618121.json" -race ./internal/handlers -run '^TestIndependentItemNoMatchWording106$' -count=1 -v -timeout=3m` | Reviewer reproduction unchanged; exit 0, 1 pass / 0 skips; no first-Item wording and explicit no-match wording. `logs/ui-review-empty-final-02.log` |
| `BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers -run '^(TestDiscoveryPersistedBrowser|TestAccessibleInteractionPersistedBrowser)$' -count=1 -v -timeout=8m` | Exit 0, 2 pass / 0 skips. D's expanded real-browser assertions cover both Contact/Location Next/Previous/Back identities and Item no-match→Clear vs authorized-empty scope with hidden-Item noninterference. Shared interaction also passes. `logs/ui-review-browser-green-03.log`; Chromium 151.0.7922.34; discovery artifacts `browser-artifacts/acceptance-discovery-1795752589928479264/`. |
| `GOFLAGS='-p=2' make lint` | Exit 0; `logs/ui-review-make-lint-final.log` |
| `/home/adam/go/bin/templ generate -check` | Exit 0, zero updates; `logs/ui-review-templ-check-final.log` |
| `git diff --check`; `gofmt -l` on C Go files | Exit 0 / no output |

Failed iterations are retained, not represented as passes: both initial unchanged repro runs were compile-blocked during A's normalization/parser handoff (`ui-review-*-red-01/02`). The first expanded browser run identified semantic-button pagination links and an old test assumption that Clear reset sort/size (`ui-review-browser-regression-01`). The diagnostic run proved Clear correctly preserved descending sort/size, placing the old expected Customer on page two (`ui-review-browser-debug-02`). Tests now explicitly assert retained presentation before their separate transport-failure scenario. Temporary `[DEBUG-i106-clear]` instrumentation was removed and grep returns no matches.

**C production is frozen after these corrections.** Generation is fresh and the file inventory above remains complete. Coordinator owns final repository-wide qualification; no full suite or shutdown was run by C.

## Freeze revoked: literal zero and Retry synchronization correction

This section supersedes the freeze above and its old hash manifest.

- `listHasFilters` now treats zero as absent only for numeric sentinel keys (`status_id`, `asset_type_id`, `asset_status_id`). Literal `q=0` and `search=0` remain active text filters.
- Retained `TestMainCollectionLiteralZeroIsTextHTTP` exercises both aliases across nine shared-empty-state collections, plus numeric status-zero control.
- Unchanged reviewer overlay failed before the fix (`logs/ui-zero-red-01.log`). Both reviewer tests (`TestIndependentZeroTextFilterNoMatch106`, `TestIndependentAllCollectionEmptyRecovery106`) and the retained regression passed together afterward: `logs/ui-zero-green-01.log`, exit 0, three root tests, no skips.

### Retry trace and deterministic reproduction

Ordinary diagnostic runs passed, so they did not establish absence of the reported race. The retained opt-in `FREEFSM_INTERACTION_STALE_GET=1` interleaving commits a real CSRF-protected preference update after a GET renders its revision but before that GET reaches the browser. `logs/ui-retry-interleaved-red-05.log` reproduces the reported `scrollIntoViewIfNeeded` hidden-row timeout. `interaction-retry-0bqc0j/responses.json` records revision 7 committed (204), the GET returned (200), then its automatic preference POST at revision 7 rejected (409). `failure-state.json` identifies hidden table/results ancestors and the preference failure notice; `trace.zip` retains the browser trace.

The ordinary test advanced after Retry's URL/row text appeared, without waiting for the preference POST. It now waits for observable **Filters remembered** and visible persisted rows before the next navigation. The forced-interleaving branch separately asserts that conflict hides rows, clicks explicit **Retry filters**, then requires remembered state before the unchanged scroll/detail/Back assertions. No production JS behavior or authorization/failure hiding was weakened.

Failed diagnostic fixture iterations are retained: `ui-retry-interleaved-red-01/03/04` lacked the Referer required by the real CSRF boundary; `red-02` attempted DOM parsing in the navigating page and timed out. The successful deterministic fixture uses a separate parser page and real form fields/Referer.

### Stable-source checks and remaining blocker

- `FREEFSM_INTERACTION_STALE_GET=1 BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers -run '^TestAccessibleInteractionPersistedBrowser$' -count=3 -v -timeout=5m`: **3/3 passed**, no skips; `logs/ui-retry-interleaved-green-01.log`.
- `BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers -run '^(TestAccessibleInteractionPersistedBrowser|TestDiscoveryPersistedBrowser)$' -count=3 -v -timeout=6m`: interaction **3/3 passed**; discovery **3/3 failed** at D's new assertion line 161 expecting `/no matching/i` when the actual correct established wording is “No matches for these filters.” `logs/ui-zero-retry-final-browser-01.log`. Reported through coordinator; D owns the correction.
- `GOFLAGS='-p=2' make lint`: exit 0 (`logs/ui-zero-retry-lint-final.log`).
- `templ generate -check`: exit 0, zero updates (`logs/ui-zero-retry-templ-final.log`).
- `node --check internal/handlers/testdata/browser/interaction.cjs`, `git diff --check`: exit 0.

**No new freeze claimed pending D's assertion correction and actual browser reexecution.** All trace artifacts remain local evidence; traces can contain session/CSRF data and must not be published unredacted.

## Full-suite regression repairs: final C handoff

This section supersedes earlier C freeze statuses. Read the coordinator's `results/final-quality-dnQIMR3T/failure-summary.md` and extracted failure logs. C repaired its assigned regressions and is now frozen for independent review/final qualification.

Production changes:

- `list_state.go` clears any unvalidated outer-middleware flash context, then supplies only successfully parsed flash through render context. B's parser strips transport before Explicit/default/persistence decisions; canonical URLs and preference data remain flash-free. Flash-only GET restores saved filters; flash plus explicit empty search overrides them. Rejected flash is excluded from failure-form fields.
- `ListStateFailure` uses the existing `navigation-link` target-size treatment for “Review Reset using current saved revision.” Real browser measurements retain the original 24px fine / 44px coarse thresholds.

Legacy browser corrections preserve the current contract:

- Customer dashboard expects current unavailable wording while retaining section-failure/recovery, independent activity Retry, and revoked-authority purge assertions.
- Lease Reset review (`GET ?reset=1`) is nonmutating, not an empty list. Empty-state and keyboard journeys now explicitly submit Reset before asserting defaults.
- Lease remembered-authority browser waits for confirmed persistence before revocation, proves ordinary entry remains blocked twice, proves repeated Reset-review GETs leave stored state unchanged, performs real CSRF-protected Reset, checks an advanced empty tombstone, rejects an authorized stale save with 409 without changing the tombstone, and verifies only the authorized default Lease identity is visible afterward. Both Billing and archived grant-loss scenarios execute.
- Archived Item checks are scoped to `.focused-list-search`, not the independent size/sort form's hidden fields. The actual submitted GET must contain exactly one `archived=1`. After Restore, the remembered archived query stays empty; explicit active query contains the restored record.
- Domain repair's original navigation/identity assertions pass after B's concurrent parser fixes. Temporary response/body diagnostics were removed; there is no retained change to its browser script.
- Lease list journeys wait for observable preference completion before subsequent navigations; failure hiding and no-silent-broadening remain enforced.

### Final executed evidence

Environment: same disposable `$E/env.sh`; each command sets child `umask 022`. No full suite, nested agents, Git mutations, or shutdown performed.

```sh
BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers \
  -run '^(TestCollectionFlashTransportRestoresSavedStateHTTP|TestCustomerDashboardPersistedBrowser|TestDomainRepairPersistedBrowser|TestLeaseWorkspaceRememberedAuthorityBrowser|TestLeaseWorkspaceEmptyStatesBrowser|TestLeaseWorkspaceRelationshipSearchHTTP|TestMasterItemArchivedListPersistedBrowser)$' \
  -count=1 -v -timeout=8m
```

**Exit 0: 7 root tests + 19 subtests passed; zero skips.** Log: `logs/ui-full-failures-final-04.log`; exit: `results/ui-full-failures-final-04.exit`. The dashboard's missing-table and denied-authority log entries are intentional exercised failure cases.

`GOFLAGS='-p=2' make lint` passed (`logs/ui-full-failures-lint-final.log`). `templ generate -check` passed with zero updates (`logs/ui-full-failures-templ-final.log`). `git diff --check` passed.

Failed intermediate attempts remain in `ui-full-failures-focused-01`, `ui-full-failures-focused-02`, and `ui-full-failures-final-03`; the second was green for its four-test scope, while the first/third exposed the remaining legacy assumptions and synchronization described above. None is represented as complete acceptance.

Additional C-owned files beyond the earlier inventory:

```text
internal/handlers/lease_workspace_recovery_test.go
internal/handlers/master_item_archive_test.go
internal/handlers/testdata/browser/customer-dashboard.cjs
internal/handlers/testdata/browser/lease-workspace-empty.cjs
internal/handlers/testdata/browser/lease-workspace-recovery.cjs
internal/handlers/testdata/browser/lease-workspace.cjs
internal/handlers/testdata/browser/master-item-archive.cjs
```

B's specs/parser/flash tests and the separate agent's navigation/item-permission browser files were not edited by C. The earlier inventory's `navigation_integration_test.go` is now separately owned for final navigation corrections.
