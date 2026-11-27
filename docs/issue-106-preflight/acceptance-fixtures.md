# #106 direct acceptance fixture handoff

## Status

This is the initial fixture-design snapshot. Subsequent implemented tests, actual commands/results and open blockers are recorded in [acceptance-execution.md](acceptance-execution.md); current owned test names use `discovery_*_test.go` per the frozen peer contract.

2026-11-26: inspected root AGENTS, all three preflight reports, live GitHub #106/#18 bodies, and the fixture sources below. Approved scope supplied by coordinator: combined search plus ten collections, server-side user state, explicit URL as whole override. This report supersedes no product contract. No production code, existing tests, Git/GitHub state, database or runtime was changed. No acceptance tests executed; every test name below is **proposed**, not present/passed.

New files owned by this acceptance agent after interface handoff:

- `internal/handlers/issue106_fixtures_test.go`
- `internal/handlers/issue106_search_test.go`
- `internal/handlers/issue106_state_test.go`
- `internal/handlers/issue106_failure_race_test.go`
- `internal/handlers/issue106_api_test.go`
- `internal/handlers/issue106_migration_test.go`
- `internal/handlers/issue106_browser_test.go`
- `internal/handlers/testdata/browser/issue106-discovery.cjs`

No interface-dependent helpers have been created yet. Runtime provisioning belongs to the runtime agent. New tests must require its explicit disposable URL and disk-backed `TMPDIR`; browser output/profile/artifacts must be retained under its #106 evidence directory, never `/tmp`. Existing fixture `t.TempDir()` allocations consequently also require that disk-backed environment.

## Reuse verified in source

| Existing fixture | Reuse / caveat |
|---|---|
| `customer_permissions_integration_test.go:35–148` | `permissionHTTPFixture` returns pool, ent client, production web handler, session service, company ID, Admin ID. Creates unique schema, sets `application_name` to that schema, migrates, seeds bcrypt Admin, wraps `New` with company middleware. Drops schema at cleanup: save measurements/dumps before cleanup if evidence needs rows. Missing URL **skips**, so enforce URL externally and reconcile skips. |
| Same file, `permissionHTTPFixtureMigrations` | Supply a filtered migration FS for immediately-pre-#106 upgrade fixtures; populate that schema, then call `database.DB.Migrate` with full migration FS. Do not use `permissionHTTPFixtureSeeded` as an arbitrary latest-base hook: its callback runs after **057**, not immediately before the new migration. |
| `authz_integration_test.go:363` | `sessionCookie` creates a genuine persisted browser session. `requestBody`/`expectStatus` exercise authenticated production routing. Use HTTP login in the new-login browser durability scenario, rather than describing injected session cookies as tested login. |
| `customer_masters_integration_test.go` | `masterPost`, `masterJSON`; production Contact/Location creation under `/customers/{id}/contacts` and `/customers/{id}/locations`; active relationship search fixture and 52-row Customer pagination example. |
| `field_evolution_integration_test.go` | `fieldPost` creates definitions/values through forms, retires definitions/options, converts Estimate and reverts Invoice through actual actions with operation IDs. Read created IDs from persisted rows/redirects; do not copy fixtures' hard-coded definition IDs. |
| `item_permissions_integration_test.go` | Explicit `SaveRole`/`AssignUser`, independent `item.view`, `pricebook.view`, `cost.view`; base-only SKU denial; `sessions.CreateMobile` and `apiv1.NewRouter`; `/items?search=...`, `/items/{id}/selection`, 403 visible-action versus 404 invisible-target. |
| `navigation_integration_test.go` | J/A grants, two legacy role names with equal current grants, Job assignments, foreign company records, hidden counts before pagination. Existing 27 hidden rows are not enough to prove new global top-100 behavior: expand ticket fixture. |
| `lease_workspace_test.go` | Actual Draft registration/detail/Billing/finalization helpers; company-local date via `lease_dependency_today`; future coverage, premises and Asset Number, queues, Billing failure persisted state, explicit archived reference semantics. |
| `lease_workspace_recovery_test.go` | Real-server browser fixture with test-only mutation control endpoints; authority loss between requests. Prefer second Admin browser production forms when practical; test-only endpoints may coordinate real DB faults/barriers, never return invented search results. |
| `testdata/browser/navigation.cjs` | Playwright persistent context, session cookies, mobile 320px, actual Back/scroll assertion, second Admin account revoking grants through production form. New script must assert domain result identities/freshness, not merely `#main-content` existence. |
| `credit_application_matrix_test.go:88–107`, `authority_waiters_integration_test.go` | Observe actual PostgreSQL waiters scoped by fixture `application_name`, blocker PID and statement. Do not reuse commercial fence 190019 blindly: reads may not acquire it. Barrier timeout fails; a sleep is not proof of ordering. |
| `backup_history_integration_test.go:530` | `historyDurableRows` captures sorted complete JSON rows and rejects accidentally empty tables. Reuse for populated preservation; use explicit counts for intentionally empty audit/effect tables. |

Browser server assembly: `httptest.NewServer`, serve actual `../../cmd/freefsm/static` at `/static/`, wrap real web handler with `middleware.BrowserCSRF(middleware.CSRFToken(...))`, retain CurrentPath/Flash wrapper as in navigation test where needed. Browser controls must click actual forms/links, await response plus HTMX settle, and inspect persisted DB state after the process exits.

## Concrete dataset and assertions

Each independent test gets a separate schema. Use fixture-owned IDs returned by writes, not assumed sequences. Define a result observation containing ordered `(type, ID, destination, displayed label, relationship label, archived badge)`, total, page links, available filters and empty/error wording. Extract only the result region, not sidebar/detail links. Normalize only inherently volatile CSRF/request timestamps when comparing observations; never normalize counts/order/labels.

1. **Ten-type core:** one visible record of every type with shared marker `Discovery106`, plus a second Item matching name, description and authorized SKU simultaneously. Seed real Customer children, company/customer-owned Assets with required Asset Type, Job/Project, independently numbered documents and a Draft Lease. Assert exact expected identity set, ten type labels, valid authorized destinations, and each Item once. Time Entry/Pricebook/Cost must not appear as extra result types.
2. **Ranking/paging:** 137 matching records distributed across types, including >100 hidden matching records inserted before a late visible exact-identifier hit; three isolated rank classes (exact configured identifier, prefix, elsewhere), tied labels, repeated terms in fields/relationships. Expected order is derived from the approved fixed type order and actual IDs, not copied from returned order. Traverse 25/50/100 pages and compare concatenation to the full expected sequence, no gaps/duplicates; repeated requests identical. Assert default 25, last page, out-of-range behavior and approved sort variants on every collection.
3. **Authority matrix:** empty Role; only Lease base; only Lease Billing; Item base; base+Pricebook; base+Cost; all master A; master C with Job J; document J; A/J mixed; archived-view absent/present; equivalent current grants with legacy `tech`/`dispatcher` strings and renamed Role. Use explicit valid grant prerequisites. Customer assignment alone must not expand Job/master visibility. Foreign-company matches never appear.
4. **Noninterference:** hold actor, visible records and query fixed; insert/change/remove hidden rows and facet-only secrets. Compare complete result observation before/after, including order, count, page links, choices and no-record/no-match wording. For rank leakage, place a hidden exact SKU on a visible elsewhere-matching Item: rank must remain unchanged for base-only actor, then change only when authorized by approved identifier semantics. Separate positive controls ensure secret values really exist and eligible actors can search the intended facets.
5. **Scalar fields:** unique term only in active short/multiline text, signed precision number, ISO date, single-select or multiselect value, for every type supporting that field kind. Definitions applicable to parent and tenant; retained-global legacy definitions covered separately. A retired definition keeps its persisted value and detail history but loses search hit; reactivation restores hit without rewriting value. Retired option on active definition needs explicit product decision. Terms only in definition labels, unmatched options, foreign definitions, Comment body or actual uploaded text File bytes must not match. Query cases include mixed case, Unicode, literal `%`, `_`, apostrophe, typo (no fuzzy result), blank and max/oversize length.
6. **Customer relationships:** identity does not contain query; two active matching Contacts (name/email/phone cases) and two Locations (title/address cases). Return one Customer with authorized deterministic relationship label; independently remove Contact/Location View and archive child. Neither snippets, rank nor count may identify hidden child. Unrelated sibling and Job do not gain access.
7. **Lease:** unique Number/title/Customer/Asset Number/premises-address queries; exact lifecycle boundaries using company date; relation, ending, archive, placement, each Billing queue alone and composed. Use valid finalized Lease fixtures for Scheduled/Active/Month-to-Month/Terminated rather than pretending stored status controls lifecycle. Base-only Billing-secret query never hits/ranks; Billing-only must not expose title/premises without base grant. Related archived master match needs its own archived grant. Preserve dedicated Number+ID order.
8. **Archive/conversion:** create ordinary archived records with valid domain actions; View archived alone does not opt in, explicit request without grant safely rejects, authorized opt-in shows badge. Convert real Estimate, verify source absent from combined and dedicated results even by exact number; revert Invoice, verify tombstone absent; reconvert, verify new Invoice identity while prior provenance remains stored. Archived/provenance grants do not override tombstone exclusion.

## Proposed executable tests and AC mapping

| Test name | Concrete assertion | #106 AC |
|---|---|---|
| `TestIssue106TenTypesAndFacetNoninterferenceHTTP` | Dataset 1/3/4; complete observation comparison, authorized links, no-grant and Lease-only entry. | 1,3,9 |
| `TestIssue106ActiveScalarAndExcludedContentHTTP` | Dataset 5, positive/negative controls, preservation after retire/reactivate. | 2,9 |
| `TestIssue106GlobalRankAndCollectionPaginationHTTP` | Dataset 2 across combined plus ten collection routes, type subset composition, stable sorting. | 3,6 |
| `TestIssue106CustomerRelationshipAuthorityHTTP` | Dataset 6; independent child grants and deterministic match labels. | 4 |
| `TestIssue106LeaseDiscoveryHTTP` | Dataset 7, company-local lifecycle/filter predicates and sensitive isolation. | 5 |
| `TestIssue106ArchiveAndConversionProvenanceHTTP` | Dataset 8 plus persisted conversion cycle IDs and preserved values. | 6,10 |
| `TestIssue106ServerStateURLResetHTTP` | Persist filters/sort/size using HTTP; fresh session and rebuilt router restore from DB; second user and different collection isolated. URL containing only query clears previously saved other filters; explicit empty differs from no query. Reset then revisit stays default. | 7 |
| `TestIssue106StateReauthorizationAndMalformedHTTP` | Revoke facet/archive/parent grant and assignment after save; reopen saved state and shared URL. Invalid enums/date/ranges/IDs/types/sort/page/size, duplicates, unknown keys, oversize input reject without broader results or persisting invalid input. | 8,9 |
| `TestIssue106WholeSearchFailureHTTP` | Schema-local candidate/projection/state-storage failures, safe whole unavailable response, retained inputs/retry and no false empty/new partial rows. Domain/audit snapshots unchanged by reads. | 8,9 |
| `TestIssue106DiscoveryAuthorityAndSnapshotRacesHTTP` | Deterministically interleave read with revoke/unassign/archive/convert; coherent total/page/projection for allowed linearization and fresh subsequent request. Session revoked while waiting also checked. | 8,9,11 |
| `TestIssue106PreferenceSaveResetRacesHTTP` | Two same-user tabs save/reset in controlled order; agreed revision/order behavior, stale response cannot resurrect Reset; two users never collide; failed write not acknowledged saved. | 7–9,11 |
| `TestIssue106FieldAPIBoundaries` | Real persisted bearer sessions, production API router; Item list/selection remains single identity and Cost-free even with Cost grant, base-only SKU absent, current revoke applied; no general-search/Lease discovery surface. Existing J context inaccessible sibling 404, visible forbidden action 403; unknown supported-endpoint filter 400. | 1,8,9,11 |
| `TestIssue106PopulatedUpgradePreservesFacts` | Immediately-pre-new-migration populated schema; all old table row/sequence/grant/relationship/history snapshots equal after migration and repeated migration/startup; preference table additive, no synthetic audit/grants. | 10 |
| `TestIssue106DiscoveryPersistedBrowser` | Journeys below with real forms/router, DB assertions, retained browser artifacts. | 7,8,11 |

## Browser journeys

1. Sign in, search shared marker, select types/sort/50, move to page 2, scroll to known result, open destination. Change a still-visible record through another authorized session; Back restores URL/controls/page/scroll (measured tolerance) and shows changed DB label. Archive a row or revoke grant while away; Back shows current eligible membership and never resurrects prior hidden data.
2. Navigate away and return without explicit state; reload; new browser context/new login for same user restores DB state. Different user has defaults. Open explicit shared URL specifying only query: old type/archive/sort filters must not leak into it. Explicit-empty then Reset then revisit prove no resurrection. Repeat collection isolation for all ten routes.
3. Hold first real search response in browser routing after upstream request, complete second real query first, then release first: final URL/controls/results still belong to second intent. Abort actual refresh/network offline: retained prior results, if displayed, explicitly labelled previous; current filter inputs retained, no false zero; Retry reaches real backend and shows correct new results.
4. Revoke current authority/session during failed refresh and on history restoration. Observe DOM mutations from navigation onward to catch forbidden-result reinsertion, not only final screenshot. Remove now-unauthorized data; expired session requires sign-in. A static old visible result before browser learns revocation is not evidence of a server leak; assert at the defined reauthorization/contact boundary.
5. At 320px/coarse-pointer and desktop 200% zoom: actual filter/reset/page/retry controls reachable by keyboard, labels associated, focus sensible, status/error announcements present, no document-level horizontal overflow. Record tested browser/version only; do not claim whole release matrix.

Failure controls manipulate actual isolated dependencies and restore them in cleanup: rename one required table, or use approved schema-local failing view/function where query design requires it; preference write failure via trigger once schema is known. Never close the entire pool for the only fault test: that proves middleware unavailable, not an individual authorized search branch failing. If one SQL statement replaces all branches/enrichment/count, test its real failure paths rather than invent nonexistent component calls.

## Critical interface questions for coordinator/peers

1. **Route inventory:** current Contact/Location collections are Customer-nested; no independent top-level `/contacts` or `/locations` list was found in current router. Confirm exact ten collection destinations and preference keys; do not silently substitute two Customer-scoped sublists for global collection acceptance.
2. **Query/result contract:** canonical keys, fixed tie type order, identifier manifest (internal IDs vs display numbers/SKU), each collection's allowed sort, total/row markup, type multi-select serialization, archived mode, empty query/out-of-range semantics, maximum bounds and safe error status. Need stable semantic markup to identify actual rows without counting sidebar links.
3. **Field/facet manifest:** which parents genuinely support each scalar definition, textual number/date representation, retired-option semantics, and whether Cost/price/Billing numerics are searchable at all. Negative leakage tests apply regardless; positive tests must not invent requirements or search fields.
4. **State:** exact save/reset HTTP contract, saved-query policy, reset scope, invalid/restored-unavailable state handling, preference audit classification and save/Reset ordering. Server persistence must be demonstrated with fresh sessions and router, not merely cookies or unchanged browser context.
5. **Read concurrency:** identify authority/read snapshot linearization and actual transaction/fence. A pooled multi-query count/page with separately loaded principal can mix snapshots. PostgreSQL table-lock wait plus `pg_stat_activity` may expose this, but the barrier must target a real new query; using a mutation-only fence would hang or prove nothing.
6. **Error/history:** define when prior results remain with explicit label and when authority uncertainty requires hiding them. Need actual failure UI and stale-response guard contract, not service-only error assertions.
7. **Migration:** reserve new migration filename and pre-migration ceiling. Current fixture's seeded hook is too old for realistic Lease/conversion/current-grant preservation; filtered latest-base FS is the suitable seam.

## Execution handoff

After production interface handoff, implement the owned tests and resolve failures with source owners. Run targeted real-DB tests under `-race -count=1 -v`, browser suite with `BROWSER_TESTS=1`, then coordinator's required `make lint` and `FREEFSM_TEST_DATABASE_URL=... make test` on frozen source. Log actual matched tests, pass/fail/skip counts, exit codes and durable paths. Schema fixtures are acceptable persisted journeys; fake result services, placeholder browser PASS messages, no-op assertions, zero matched tests and skipped DB/browser tests are not acceptance.
