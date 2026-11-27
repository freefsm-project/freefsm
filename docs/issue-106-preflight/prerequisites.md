# Issue #106 prerequisite preflight

## Scope and verdict

Inspected base `623a8655fe44502db4078da79653e5bb3de67c1d` in this worktree. Initial `git status --short` was empty and HEAD matched that exact base. Read GitHub issues #106, #30, #64 and #73 including all returned comments using `gh issue view --comments --json ...`; also fetched labels. #106 is OPEN; all three prerequisites are CLOSED; all four carry `ready-for-agent`.

**All three prerequisite implementations are present in ancestry and in the actual base source/test tree. No missing-prerequisite blocker was identified.** This is a source/ancestry preflight, not renewed runtime acceptance or proof that every prerequisite criterion remains regression-free.

No implementation, Git mutation, GitHub mutation, generalized harness, other-worktree inspection, or test execution was performed. This report is the only added file. Historical acceptance statements below are attributed to issue comments, not new test results.

## #30 — Evolve Custom Fields without losing history

Comment: https://github.com/freefsm-project/freefsm/issues/30#issuecomment-6139368377

The comment cites local acceptance at `23da4af`, retained-global repairs, and `docs/v1-integration-23-96-round3.md`. Resolved commit is `23da4af7a3f3242d7a30188723449e048c2b2da2` (“docs: record durable round-three integration acceptance”). `git merge-base --is-ancestor <commit> 623a8655fe44502db4078da79653e5bb3de67c1d` exited **0**.

Actual implementation present:

- `internal/authorization/custom_fields.go`: definition writes include retirement/options metadata, type/precision safeguards, historical conversion evidence checks, and destructive-change guards (notably lines 93, 125–161, 177–186).
- `internal/customfields/values.go:50–140`: company-owned plus retained-global definitions; reject submitted retired-field edits (96–98), skip retired defaults (113), suspend retired requiredness (130), and preserve existing values (79–81, 134–140).
- `internal/customfields/values.go:145+`: retained retired-option selections versus forbidden new selections.
- `internal/customfields/cycles.go`, `references.go`: captured pairing/definitions and active-cycle/deletion dependencies, including `conversion_field_snapshot` and stored current shared values.
- `internal/database/migrations/070_field_retirement.up.sql`, `071_field_reconciliation.up.sql`, `072_historical_field_review.up.sql`, `073_retained_global_fields.up.sql` exist.

Executable test sources present:

- `internal/handlers/field_evolution_integration_test.go`: `TestFieldRetirementAndReactivationHTTP`, `TestRetiredOptionsRetainSelectionsButRejectNewSelectionsHTTP`, `TestFieldPairRequiresExactPrecisionAndOrderedOptionsHTTP`, captured-cycle and required-input tests.
- `internal/handlers/field_offline_integration_test.go`: terminal retirement/stale revision API and receipt nondisclosure cases.
- `internal/handlers/field_retained_global_integration_test.go`: populated legacy upgrade, compatible global/owned pairing, concurrent overlapping-key claims, ambiguous-upgrade rollback, omitted captures requiring explicit review.
- `internal/handlers/field_company_scope_integration_test.go`, `field_retained_global_browser_test.go`, `typed_fields_browser_test.go` also exist.

**Relevance/gap for #106:** retirement is a real domain state, not deletion. Search must explicitly join active, applicable definitions and select permitted scalar field types; blindly searching retained JSON would include retired or foreign definitions. Existing global search does not query custom-field values at all (`internal/services/search.go`). That integration is #106 work, not an absent #30 prerequisite. Preserve retained-global ownership semantics and historical values.

## #64 — Preview and perform atomic Customer archive cascades

Comment: https://github.com/freefsm-project/freefsm/issues/64#issuecomment-6476285386

The comment cites `028cbda0d4c7a608caf7fd45488ac607ca3a0d49` and historical full-suite/lint acceptance. That exact commit exists (“fix: wait for shared security startup before Job lookups”) and ancestor check exited **0**. Its title alone is not cascade evidence: history of the actual cascade test file includes `84ec12e` (“show exact Customer cascade grants and primary effects”) and `d3b348a` (“enforce reviewed Customer cascade and archived workflow boundaries”).

Actual implementation present:

- `internal/authorization/customer_archive.go:28–120`: exact active Contacts/Locations/customer-owned Assets; company-owned Assets excluded; current grants; reviewed snapshot incorporates versions.
- Same file, `133–177`: transaction rereads preview, rejects missing grant/blockers/stale snapshot, archives children, clears actual primary defaults, writes per-child audit and Customer audit together.
- `internal/authorization/customer_archive_blockers.go:30–123`: work, operational Estimates excluding conversion-hidden records, Draft/outstanding Invoices, placed company Assets, Lease dependencies and available Credit. Unauthorized blocker identities/names are suppressed and restricted entries collapsed.
- `internal/handlers/customer_children.go:253` and `internal/handlers/customers.go:415`: actual preview/commit adapters invoke these services.
- `internal/database/migrations/103_master_lease_dependencies.up.sql`: Customer/master archive dependency guards; `121_lease_notice_history.up.sql:52–53` supplies current Lease-ended semantics.

Executable test sources present:

- `internal/handlers/customer_cascade_review_test.go:13–149`: exact reviewed scope, new-child conflict, no partial audit, historical-child preservation, injected final-audit rollback, independent child-grant denial.
- `customer_cascade_blockers_test.go`, `customer_cascade_authority_test.go`, `customer_cascade_races_test.go`, `customer_cascade_eligibility_test.go`: nondisclosure, waiting authority loss, dependency/financial reversal commit orders and Lease states.
- `customer_cascade_receipt_test.go`, `customer_cascade_mail_test.go`, `customer_cascade_browser_test.go`: effect/receipt atomicity, retained mail retry/restore, browser review/denial/lost-response cases.
- Commercial writer/adapter/race tests remain in `customer_cascade_commercial_*_test.go`.

**Relevance/gap for #106:** archive and conversion-hidden provenance are separate states. Search must preserve explicit archived visibility and badges without surfacing hidden converted records. Current global search excludes archived records entirely and excludes hidden Estimate/Invoice rows (`internal/services/search.go:55–130`), but offers no authorized archived search mode. #106 must extend discovery without changing cascade/restore semantics. No missing cascade prerequisite identified.

## #73 — Complete Lease workspace and related-record views

Comment: https://github.com/freefsm-project/freefsm/issues/73#issuecomment-6628421484

The comment cites `6225c5769687e61e7fbf8bd9aa29419959a947a2` and historical exact-pin lint/database/browser acceptance. Exact commit exists (“fix: complete Lease coverage history and workspace recovery”) and ancestor check exited **0**. Its actual diff includes Lease discovery, relationships, handlers, templates, coverage and remembered-authority browser tests.

Actual implementation present:

- `internal/authorization/lease_discovery.go:30–147`: page sizes 25/50/100, current principal, independent base/Billing/archive grants, authorized existence/queue counts, relationship search gated by each master permission and archived-view grant, stable `document_number,id` ordering.
- `internal/authorization/lease_workspace.go`: workspace filter validation, SQL predicates and queues.
- `internal/handlers/leases.go:131–185`: user-specific HTTP-only remembered-query cookie, explicit URL precedence, remembered-authority loss redirect/reset and successful-state persistence.
- `internal/authorization/lease_relationships.go`: related-record projections and coverage history, used by the existing workspace.
- `internal/authorization/lease_lifecycle.go:11–77`: independent Archive/Restore authority, Draft/Terminated eligibility, active Customer required for Draft restore, repeat no-op, dependency seam and audit in the transaction.
- `internal/authorization/lease_dependencies.go`: master locks and shared SQL dependency writer; `internal/templates/lease_index.templ`, `lease_relationships.templ` provide actual browser surfaces.

Executable test sources inspected/present:

- `internal/handlers/lease_workspace_test.go:19–145`: real HTTP fixture searches Number/title/Customer/premises/address/Asset Number case-insensitively; malformed filter 400; Billing-only queues denied to base-only reader; all three page sizes; persisted browser subtest; bounded archived references and pending notices.
- `internal/handlers/lease_workspace_recovery_test.go`: `TestLeaseWorkspaceRememberedAuthorityBrowser`, `TestLeaseWorkspaceEmptyStatesBrowser`.
- `internal/handlers/lease_workspace_coverage_test.go`: `TestLeaseWorkspaceAssetCoveragePagesHTTP`.
- `internal/handlers/lease_workspace_archive_test.go`: `TestLeaseWorkspaceTerminatedArchiveBannerHTTP`.
- `internal/handlers/lease_archive_writers_test.go`, `lease_lifecycle_races_test.go`, and `testdata/browser/lease-workspace*.cjs` remain present.

**Relevance/gap for #106:** this is a working specialized search/state foundation. Global search does not call it or include Lease results. `LeaseList` explicitly rejects API-channel callers (`lease_discovery.go:53`); do not assume it is an existing API search contract. Retain authorized queue/count/relationship boundaries while integrating. Existing remembered Lease filters are not evidence of a unified Back/page/scroll contract across all lists.

## Evidenced #106 integration gaps

1. `internal/handlers/search.go:33` hardcodes **10 results per type** and its result model contains only Customers, Jobs, Projects, Invoices and Estimates. Contacts, Locations, Assets, Leases and Items are absent.
2. `internal/services/search.go:55–130` uses per-type substring predicates and limits without explicit deterministic ordering or exact-identifier/prefix/elsewhere ranking. No combined type filter/pagination model exists in that handler.
3. That service does not search custom-field values or feed the specialized authorized relationship matching already in `internal/authorization/customer_search.go:32–39`. The latter labels matching active Contact/Location relationships and gates them independently; reuse its semantics, not the global service's narrower Customer predicate.
4. Secondary Customer/status lookups discard errors (`internal/services/search.go:161,167,348,354`). Whole-query unavailable behavior must cover those failures too.
5. Global handler only reads `q`, redirects empty query to `/`, and emits HTTP 500 on error (`internal/handlers/search.go:20–36`). It does not implement retained filters/results labelled as prior query, Reset, shareable sort/page/type state, or Back/scroll restoration.
6. Item/Pricebook deduplication and independently visible Item facets still require implementation/verification within #106; no claim about existing Item-domain correctness is made by this prerequisite-only review.

## Blockers and verification boundary

- **Prerequisite source/ancestry gate: clear.**
- **Runtime gate: not performed in this read-only preflight.** No tests are represented as newly passed; historical comment totals were not independently rerun or authenticated.
- #106 still requires its own real database/browser/API denial, failure and concurrency tests, `make lint`, and `FREEFSM_TEST_DATABASE_URL=<disposable PostgreSQL> make test`. Makefile lines 78–82 define these as `go vet ./...` and `go test -v -race ./...`; browser subtests require `BROWSER_TESTS=1` (e.g. `lease_workspace_test.go:98–110`). An unset database URL or browser skips do not establish acceptance.
- The listed integration gaps are expected #106 scope, not reasons to reopen a prerequisite solely because its GitHub issue is closed.
