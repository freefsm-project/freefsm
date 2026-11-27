# Dedicated collection / atomic state security review

## Final integration-repair review — flash blocker closed

**No remaining actionable findings in this collection/state security review.** This supersedes the open C adapter blocker below. It is scoped regression clearance, not a claim that the previously failing full suite has subsequently passed.

Reviewed the current adapter/template changes against the previously inspected implementation:

- `handlers/list_state.go:51–64` clears inherited unvalidated flash context before URL parsing and sets rendering context only after successful `liststate.Parse`. Canonical query replacement remains separate; validated flash is never reinserted into query values.
- `templates/list_state.templ:84` and its generated Go exclude `flash` as well as `reset` from recovery inputs. Rejected transport therefore cannot reappear through raw recovery values; parser errors also retain the cleared display context.
- The independent ten-collection HTTP test now reaches all assertions: escaped notification display without executable markup, flash-only restoration, unchanged GET preference revision/value, explicit empty search overriding remembered state, alias preservation/conflict rejection, malformed transport rejection and POST persistence without flash. Oversized rejected transport is asserted absent from the entire response; repeated-flash display/input suppression is additionally established by the unconditional context clear and template exclusion.

### Independent execution and provenance

```sh
CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS=-p=1 \
go test -race ./internal/liststate ./internal/handlers \
  -run '^(TestFlashTransportDoesNotOverrideOrPersistQuery|TestCollectionFlashTransportRestoresSavedStateHTTP)$' \
  -count=1 -v -timeout=5m
```

**Exit 0: 2 root tests + 10 subtests passed; zero failures/skips/race reports.**

Durable command, log, exit, freeze verification and before/after source hashes:

`/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/flash-final-independent-BdCsQ4dD/`

Scoped source manifests are identical; SHA-256 of either manifest:

`3752ae998b68e7eff92e0c8385246dc3d2ab73fe4ebe49da016c5c0f466ad4fb`

All entries in `results/ui-full-failures-freeze.sha256` verified successfully (exit 0). Independently counted C's retained `logs/ui-full-failures-final-04.log`: **7 root + 19 subtest passes, zero failures/skips**, matching its exit 0. That broader C run was inspected, not independently rerun here. The historical failed full-suite run remains a failed run; final whole-suite acceptance belongs to the integration owner.

## Post-full-suite B repair review — integration still blocked

Independently inspected the landed parser, SaveSafety fixture/assertions and native-upgrade fixture repair. **No actionable defect found in these B repairs. The C flash adapter remains incomplete; neither this review nor B's passing subset establishes full-suite acceptance.**

### Accepted B repairs

- `internal/liststate/query.go:34–74`: checks the complete input bound, clones input, validates a singleton UTF-8 flash of at most 500 characters with no control characters, then removes it **before** determining explicit query state or handling Reset. Flash-only input cannot override saved filters. Explicit query state alongside flash remains explicit. Other unknown parameters and malformed typed values still reject. `Canonical` and `Persistent` also remove flash (lines 101–120). The parser does not mark HTML trusted.
- Existing aliases remain route-owned: `collectionQuery` accepts `q`/`search`, rejects conflicting nonempty aliases, and retains actual search values. The new flash HTTP test covers whole-state override, aliases and non-persistence; those later assertions are not yet execution-qualified because its earlier display assertion currently fails.
- `save_safety_integration_test.go:80–131` now supplies each route's actual accepted sort, retains the real renamed-table fault, requires 503/Retry, checks retained search/sort/page and route, rejects raw SQL/false-empty messaging, restores the table and **executes the derived Retry request successfully**. Parsing the rendered Retry link/form instead of requiring an encoded substring is a stronger semantic assertion, not failure suppression. The helper at lines 136–203 matches the current recovery forms' named input structure.
- `lease_billing_upgrade_test.go:21–29` checks `list_states` row count and fatally asserts zero before dropping only that fixture table, without CASCADE or `IF EXISTS`. This reconciles schema with the fixture's later removal of migration markers from 113 onward. `historyEqual` uses `t.Fatalf`, so a populated table stops before DROP. Production migration 126 remains a strict `CREATE TABLE`; the fixture retains its original domain/grant/history/sequence comparisons and second migration execution. No production migration error was swallowed or relaxed.

### Open C blocker — preserve validated flash separately from query state

Inspected `handlers/list_state.go:49–90` still parses/removes flash and rewrites `r.URL.RawQuery` to canonical query state without explicitly carrying the validated message to rendering. The retained ten-collection integration run reaches correct saved-state/canonical assertions, then fails because the escaped flash is absent.

The adapter must preserve **only successfully validated** flash as transient rendering context, independently of canonical/persisted/query values. It must also remove rejected flash from both any inherited flash-display context and error-page input values: parse-error branches currently pass `raw` to `listStateFailure`, and `ListStateFailure` renders every supplied key except Reset. Thus an oversized/repeated rejected flash can currently be echoed in recovery inputs even though parsing rejects it.

Do not solve display by putting flash back into `Query.Values`, `Content-Location`, `HX-Push-Url`, state POST payloads or the durable row. Do not let `?flash=...` become an explicit-filter override. Keep malformed percent-encoding rejection and HTML escaping. The completed HTTP test must get past display and execute its invalid-flash, alias, explicit-override and save/no-flash assertions for all ten collections.

### Evidence reviewed, not rerun

Under `/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/`:

- `commands/list-state-full-regressions-2.txt`, `logs/list-state-full-regressions-2.log`, `results/list-state-full-regressions-2.exit`: independently counted **exit 0, 22 root + 74 subtest passes, zero failures/skips**. This focused selection excludes `TestCollectionFlashTransportRestoresSavedStateHTTP`.
- `logs/list-state-flash-integration-1.log`, matching command/exit: **exit 1, parser root passed; HTTP root and all ten collection subtests failed** at the missing escaped-flash assertion. Subsequent assertions did not execute in those subtests.

No tests were rerun by this repair review. The prior full-suite failure, C flash blocker, Lease blocked-state/explicit-Reset reconciliation and other integration work remain distinct from accepted B repair semantics. Earlier scoped findings/resolutions below retain their historical provenance.

## Final scoped findings disposition

**All findings raised by this collection/state review are resolved in the reviewed A/B source. No remaining actionable finding identified in this scope.** C's continuing UI changes and final whole-ticket integration acceptance are not covered by this disposition.

### Bounded collections: resolved

Actual main-handler paths now call:

- Jobs: `JobOperationalPage` (`handlers/jobs.go:87`).
- Estimates/Invoices: `DocumentListPage` (`handlers/estimates.go:96`, `handlers/invoices.go:82`).
- Contacts/Locations: `CustomerChildrenPage` on the main-list branch (`handlers/customer_children.go:109–114`); the related/picker branches retain their separate selectors.

The three new domain-specific implementations in `authorization/collection_pages_{jobs,documents,children}.go` validate current authority and selected references, count and select under `WithinSnapshotPermission`. This acquires the authority fence before the Repeatable Read snapshot. Counts and bounded rows therefore use one coherent snapshot; all filter/tenant/assignment/archive predicates precede SQL `LIMIT/OFFSET`. Materialized page-ID CTEs bound row projection/enrichment; deterministic ordering includes identity ties. Out-of-range pages retain their authorized total.

Inspected specialized projections preserve Job assigned Pricing/Customer redaction, document effective Invoice status/validity/independent pricing and settlement facets, and child Customer-reference redaction/mutation eligibility. Existing unbounded report/export methods are no longer used to implement these five main pages.

Independent tests capture and EXPLAIN the exact production statements: each of the five page projections has a real SQL Limit and returns 25 projected rows. This demonstrates bounded application row materialization/enrichment, not constant-time database scanning or a general latency guarantee.

### Unicode versus export compatibility: resolved

`document_list_exports.go:48–78` now separates `NormalizeDocumentCollectionFilter` (500 Unicode characters) from legacy `NormalizeDocumentListFilter` (1,000 bytes). `AuthorizeDocumentListFilter` and `DocumentListSnapshot` carry the export/collection choice through their normalization, so downstream revalidation does not reintroduce the earlier incompatible limit.

Exactly one `parseDocumentCollectionFilter` exists, at `handlers/collection_list_specs.go:117`; main Estimate/Invoice handlers use it. Report/export parsing and `exports.Documents` retain the historical export normalizer.

Independent HTTP execution verifies:

- Collection reads, saves and restores accept 334/499/500 multibyte characters via `q` and `search`; 501 characters and invalid UTF-8 reject without state revision changes.
- Estimate/Invoice export preview, queued generation, worker processing and CSV download accept 501 and 1,000 ASCII characters and include the expected record.
- Collections reject those over-500-character ASCII filters; exports reject 1,001 ASCII bytes and 334 copies of `界` (1,002 bytes).

### Independent final scoped execution and hashes

After sourcing the supplied durable environment:

```sh
CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS=-p=1 \
go test -race ./internal/authorization ./internal/handlers \
  -run '^(TestCollectionPages.*|TestCollectionDocumentUnicodeQueryBoundaryHTTP|TestCollectionDocumentExportRetainsByteLimitHTTP|TestDiscoveryTenCollectionsPageSizesHTTP)$' \
  -count=1 -v -timeout=10m
```

**Exit 0: 6 top-level tests and 14 subtests passed; zero failures/skips/race reports.** Includes actual all-ten-main-route pagination/state journeys and domain authority/projection/SQL-bound checks, not only helper tests.

Durable exact command, full SQL plans/test log, numeric exit and source manifests:

`/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/collection-final-review-7XL6AAkr/`

`source-before.sha256` and `source-after.sha256` compare identically. SHA-256 of either manifest:

`c1f756725387bbadd7d89cb0b7d9bdbb5d1ba7521ac33fbf32ef971b5a79e93a`

The manifest covers new paged selectors/tests, document normalization, state validators/storage/HTTP adapter, the five main-list call sites, collection parser/tests, legacy report parser and export adapter. This is scoped A/B provenance while C remains active, not a whole-worktree freeze.

Earlier findings and failed attempts below remain as historical evidence. In particular, this run did not rerun the earlier failed Billing HTTP fixture; its final reconciliation remains with acceptance ownership rather than being silently reclassified here.

## Latest correction review

**Collection Unicode defect resolved; explicit Lease base-facet check accepted. One export compatibility issue remains, aside from the separately in-progress bounded selectors.**

- `list_state_query.go:46–50` now rejects explicit `facet=base` without current `lease.view`, independently of retained Billing authority. This validator is used on the dedicated read and in the existing same-transaction preference CAS callback, so base-grant loss cannot be hidden by the Billing-only default at commit. Omitted facet still supports Billing-only users; explicit Billing still requires Billing View.
- `document_list_exports.go:53–54,62` now checks UTF-8 and 500 runes and wraps invalid filters/sorts with `ErrConflict`. Independently executed HTTP tests accept 334/499/500 multibyte characters through both aliases, save and restore, and reject 501 characters/invalid UTF-8 without revision changes. This resolves finding 2 below for collections.
- Explicit-base tests cover shared URL/restoration/POST denial after base revocation, direct commit-validator denial without state changes, and continued omitted/explicit Billing access. The lock-observed generic View/archive/Billing fence test also passed. The explicit-base test itself is sequential, not a new concurrently queued explicit-base race; its atomicity follows the shared, separately exercised transaction seam.

### Remaining P2: shared normalizer narrows legacy export inputs

`authorization/document_list_exports.go:53` replaces the old **1,000-byte** search limit with **500 characters** for every caller. `exports/documents.go:26–39,47–62` still uses this same normalizer/authorization/snapshot chain. A filter such as `{"search":"<600 ASCII a characters>"}` was valid for Estimate/Invoice exports at the approved base and is now rejected. The collection Unicode tests do not cover this compatibility case.

The contract limits this slice to the main collections and explicitly preserves report/export contracts. The correction therefore must not silently narrow old accepted export filters while fixing collection Unicode.

**Smallest domain-correct correction:** keep the 500-rune constraint at the dedicated collection/state boundary, and preserve the historical export acceptance set in the shared/export normalization path. If a shared normalizer accepts the union of old-valid text and new collection-valid Unicode, ensure every dedicated entry point still enforces its 500-rune limit; otherwise introduce purpose-specific search-limit validation without duplicating domain authorization/projection. Carry the compatibility policy through `AuthorizeDocumentListFilter` and `DocumentListSnapshot`, not merely `Documents.filter`. Add an export regression for 600 and 1,000 ASCII characters alongside collection 501-character rejection and 500-character Unicode success.

### Independent focused results

After sourcing the supplied durable environment:

```sh
CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS=-p=1 \
go test -race ./internal/authorization ./internal/handlers \
  -run '^(TestListStateDocumentNormalizerUnicodeContract|TestDocumentListNormalizers|TestCollectionLeaseExplicitBaseRequiresCurrentBaseViewHTTP|TestCollectionDocumentUnicodeQueryBoundaryHTTP|TestListStateAuthorizedSaveFencesViewAndSensitiveFacet)$' \
  -count=1 -v -timeout=5m
```

**Exit 0: 5 top-level tests and 5 subtests passed, no skips/failures/race reports.** Durable command/log/exit and identical scoped before/after source manifests:

`/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/collection-corrections-review-xvc9jpRS/`

Legacy export compatibility finding is source-proven from the changed predicate and existing export call chain, not a newly executed export regression. No large suite or unfinished bounded-selector review was performed. Earlier failed Billing fixture evidence below remains historical and is not converted into a pass by this focused run.

## Earlier review and execution record

Reviewed current B source and the current C callback wiring read-only. This is an intermediate review while other slices remain active, not final integration approval.

## Atomic commit verdict

The previously demonstrated read→revoke→preference-write gap is closed in the inspected path:

- `handlers/list_state.go:164–171` calls `SaveAuthorizedListState` with a transaction-bound discovery or dedicated-collection validator.
- `authorization/list_state.go:95–137` invokes that validator with the fresh principal **inside the same `WithinActor` transaction and exclusive authority fence** as the CAS insert/update.
- `list_state_query.go:45–76` checks Lease Billing/base/archive/relationship rules and Job/document scopes/selected references using existing domain predicates. Asset Customer/vocabulary and Project status references are checked on the supplied transaction (lines 88–121).
- `discovery.go:109–148` checks current selected type/archive permissions and independent Lease Billing/relationship target authority. It does not use saved grants or the navigation principal.
- Identity/channel/company/user binding and original-revision CAS remain intact. Reset remains intrinsic and retains tombstones after loss of collection authority. CSRF remains the HTTP adapter's responsibility.

Independent execution of the original HTTP race now **passes**. Source inspection and the passed View/archive/Billing transaction tests support the corrected seam; the failed Billing HTTP fixture below prevents claiming all sensitive HTTP races qualified.

## Actionable findings

### 1. Dedicated pagination still materializes entire collections

**Locations:** `handlers/jobs.go:87–96`, `handlers/estimates.go:97–105`, `handlers/invoices.go:83–93`, and `handlers/customer_children.go:114,178–216`.

Job/Estimate/Invoice pages call full report selectors and slice their complete result arrays in Go. Their SQL has no page bound (`authorization/job_operational_exports.go:134–158`, `document_list_exports.go:151–169`). Contacts/Locations similarly load all authorized child records (`authorization/customer_children.go:176–196`) and then filter/sort/slice in the handler.

Consequently `per_page=25`, even with an out-of-range page, reads and allocates all matching documents/jobs or the entire authorized child collection. Job reads also fetch assignment arrays and page-independent pricing enrichment. Every preference POST repeats this full read before its atomic save. Cost grows with total records, not selected page size, and large collections can exhaust request memory or time out despite pagination.

**Provenance:** this broad materialization predates B; B preserves it rather than introducing it. It is an unresolved completion issue against the requested bounded-query review, not a newly introduced disclosure or a measured performance regression. Authorization does precede the Go slicing.

**Correction:** introduce domain-owned paged collection selectors using the same authorized filter/projection and deterministic order, with count and bounded page in a coherent snapshot. Retain unpaginated selectors for reports/exports. Do not truncate before authority/filtering or replace specialized pricing/effective-status projections with generic discovery rows.

### 2. Document lists reject valid 500-character Unicode queries downstream

**Locations:** `handlers/collection_list_specs.go:34–37`, `authorization/list_state_query.go:73–76`, `authorization/document_list_exports.go:47–53`.

The new route contract accepts `q`/`search` up to **500 Unicode characters**, but the reused document normalizer rejects `len(f.Search) > 1000` **bytes**. For example, an authorized `/invoices?search=` followed by 334 copies of `界` is valid under the new parser (334 characters, 1,002 UTF-8 bytes, within the encoded-state bound), then fails domain validation. `collectionQuery` passes that generic error to `authorizationError`, which reports GET unavailability (503), rather than returning a valid search result. Estimates have the same mismatch; preference saving cannot succeed either.

**Correction:** reconcile the dedicated document adapter and shared normalizer with the 500-character contract, preserving any separately documented export/report limits. Use explicit UTF-8/rune validation and domain validation errors, not a byte-length limit masquerading as dependency unavailability. Add a real HTTP case using multi-byte text near the accepted limit.

This is a new integration mismatch with an inherited normalizer, not a flaw in the generic parser's rune counting. Static reproducer derived directly from the inspected predicates; no new test file was written.

## Independent execution and limitation

Supplied environment:

`/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/env.sh`

Executed:

```sh
CGO_ENABLED=1 GOMAXPROCS=2 GOFLAGS=-p=1 \
go test -race ./internal/authorization ./internal/handlers \
  -run '^(TestListStateAuthorizedSaveFencesViewAndSensitiveFacet|TestDiscoveryGrantLossBetweenReadAndPreferenceCommitHTTP|TestDiscoverySensitivePreferenceCommitOrdersHTTP|TestMainCollectionStateHTTP|TestMainCollectionPreferenceReauthorizesHTTP|TestCollectionMainRoutesStrictHTTP|TestCollectionProjectPaginationCurrentAuthorityHTTP)$' \
  -count=1 -v -timeout=10m
```

**Exit 1:** 6 top-level tests passed, 1 failed; 36 subtests passed, 2 failed; zero skips. No race report appeared.

- Passed: original commit-time race; transaction fence View/archive/Billing cases; archive/relation HTTP races in both queued orders; ten-list state/Reset/CAS; strict main routes; collection denials; Project authorized deterministic pagination.
- Failed before race execution: both Billing variants of `TestDiscoverySensitivePreferenceCommitOrdersHTTP`, at `discovery_sensitive_persistence_race_test.go:43`, inserting `lease_billing_period_failures`: foreign-key violation `lease_billing_period_failures_lease_id_fkey` (SQLSTATE 23503). This is a fixture/setup failure, not proof of a remaining production commit-time race. Acceptance owner must repair the fixture and rerun; these two cases are **not passes**.

Durable logs, exact command, exit and scoped before/after hashes:

`/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/collection-security-review-AGNJGmhq/`

The scoped manifests compare identically. They cover B source, discovery validators, collection handlers/specs and the shared state adapter; they do not freeze the whole concurrent worktree.

## Preserved semantics / remaining boundary

The inspected dedicated paths retain their domain projections: effective Invoice status and separately authorized document financial display; Job pricing projection; Customer relationship matching; Contact/Location Customer identity redaction; Item Pricebook/Cost redaction; Lease Billing-only Number projection. Asset/Project SQL pagination now has ID tie-breakers. No observed B projection regression or new pre-pagination authorization bypass was found.

Current route specs intentionally preserve existing archive modes rather than adding archive browsing to previously active-only dedicated lists. Combined search supplies explicit archive modes; this review does not infer new dedicated archive UI scope. Final forms/router/history behavior and whole-ticket scope acceptance remain for the frozen integration review.

No production edits, nested agents, Git/GitHub mutations or full suite were performed. The ticket runtime remains owned by the coordinator.
