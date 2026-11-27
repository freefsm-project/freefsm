# Independent review — #106 slice A discovery engine

## Verdict

**Current verdict: original P2 resolved; no remaining findings identified in the correction review.** The unchanged independent reproduction and all ten discovery tests passed in a fresh race-enabled database run. See the correction review and exact hashes below.

**Original review: changes requested for one reproduced P2 correctness finding.** The seven existing discovery tests passed independently with the real disposable PostgreSQL database and race detector. An additional independent test failed after exercising a valid Lease Billing revision. The correction review below records its subsequent resolution; the original failure and evidence remain preserved.

Scope: `internal/authorization/discovery.go`, `discovery_sql.go`, and `discovery_integration_test.go`. Read `AGENTS.md`, GitHub #106 and normative #18 search/permission requirements, `implementation-contract.md`, and `contract-review.md`; checked existing grant, parent-access, custom-field, Lease workspace and transaction implementations. C/D integration is still in progress and is not evaluated as completed here. No nested agents, production edits, Git/GitHub mutations or full-suite execution.

## Finding

### P2 — Current Lease Billing terms disappear from search after an effective revision

**Location:** `internal/authorization/discovery_sql.go:245–250`.

The Billing term branch always reads `lease_draft_asset_terms`. For finalized agreements these are the original draft values, not the current effective rent/tax terms. A valid `ReviseLeaseBilling` stores its changes in `lease_billing_revisions` (`internal/authorization/lease_billing_revision.go:192–232`); the owning billing path resolves applicable terms through `currentCoverageBillingTerms` (`internal/authorization/lease_billing_period.go:195`). Discovery never reads that effective source.

This violates #106 AC2 / DISC-03's authorized active scalar-number search. It is not a request to index arbitrary historical ledger entries: the missed value is an effective, current Billing scalar on the Lease itself. The independent Billing permission check is present, but an authorized Billing reader cannot find the agreement using its current rent.

**Executed reproduction:**

1. Create/finalize an active Lease with original rent `123.45` using the existing real-database fixture.
2. Call production `ReviseLeaseBilling` with an effective date of the company's current day, rent `876.54`, and unchanged tax `5`. This succeeds.
3. Create a reader with only `lease.billing.view: All`.
4. Call production `Discover` with `Types: ["lease"]`, search `876.54`.
5. Observe total **0**; searching the original draft rent `123.45` still returns total **1**, labelled `Lease Billing terms match`.

The test fails with:

```text
current revised rent 876.54: total=0 rows=[]
original draft rent 123.45: total=1 rows=[{Type:lease ... Match:Lease Billing terms match ... Status:active}]
authorized current Lease rent is not searchable after a valid effective Billing revision
```

**Correction:** use the domain's applicable current Billing terms for finalized Leases (including effective revisions/coverage terms), while retaining draft terms for Drafts. Keep the projection Billing-gated and within the same candidate/count/page statement consistency boundary. Add a regression using an effective revision; distinguish current terms from explicitly retained original/history evidence rather than treating draft storage as the current agreement.

## Independent verification

Environment sourced:

`/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/env.sh`

It explicitly sets `FREEFSM_TEST_DATABASE_URL=postgres://issue106_app@127.0.0.1:45883/freefsm_issue106?sslmode=disable`.

### Existing focused tests — PASS

```sh
go test -count=1 -race ./internal/authorization -run '^TestDiscovery' -v
```

Exit **0**, seven tests passed, zero skips/failures, package runtime 16.099s.

Log:
`/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/discovery-independent-review-20261127T032146Z-578915.log`

### Independent reproduction — FAIL as expected

```sh
go test -overlay "$E/discovery-review-overlay-578915.json" \
  -count=1 -race ./internal/authorization \
  -run '^TestIndependentDiscoveryCurrentLeaseRent$' -v
```

Exit **1**, one failing test, package runtime 2.060s. Setup/revision succeeded; failure is the missing search result.

Durable evidence (all under the supplied evidence root):

- Test: `discovery-review-repro-578915_test.go`
- Go overlay mapping: `discovery-review-overlay-578915.json`
- Log: `discovery-independent-repro-20261127T032242Z-580122.log`

The overlay adds a test to Go's build view without editing production or repository test files.

## Other reviewed boundaries

No additional concrete defect identified in the following inspected boundaries; this is not a claim of exhaustive runtime coverage:

- **A/J/C grants:** `DiscoveryAllowed` delegates collection entitlement to `CollectionAllowed`; Context-only masters are excluded, and Job/Estimate/Invoice Assigned candidates require a currently assigned active Job. This agrees with the existing `parent_access.go`/`navigation_reads.go` active-Job semantics. No Role-name shortcut is used.
- **Independent facets before rank/count:** Item base is one candidate, SKU/price/tax require Pricebook and unit cost requires Cost; Customer billing addresses require financial View; Job billing type and document tax use row-specific Pricing authority. Lease base versus Billing terms are independently gated. Tests directly cover Item hidden facet noninterference and base/Billing separation; Assigned Pricing combinations are inspected, not separately reproduced.
- **Hidden records:** company and archive predicates precede matching; Item retained-global rows preserve existing scope semantics. Conversion-hidden Estimates/Invoices never enter candidates. Explicit unauthorized types/archive modes reject. Tests mutate hidden Item facets/foreign-company rows and check unchanged visible results/counts; hidden conversion IDs/Numbers are tested across all archive states.
- **Custom fields:** all seven supported parent types from `customfields/catalog.go` are included; Contacts/Locations/Items have no supported custom-field parent contract. Definition joins require matching parent, company applicability (including retained globals), nonretirement, and allowed scalar kind. Single/multi options match stored labels that belong to the definition; retained retired options remain searchable because only definition retirement is excluded. Checkbox and unknown/non-scalar values are excluded. Existing tests exercise six scalar kinds and retirement on Customer; retained-global/foreign-definition/multi-parent applicability is source-inspected, not an independent runtime matrix.
- **Built-ins:** reviewed the explicit field manifest against record schemas and active presentation mappings. For example Project date controls map to `planned_start_date`/`planned_end_date`, not legacy `start_time`/`end_time`. The Lease Billing current-source omission above is the identified manifest/semantic gap.
- **Literal Unicode matching/ranking:** bound `strpos`/`left` avoids SQL LIKE wildcard behavior; an installed Unicode-capable collation is selected explicitly. Exact internal/public identity (and authorized SKU) precedes individual-term prefixes and elsewhere matches. Type/ID ties and global paging are deterministic. Tests cover `%_`, accented case conversion, exact/prefix ordering, and 25/50/100 tied pages. Full Unicode case-fold equivalence beyond the chosen PostgreSQL lowercase semantics was not qualified.
- **Lease filters:** validation and SQL predicates reuse `LeaseWorkspaceQuery`; base-only Billing filters are rejected; relation IDs additionally require independently visible same-company target records. Current tests exercise lifecycle/relationship/ending/placement/monthly combinations and malformed/denied filters. Specialized workspace queue counts are outside the result shape of this slice.
- **Snapshot/current authority:** `WithinActor` obtains transaction advisory lock `190019` before current principal loading and rechecks principal/session before commit (`authorization.go:224–277`). Candidates, terms, match/rank, existence, total, page and result JSON share one SQL statement snapshot. Relation-filter target checks occur earlier under the same domain fence. The waiting-reader revocation test observes an actual blocked backend and passes.
- **Whole-query failure:** `Discover` returns an empty page on every error; no successful branch survives an unavailable branch. The real-DB table-rename failure test passes. API actors are rejected before discovery, consistent with this browser-only boundary.

## Review identity and limits

The reviewed files were untracked work-in-progress files. Recorded SHA-256:

```text
89828d465bebba88d3a894d875d03c2c9b09aafd44c6dada1b5b7e7f3c92635f  discovery.go
d7503902196f64d35e7c0b05f79ba99c9acbebf1d9c302e973c48fab82c465a1  discovery_sql.go
195e36c988ffa1c2a84fd17cb6f154fbb0818deb6dbe893c9c7ca420a3c934f2  discovery_integration_test.go
```

No browser/full API integration qualification, `make lint`, or full `make test` was run by this reviewer. Those ticket-level gates remain separate; passing the seven focused tests does not override the reproduced finding.

## Correction review — current Billing allocation source

Independently read the correction and new tests, not just the author's summary. The original reproduction source and overlay were reused without edits.

### Semantic comparison

The new `internal/authorization/discovery_lease.go:18–65` embeds a recursive allocation replay into the existing candidate SQL. Compared it with `lease_coverage.go:386–464` (`replayLeaseBillingAllocations`), the path behind `currentCoverageBillingTerms`, and the latest-event view in migration 120:

- Finalized initial membership comes from selected `lease_finalizations.terms`; Drafts retain `lease_draft_asset_terms`.
- Billing revisions are ordered by effective date and revision ID, before coverage events on the same date. A revision only updates an allocation that already exists, matching `applyThrough`.
- Coverage uses `lease_coverage_current` (latest revision per stable event ID), excludes withdrawn events, and orders current payloads by effective date/event ID.
- Add creates fresh rent/tax/initial terms, including an optional one-time initial charge; re-adding an Asset does not inherit its former allocation's revisions.
- Replace carries the outgoing allocation's current rent/tax, changes Asset identity and zeroes initial charge; Remove removes membership.
- Effective dates after the company date are excluded. Same-day coverage participates, matching allocation replay's `coverage`-mode membership timing.

**Important equivalence boundary:** this is equivalent to the owning replay's *active allocation state* for valid domain histories, not the entire return value of `currentCoverageBillingTerms` for every charge kind. Monthly entitlement intentionally excludes same-day coverage; `coverage` return values select separate one-time charge allocations; termination can suppress amount owed. Those charge-entitlement rules should not erase searchable retained agreement fields. The correction documents this distinction explicitly (`discovery_lease.go:3–17`); it does not claim to calculate an Invoice.

### Authorization and consistency

`discovery_sql.go:245–247` only adds the entire Billing expression with independent `lease.billing.view: All`. The recursive read does not execute an extra post-page enrichment query: finalization, current coverage, revisions, matching, rank, total and page remain in the original single statement snapshot under `WithinActor`. No new permissions, role grants, writes or migrations were introduced.

The new tests exercise valid production commands for effective/future rent and tax revisions, base-only denial, Add → revision → Replace → Remove → re-add, future coverage revised to become effective today, and Draft scalar terms. Withdrawal is verified by source comparison to the latest-event view; the new tests do not independently exercise withdrawal.

### Independent correction rerun — PASS

Sourced the same durable `env.sh` with explicit disposable PostgreSQL URL, then ran:

```sh
go test -overlay "$E/discovery-review-overlay-578915.json" \
  -count=1 -race ./internal/authorization \
  -run '^Test(Discovery|IndependentDiscoveryCurrentLeaseRent)' \
  -v -timeout=15m
```

Exit **0**, **11 passed, 0 failed, 0 skipped**, package runtime **22.299s**. The unchanged independent reproduction now reports current `876.54` total **1**, obsolete original `123.45` total **0**.

Unique reviewer log:
`/home/adam/.cache/freefsm-issue106-evidence-20261127T030854Z-719520/discovery-independent-correction-20261127T032927Z-590145.log`

**Resolution:** the original P2 is fixed at the hashes below. No remaining actionable finding identified in this correction. Full-suite/browser/API acceptance and other slices remain outside this review.

Hashes checked before and after this rerun:

```text
89828d465bebba88d3a894d875d03c2c9b09aafd44c6dada1b5b7e7f3c92635f  discovery.go
195e36c988ffa1c2a84fd17cb6f154fbb0818deb6dbe893c9c7ca420a3c934f2  discovery_integration_test.go
8ad038950c0d73297c3cbd3a9d42067ad8dc3349832c72a31fdc28b3e6a7f546  discovery_lease.go
e2698c37d35c428fe3f657f419a6990deaaa4143bd09530d93212cc6a03fd50e  discovery_lease_integration_test.go
e35300f7ec34dcc566ff4a43e4685c169699dedae5189c35c3ed185d6cf7a0c2  discovery_sql.go
```

Unmodified independent evidence inputs:

```text
f45f578b2a9dac606145af12d258d729743ba304c0abbbe711cb89f71265385c  discovery-review-repro-578915_test.go
55b7f9ef7fdc42179437af1f09fd0ab362750c9ffc2e964a76186d747dd4cabb  discovery-review-overlay-578915.json
```
