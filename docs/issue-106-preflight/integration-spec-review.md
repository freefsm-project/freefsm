# #106 independent integration SPEC review

## Verdict

**Scoped disposition: all findings from this integration SPEC review are resolved in the source identified below.** Independent unchanged zero/all-list repros, expanded discovery browser, ordinary interaction browser and forced-CAS-conflict interaction browser passed. A's canonical-navigation repair and C's subsequent full-suite regression repairs are approved in the appended reviews. B's flash/parser/adapter security qualification remains separately reviewer-owned. Historical failures are retained. Whole-repository qualification is separately coordinator-owned; this is scoped review approval, not a final-tree/full-suite claim.

## Final-freeze independent recheck

### Subsequent zero/Retry correction review — resolved

Read final `listHasFilters`: only `status_id`, `asset_type_id` and `asset_status_id` treat `"0"` as absent; text aliases remain active. Reviewed the actual updated interaction script and retained failed trace state. The ordinary Retry path now waits for successful preference completion and visible persisted rows before navigating. The forced-interleaving path performs a genuine CSRF-protected competing preference POST, requires 204, then requires a failure notice/hidden rows before explicit Retry filters and successful preference completion. Existing scroll position, detail navigation, Back URL and `Context row 055` assertions remain. This exercises recovery rather than suppressing errors or weakening fail-closed behavior. Production `interaction.js` retains its previously reviewed SHA-256.

D's wording change accepts “No matches” while still requiring `data-list-empty="no-matches"`, rejecting “create your first”, and testing clear-filter recovery. It does not remove the semantic regression assertions.

Independent final verification used child `umask 022`, the durable environment, and the unchanged reviewer overlay. All three commands returned **0**, with no skips:

```sh
umask 022
source /home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/env.sh
CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test \
  -overlay "$E/integration-spec-zero-overlay-648728.json" -race ./internal/handlers \
  -run '^TestIndependent(ZeroTextFilterNoMatch106|AllCollectionEmptyRecovery106)$' \
  -count=1 -v -timeout=3m
BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers \
  -run '^(TestAccessibleInteractionPersistedBrowser|TestDiscoveryPersistedBrowser)$' \
  -count=1 -v -timeout=6m
FREEFSM_INTERACTION_STALE_GET=1 BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' \
  go test -race ./internal/handlers -run '^TestAccessibleInteractionPersistedBrowser$' \
  -count=1 -v -timeout=4m
```

Actual durations: unchanged overlay **6.093s** (two roots plus nine collection subtests); ordinary browser pair **21.007s**; forced-conflict browser **8.587s**. Log: `$E/integration-spec-resolved-20261127T043044Z-669561.log`.

Artifacts: discovery `$E/browser-artifacts/acceptance-discovery-1795753894871467316/`; ordinary interaction `$E/interaction-retry-0SMJNf/`; forced conflict `$E/interaction-retry-yYjoWi/`.

The former hidden-row timeout is closed as a test synchronization/recovery expectation issue: reviewed retained red trace shows a genuine competing commit followed by 409, and independent forced-conflict execution now proves explicit recovery with unchanged navigation/scroll assertions. No production failure hiding or CAS check was removed.

Final source SHA-256 values were identical before and after this run:

| Source | SHA-256 |
|---|---|
| `cmd/freefsm/static/js/interaction.js` | `756caf3270ceee405ad0883502b0c776edb917a0c843fee3ae79c8823844fd55` |
| `internal/templates/list_state.go` | `112e2784f9021e2db271bbeb6ea4b834ea3f33305ffc001fd03eb8ba29e46a92` |
| `internal/handlers/testdata/browser/interaction.cjs` | `cecb666d0d01164e3b1289b3f84594d8ef4dce22abf847aab4984a36841c7a03` |
| `internal/handlers/testdata/browser/discovery-persisted.cjs` | `c3923897846cccb2a409651e60bc2324112f3dcd5e0e770ba6d096174a5ae36e` |
| `internal/handlers/discovery_empty_test.go` | `63798f7b4897513e0283c77b9fa93c2293350e08c2f696e9c38b8e1530da0585` |

The remaining subsections preserve earlier freeze observations and failures; their open statuses are superseded by this resolved disposition.

Verified every entry in `$E/results/ui-spec-fix-freeze.sha256` and `$E/collection-final-review-7XL6AAkr/source-after.sha256` with `sha256sum -c`: all matched. Read final main-handler wiring: Jobs uses `JobOperationalPage`, Estimates/Invoices use `DocumentListPage`, Contacts/Locations use `CustomerChildrenPage` only on main-list branches. Read the bounded domain implementations and the separated collection Unicode versus historical export normalizers. The earlier active A/B corrections are now complete in this inspected source; the peer's collection/security disposition is resolved, not a claim that this reviewer reran its whole suite.

### Finding 1 resolved

`PaginationLink` now uses native links without a fragment target and retains HTMX only with a valid target. Independently reran the **unchanged** original Contact browser overlay command recorded below: exit 0, **5.788s**. Page two replaces page-one identities and leaves exactly one main content element. Also independently ran expanded `TestDiscoveryPersistedBrowser`: **passed, 14.86s**, including actual Contact **and Location** Next/Previous/Back identities, persisted state, and Item no-match/clear-filter/hidden-record checks.

### Finding 2 partially resolved — P2 literal zero is still misclassified

The unchanged original Item overlay now passes: exit 0, **3.308s**; `Create your first item=false`, no-match wording true. Shared `ListEmptyState` correctly provides page recovery from authorized query totals, and does not probe hidden rows.

However, `internal/templates/list_state.go:50–52` ignores `"0"` for **every** key in `listRemovableFilters`, including free-text `q` and `search`. An existing visible Item plus either `/items?q=0` or `/items?search=0` renders `data-list-empty="empty"` and “Create your first item”, omitting no-match recovery. `"0"` is valid literal text, not a numeric-reference sentinel. Restrict zero-as-absent handling to the numeric filter keys; text `"0"` must count as an active filter.

Independent real-DB repro, exit **1**, **2.161s**, both aliases failed:

```sh
CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test \
  -overlay "$E/integration-spec-zero-overlay-648728.json" -race ./internal/handlers \
  -run '^TestIndependentZeroTextFilterNoMatch106$' -count=1 -v -timeout=3m
```

Log: `$E/integration-spec-zero-filter-20261127T041331Z-650529.log`.
Source: `$E/integration-spec-zero-filter-648728_test.go`.

Independent all-list HTTP follow-up **passed**, exit 0, **6.723s**: `TestIndependentAllCollectionEmptyRecovery106` uses the same overlay/environment with `-race -count=1 -v -timeout=3m`. It verifies ordinary unmatched text produces no-match state and Clear filters for Customers, Contacts, Locations, Items, Assets, Projects, Jobs, Estimates and Invoices; page two produces out-of-range state and first-page recovery for those nine, and out-of-range state for Leases. Log: `$E/integration-spec-all-empty-20261127T041439Z-652340.log`. This confirms the ordinary cases across all applicable lists; it does not resolve the literal-zero failure above.

### Commands and evidence

Unchanged original repros used precisely their original overlay/`-run` commands shown in findings below. Combined log:
`$E/integration-spec-final-own-repros-20261127T041254Z-648728.log`.

Expanded browser run:

```sh
BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers \
  -run '^(TestDiscoveryPersistedBrowser|TestAccessibleInteractionPersistedBrowser)$' \
  -count=1 -v -timeout=8m
```

Log: `$E/integration-spec-final-expanded-browser-20261127T041254Z-648740.log`.
Discovery passed, but the accessibility journey failed at `interaction.cjs:100` scrolling to a hidden row after the Retry scenario. Overall exit **1**, **51.219s**. An isolated repeat using the same environment/options and `-run '^TestAccessibleInteractionPersistedBrowser$' -timeout=4m` also failed at the same location: exit **1**, **37.257s**. Log: `$E/integration-spec-final-interaction-recheck-20261127T041413Z-651669.log`. This is an unresolved, independently repeated verification failure, not yet attributed to a production defect versus test synchronization. No passing full-browser qualification is claimed.
Discovery artifacts: `$E/browser-artifacts/acceptance-discovery-1795752780666200911/`.

Reviewed hashes:

| Source | SHA-256 |
|---|---|
| `internal/templates/list_state.go` | `97ca3fadc8de9eecb1d12ddebf7c7453672653874517aa12c6c3a857b0e3dfdf` |
| `internal/templates/list_state.templ` | `e8cc305db12d9f9188bc0c2c5ab5d82ee1d6687f5cd67dcdf2e84a0a2b5c99b5` |
| `internal/templates/pagination.templ` | `99ab8f8e560372fcec854e0271e38910bbf0ec7ca54ac056a992d368b014f010` |
| `internal/templates/customer_children.templ` | `7bc063ec445592d3fca48e7195bbf1450f4677d93b25f42b949efa69149eae40` |
| expanded discovery browser script | `9492b776c47d99f9f1096b53ae6b5883808840e8e4c70a52af543c72e63facde` |

No production edits or Git mutations. `git diff --check` passed during this recheck. Sections below preserve original review evidence and interim status, superseded by this final-freeze disposition.

Reviewed actual tracked diff from `623a8655fe44502db4078da79653e5bb3de67c1d` **and untracked production/test files**, not just handoff summaries. Read AGENTS, #106's eleven criteria, normative #18 DISC-03/DISC-06/MAS-01/MAS-07/LEA-06/ARC-04/UX-02/UX-04, contract/corrections, UI handoff and actual acceptance test bodies. Scope is completed HTTP/UI/list-state integration; narrow discovery-engine findings remain in `discovery-independent-review.md`, and a separate peer owns collection/state security.

## 1. P2 — Contact/Location pagination links cannot navigate in the actual browser

**Locations:** `internal/templates/customer_children.templ:74`; `internal/templates/pagination.templ:21–28` (and previous-page branch at 9–16).

`CustomerChildrenPage` calls `Pagination` without a `Target`. The shared pagination component nevertheless emits `hx-target=""` and `hx-sync=":replace"`. Clicking Next in the real browser triggers an HTMX error rather than navigating to the next authorized page.

**Independent reproduction executed:**

1. Seed 30 real Contacts under an authorized Customer.
2. Authenticate through the production session/router/CSRF stack and load `/contacts?search=PaginationReview106`.
3. Wait for the real preference POST to report “Filters remembered.”
4. Click the visible `.pagination-nav a` Next control.
5. Observe `PAGEERROR Cannot read properties of null (reading 'htmx-internal-data')`; URL never reaches `page=2`, and the navigation assertion times out.

This fails #106 AC6/AC7 and UX-02 for the ten main lists. The issue is in a shared pre-existing call pattern now used by the integrated main collections; it is not an unfinished A/B selector correction. Contact and Location main lists both use this same component. The new HTTP page-size test passes because it constructs successive GET URLs directly (`discovery_collections_test.go:84–103`), rather than clicking the generated links.

**Correction:** provide a valid target/swap contract for full child-collection pages, or omit incompatible HTMX attributes and let their normal link navigation work. Add real click/Back coverage for both main child collections. Merely making successive manual GETs return the expected records does not close this defect.

Durable independent test inputs:

- `$E/integration-spec-pagination-618121_test.go`
- `$E/integration-spec-pagination-618121.cjs`
- `$E/integration-spec-pagination-overlay-618121.json`

Executed:

```sh
BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' \
go test -overlay "$E/integration-spec-pagination-overlay-618121.json" \
  -race ./internal/handlers -run '^TestIndependentContactPaginationBrowser106$' \
  -count=1 -v -timeout=3m
```

Exit **1**, one failed real Chromium test, **33.887s**.
Log: `$E/integration-spec-pagination-20261127T035131Z-620818.log`.

## 2. P2 — Filtered dedicated lists still present “create your first” empty collections

**Location:** `internal/templates/items_index.templ:57–62`. Same conditional pattern is present in `assets_index.templ:77–80`, `projects_index.templ:52–55`, `estimates_index.templ:60–63`, and `invoices_index.templ:56–59`.

For an authorized user with an existing Item, `/items?search=NoMatchingReview106` returns 200 and “No items found” / **“Create your first item”**. It does not distinguish “no matching records” from an empty visible collection. The condition checks only the current page's length, so an out-of-range page also gets first-record wording.

This is inherited wording rather than a newly introduced regression, but it remains an evidenced gap in #106's explicitly normative **UX-04** requirement (“no records vs no matches/Create-clear filters”) on the ten main lists now in scope. Combined search and Lease already have explicit separate states; preserving their correct behavior does not establish the other lists' conformance.

**Correction:** distinguish authorized empty scope, filtered no-match and out-of-range page using domain-safe information; offer clear-filter/page recovery for the latter cases rather than claiming there are no records yet. Do not count hidden records to choose wording.

Independent real-database HTTP repro:

```sh
CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' \
go test -overlay "$E/integration-spec-empty-overlay-618121.json" \
  -race ./internal/handlers -run '^TestIndependentItemNoMatchWording106$' \
  -count=1 -v -timeout=3m
```

Exit **1**, one failed test, **2.692s**. Fixture creates a visible Item, requests an unmatched query and observes `contains Create your first item=true; contains no-match wording=false`.

Inputs: `$E/integration-spec-empty-618121_test.go`, `$E/integration-spec-empty-overlay-618121.json`.
Log: `$E/integration-spec-empty-20261127T035303Z-624324.log`.

## Known peer corrections — separate status

- **Explicit Lease `facet=base` after base grant loss:** correction is now present in `authorization/list_state_query.go`; independently reran the real HTTP/restoration/preference/commit-callback regression. **Passed.** Blank facet still legitimately selects Billing-only default; explicit base rejects.
- **Document query Unicode length:** `NormalizeDocumentListFilter` now validates UTF-8 and 500 runes rather than the prior byte limit. Independently reran 334/499/500 multibyte-rune GET/save/restore cases and 501/invalid UTF-8 rejection. **Passed.**
- **Five collection bounded-query correction:** new `authorization/collection_pages*.go` files are present and were read. They introduce bounded SQL pages with existing domain DTOs, authorization and repeatable-read count/page boundaries, rather than substituting combined-search identities. At the last callback inspection, Jobs/Estimates/Invoices still called the unbounded report path and sliced `report.Rows`, and Contacts/Locations still used their older collection path. This is an **announced active correction**, not a duplicate newly discovered finding; it requires final callback/test recheck after A's handoff.
- **Sensitive Billing persistence race fixture:** inspected the correction that finalizes the Lease before inserting `lease_billing_period_failures`. D's final targeted race suite is separately in flight; no passing result is inferred here from the fixture edit.

Known-correction command:

```sh
CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers \
  -run '^TestCollection(LeaseExplicitBaseRequiresCurrentBaseViewHTTP|DocumentUnicodeQueryBoundaryHTTP)$' \
  -count=1 -v -timeout=5m
```

Exit **0**, two top-level tests plus two subtests passed, no failures/skips, **7.609s**.
Log: `$E/integration-spec-known-corrections-20261127T035228Z-623220.log`.

## Integration evidence and requirement coverage

| Criteria | Actual reviewed integration and limits |
|---|---|
| AC1–3: ten identities, active scalar/literal/rank/independent facets | Browser `/search` now calls actor-bound `Discover`, not legacy Role-name `Search`; strict parser and authorized result DTO feed templates. Prior independent engine review and its corrected Lease regression remain applicable subject to final hashes. API remains separate. New HTTP tests use real fixtures and assert identities/rank/noninterference, not just status codes. |
| AC4: Customer relationship matches | Combined results render safe plain-text `Match`; relationship authority stays in the domain engine. Dedicated Customer list retains its matching-relationship projection. No relationship selection is used to grant access to unrelated records. |
| AC5: Lease parity | Combined search exposes Lease-specific fields only when Lease is selected; dedicated Lease preserves lifecycle/queue/ending/relationship/placement/Billing controls, Number ordering and independent projections. Old remembered cookie is no longer authoritative. Explicit base-facet fix independently passed as above. |
| AC6: sizes/sorts/archive/provenance | All ten route specs/defaults/pagination adapters inspected; existing test actually seeds 101 identities per type and traverses 25/50/100 with no duplication. Hidden conversion and archived visibility are tested separately. **Browser Contact/Location pagination finding remains.** |
| AC7: URL precedence/Reset/Back | `listState` treats any explicit state as whole-state override; GET does not write; automatic or no-JS Remember POST is CSRF-protected, canonical, user-bound and original-revision CAS. Reset is POST and preserves tombstone revision. Shared JS owns only URL/scroll history; Back/BFCache clear old DOM and fetch current data. Real Chromium test proves combined Back/page/sort/scroll with refreshed persisted data; representative Customer/Item/Lease save/restore/Reset runs also pass. It does not click Contact/Location pagination. |
| AC8: malformed/current authority/failure | GET and state POST reparsing, query reauthorization, bounded error HTML, no raw DB error echo; unavailable engine returns no partial results. JS explicitly labels prior-query rows while updating, hides uncertain rows on failure, preserves inputs and provides Retry. Genuine delayed response and network-abort browser tests exercise latest-request-wins and no stale row reinsertion after grant/session loss. UX-04 dedicated-list no-match wording remains a finding above. |
| AC9: independent grants/commit safety/audit | `saveListState` reruns the real read and then calls `SaveAuthorizedListState` with a transaction-bound domain validator under the same authority fence as CAS. Presentation writes intentionally do not create business/audit events (DISC-06); narrow `/state` and `/reset` exemptions do not exempt authentication/CSRF. Final sensitive-race qualification remains D/peer-owned. |
| AC10: preservation | Migration 126 is additive empty `list_states`, scoped company/user/key, no permission grant or historical backfill. The populated upgrade test constructs conversion/reversion/archived history/retired field/Comment/File/Lease/custom Role, snapshots pre-existing rows and sequences, migrates twice, and verifies identical prior data plus authorized discovery/provenance behavior. Independently passed. |
| AC11: meaningful real DB/browser/API tests and required gates | Eight selected integration roots independently passed with real PostgreSQL and Chromium, including API denial and populated migration. Independent browser repro nevertheless fails. No frozen final full `make test` result was verified; historical focused counts and earlier lint runs do not satisfy the final gate. |

### Independently executed focused integration suite

All commands sourced:

`/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/env.sh`

This supplies the explicit disposable `FREEFSM_TEST_DATABASE_URL`, durable evidence root `$E`, browser executable and Node path.

```sh
BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' \
go test -race ./internal/handlers \
  -run '^TestDiscovery(PersistedBrowser|TenCollectionsPageSizesHTTP|ServerStateURLResetHTTP|MalformedAndRestoredAuthorityHTTP|WholeSearchFailureHTTP|PreferenceWriteFailureIsWholeFailureHTTP|PopulatedUpgradePreservesHTTPHistory|FieldAPIFacetAndOfficeBoundary)$' \
  -count=1 -v -timeout=15m
```

Exit **0**, **eight top-level tests passed**, zero failures/skips, **104.470s**. Includes ten collection subtests, malformed-input cases, dependency failures and the actual persisted browser journey.

Log: `$E/integration-spec-independent-20261127T035017Z-618121.log`.

Chromium **151.0.7922.34** trace/screenshots/browser assertions:
`$E/browser-artifacts/acceptance-discovery-1795751481962723936/`.

This run compiled an interim integration tree while peer corrections continued; it is deliberately not represented as qualification of later selector changes.

## Review limits and source identity

No production edits, nested agents, Git/GitHub mutations, full-suite run, or runtime shutdown. Reviewer-only repro files live under durable evidence and are injected with Go overlays. `git diff --check` returned 0 during review.

Observed source hashes, including JS, list adapters, known-correction sources, new selectors, templates and migration:
`$E/results/integration-spec-observed-20261127T035336Z-624968.sha256`.

Final acceptance requires the new findings to be fixed and rerun, A's changed callback/selector handoff to be rechecked, D's corrected race results to be recorded, and the coordinator's required exact-final-tree gates. No historic pass count is substituted for those checks.

## Subsequent full-suite regression repair — A's canonical browser expectations

**Approved within this three-script scope; no actionable finding.** Independently read the actual diffs and complete scripts for `internal/handlers/testdata/browser/{item-permissions,navigation,navigation-details}.cjs`, the supplied summary, baseline failure log and final passing log. Verified all three current files against `$E/results/browser-canonical-owned-source.sha256` with `sha256sum -c`: all matched. No rerun was necessary for these assertion-only changes; the execution evidence below is A's inspected run, not a new reviewer execution.

- **Exact canonical state:** replacement assertions require the same origin, exact collection pathname, empty fragment and exact sorted query-entry arrays. They reject extra or duplicate parameters, not merely accept any query string. Defaults match #106: page 1, size 25, empty q and each domain's sort. Customer Back retains exact search `Navigation`, descending name sort and page 2.
- **Item permissions/facets retained:** real reader creation, absent Cost/Price inputs, admin edits of protected fields, reader base edit, absence of protected values in reader output, and admin confirmation that protected persisted values survive are unchanged.
- **Detail navigation/facets retained:** all five collections still require real record links, detail/edit navigation, undisclosed pricing absent from DOM/editor payload, persisted edit reload and Estimate title save without pricing. Return now additionally waits for that record's link. Cancel/unsaved-change handling remains.
- **Navigation/purge retained and strengthened:** workflow-only navigation hiding, mobile menu/configuration, persisted status reload, destination rendering/current-section semantics, exact filtered Back state, scroll restoration and expected first record remain. Authority is still revoked through another account's production role form. Before Back, the test registers a response waiter requiring the same origin, `/customers` and actual **403**; it then asserts canonical state, absence of revoked record text and removal of sidebar entries. Skipping the authorized `#main-content` container wait only on this denied return is correct: denial must not masquerade as an authorized list. Neither absence-of-records nor a changed URL alone can satisfy this denial check.

Inspected baseline `$E/logs/browser-canonical-owned-before.log` shows three bare-URL expectation failures (Items exact equality, Projects return, Jobs navigation); the ten HTTP facet subtests already passed. The intermediate correction's authorized-container assumption on 403 is documented in `$E/results/browser-canonical-owned-summary.md`.

Inspected final command, run with disposable real DB/Chromium and child `umask 022`:

```sh
BROWSER_TESTS=1 go test -race ./internal/handlers \
  -run '^(TestItemPermissionsPersistedBrowser|TestNavigationPersistedBrowser|TestNavigationDetailAffordancesAndFacetsHTTP)$' \
  -count=1 -v -timeout=15m
```

`$E/logs/browser-canonical-owned-after-2.log`: exit 0, **3 roots / 10 subtests passed**, no skips, **21.496s**; all three browser completion messages present. The actual revoked `/customers` 403 is logged. The Item activity context-cancellation error is retained in the evidence, not filtered or recast as another test result.

**Qualification boundary:** this approves A's frozen test expectation repair only. C's other browser/production repairs and B's flash/migration work remain active and require their own final source review/verification. Earlier scoped approvals and this targeted pass do not qualify those changes or establish a passing renewed full repository gate. No production edits, nested agents or test reruns were made for this follow-up.

## Subsequent full-suite regression repair — C final frozen source

**Approved within C's reviewed scope; no remaining actionable finding or weakened assertion identified.** This supersedes C's pending status above. Read the actual diffs/full affected scripts, real production adapter/failure rendering, fixtures and final handoff. All entries of `$E/results/ui-full-failures-freeze.sha256` matched before and after independent focused execution.

### Semantics and assertion review

- **Lease remembered authority:** replacing legacy automatic broadening with repeated 403 is required by #106's current-authority/no-silent-dropping contract. Both Billing-filter and archived-filter cases first wait for a durable revision, then revoke the relevant grant. Two ordinary entries must deny and expose no results/sensitive text. Two Reset-review GETs must leave the complete stored state identical. The actual POST includes the rendered CSRF token and Referer and traverses `BrowserCSRF`; it must return 303, advance revision and leave an empty tombstone. A subsequent **authorized** `q=` save at the old revision must return 409 without changing the tombstone, so that assertion tests CAS rather than merely failing authorization. Following Reset must show the named authorized default Lease and no revoked controls/data; explicitly requesting the forbidden filter still returns 403. The new read-only fixture endpoint observes real stored state; it does not implement Reset or spoof a successful result.
- **Reset/empty states/keyboard:** `GET ?reset=1` remains a nonmutating review page. Empty/default checks now explicitly activate its POST Reset button, including keyboard activation. Empty authorized scope, no-match, inaccessible archive, creation-grant absence and visible Lease count assertions remain. Clear-filter recovery checks actual cleared text and visible result count rather than confusing it with a durable Reset operation.
- **Target sizes:** `lease-targets.cjs` has no diff; it still requires every selected target to meet **24×24 fine / 44×44 coarse**, nonempty target sets, correct pointer media and 320px reflow. `ListStateFailure` adds the existing `navigation-link` class to its Reset-review anchor, fixing the target rather than lowering thresholds. Independent browser execution retained measured sets of 1/8/27/51/3 targets per pointer mode.
- **Archived Item:** hidden fields legitimately occur in independent forms. Scoping the DOM count to the search form preserves its invariant; additionally the actual search GET must be 200 and contain exactly one `archived=1`. Record presence, no delete action, archive marker, Restore and persisted active detail checks remain. The Go assertion now correctly expects the remembered archived list to exclude the restored record and an explicit active query to include it; Restore does not implicitly mutate list preferences.
- **Dashboard:** only the expected unavailable string changed. Missing Contacts must still be unavailable rather than empty, independent related Job content must survive, Contacts Retry must restore data, activity transport Retry must clear failure, archived drilldown remains enforced and revoked customer content must disappear. The final log's missing-table/denied-authority errors are expected failure scenarios, not omitted failures.
- **DomainRepair:** `domain-repair.cjs` has no diff. Hidden/archived/clear Location intent, secret-name absence, persisted service-note/Location selection and independent archive/restore navigation assertions remain intact. Inspected final evidence contains completion for all three phases. No temporary diagnostics or permissive URL workaround remains.
- **Production integration:** `listState` clears outer flash context before parsing, supplies accepted notification text only after successful parsing, and keeps canonical/persisted state separate. Failure form excludes both `reset` and `flash`. Buffered denial, GET nonmutation, explicit Reset CAS and transaction-bound save validation remain unchanged. Detailed parser/adapter security and migration review belongs to B's reviewer. No unrelated production regression identified in this inspected integration.

### Actual verification and source identity

Inspected C's seven-root/19-subtest execution: `$E/logs/ui-full-failures-final-04.log`, exit 0, zero skips, **41.670s**. This includes dashboard, DomainRepair and flash transport; these three were source/evidence reviewed, not independently rerun here.

Independently executed:

```sh
umask 022
source /home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/env.sh
BROWSER_TESTS=1 CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS='-p=1' go test -race ./internal/handlers \
  -run '^(TestLeaseWorkspaceRememberedAuthorityBrowser|TestLeaseWorkspaceEmptyStatesBrowser|TestLeaseWorkspaceRelationshipSearchHTTP|TestMasterItemArchivedListPersistedBrowser)$' \
  -count=1 -v -timeout=5m
```

**Exit 0, four roots / nine subtests passed, no skips, 25.390s.**
Log: `$E/integration-spec-c-regression-20261127T073404Z-987832.log`.

Hash inventory: `$E/results/ui-full-failures-freeze.sha256` (all matched twice). Selected current SHA-256:

| Source | SHA-256 |
|---|---|
| `internal/handlers/list_state.go` | `e6817bfc901a25dd68f2ccda4877ecc82f67805a14c10c4b8643128e17eea521` |
| `internal/templates/list_state.templ` | `a14701db6f0d2dc7f9b08ca7ea4520eb3f254dd8182ba7ce1af69c62eceee343` |
| `lease-workspace-recovery.cjs` | `5933efb720429f6cd6fdafbb6e4e4f650311771e47a5b59a7f336da084732eff` |
| unchanged `domain-repair.cjs` | `fce413f97e55cff28e44c3d1d8e746fbcc2b4395604e10c4582c29634bbe875f` |

No production edits, nested agents or full-suite execution. Renewed full repository qualification and B's separate security disposition remain outside this scoped approval.
