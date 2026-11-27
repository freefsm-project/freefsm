# Ticket #101 — acceptance reconciliation

Baseline: `e703bc55a0ffbfe890b8f20fb37f4afae9c886a5`, branch `implement/v1-101`. **Completed; all eleven AC independently accepted; coordinator staging and runtime shutdown completed.** Reviewer `ses_de68e5d50ffe5sJdg3eEv0Fuak` approved after actual final-artifact review and current 2,144-file manifest match. The coordinator will restage this final documentation update and verify indexed source against final frozen inputs. Completed results and exact logs: [evidence](v1-ticket-101-evidence.md). Historical design and ownership: [plan](v1-ticket-101-plan.md).

## 2026-11-26 addendum — current evidence and authorization status

Per coordinator restart handoff, `/tmp/opencode/issue101-evidence-20261125` vanished, including the linked raw artifacts, main PostgreSQL data and logs. Repository changes and `/home/adam/.cache/freefsm-issue101-scratch-20261125` survived; no recovery of missing evidence or the main database is claimed. All earlier execution/review results and “retained” evidence references below remain historical reported records, not freshly reverified results or presently available raw proof. Their failures, interruptions and limitations remain applicable.

The user-authorized narrow Dispatch production contrast fix, final-source qualification and independent acceptance are complete. Fresh durable evidence under `/home/adam/.cache/freefsm-issue101-evidence-20261126` is authoritative; it does not restore old artifact provenance. Historical blockers and pending gates below are superseded.

## Latest completed execution reconciliation — accepted

Authority: durable-root `f-final-qualification-handoff.md`, `final-qualification-20261126T171148Z/summary.json` and accompanying actual artifacts; external `acceptance-review.md` records reviewer provenance.

| Final gate | Latest accepted result |
| --- | --- |
| Repository race suite | Exit 0; 1,839 top-level + 5,030 subtest passes, zero failures/race reports; 9 expected top-level skips; all 69 expected package results |
| Exact race exclusion | Only `TestBackupBrowserNginxScale`, absent from run/pass/skip counts; mandatory same-source non-race companion passed |
| Full-1-GiB companion | Exit 0; 1 pass, zero failures/skips; 1,073,741,824 logical bytes; 1,074,932,630 bytes each direction; peak app + PG-child RSS 587,100,160 < 805,306,368 bytes |
| Final PostgreSQL 15 | Exit 0; unsupported-version rejection passed, zero failures/skips |
| Accessibility | Zero violations; Chromium and Firefox, 36 routes, 524 snapshots, 128 interaction states, 7,709 passing text measurements |
| Ticket-specific tests | All 52 passed in the final run |
| Quality | Build, lint, strict templ freshness, whole-tree gofmt and diff checks passed |
| Final source/review | Stable 2,144-file manifests across gates; current manifest match and all eleven AC independently approved by `ses_de68e5d50ffe5sJdg3eEv0Fuak` |
| Coordinator operations | Completed per coordinator handoff: 44 reviewed files staged, cached diff check passed (`staged-paths.json`, `staged.diff`); shutdown verified (`shutdown-verification.json`): PG18 no PID, ports 59441/37417 closed, PG15 container exited, both database data directories retained |

Eight direct helper skips are reconciled to passing parent/subprocess scenarios in `helper-parent-passes.json`; the PG18 version skip is covered by final-source PG15 execution. No missing packages or opt-in skips remain. This is a passing race suite with one named memory-acceptance exclusion and a passing same-source non-race companion, not every test passing under race instrumentation.

Shutdown and staging artifacts above are relative to the durable evidence root. PostgreSQL 15 `PG_VERSION` was confirmed as `15` via `podman unshare`; host permissions did not indicate data loss. The coordinator reports user root changes unchanged and ticket #99 clean. The staged snapshot precedes this final documentation update; the coordinator will restage the document edits and verify indexed source matches final frozen inputs. No commit, push or issue closure was performed by this documentation task.

Independent source/standards reviews and final artifact review cover the authorized Dispatch CSS fix without weakened assertions, thresholds or scale. Final mechanical changes were verified as exactly three generated templates' error-location metadata (`assets_show`, `customer_children`, `customers_show`) plus one argument-spacing-only assertion in `operational_activity_integration_test.go`; strict freshness requires retaining the generated metadata. Exact disposition/diffs: final-gate `final-reconciliation.json`, generated diffs and prebuild archive.

## Historical execution reconciliation — superseded; old raw evidence unavailable

The following bullets preserve reported historical results and pending/blocker statements, not the latest status or currently available old raw proof.

- D `d-handler-run03`: exit 0, **30 top-level + 52 subtests**, zero skips. Earlier 53 was a newline-crossing regex error in derived metadata, now corrected without changing raw evidence. Earlier failed attempts remain retained.
- C `c-report-regression05`: exit 0, **9 top-level + 7 subtests**, zero skips; final shared-report display/cutoff fix supersedes the adjacent retained-unowed failure and earlier focused failures. Spec and Standards reviewers approved the template change, as reported by the coordinator.
- Parent `final-compile-02` / `final-lint-02`: exit 0; `final-format-check.json`: all 25 changed Go files clean.
- `full-race-01`: **exit 2**, 6649.09s; before/after manifests contain 2031 identical source files. All Lease checks and other packages passed. Failures: `TestActivityExportPersistedBrowser`, `TestBackupBrowserNginxScale`, `TestSharedAccessibilityPersistedBrowser`, `TestAccessibleInteractionPersistedBrowser`. All opt-ins were 1 with explicit disposable DB environment. Exact `logs/full-race-01.*` records and `full-race-01-artifacts/` failure snapshot remain retained under the evidence root.
- Exact completed full01 counts (`logs/full-race-01.summary.json`): **1836 top-level PASS, 4 FAIL, 9 SKIP; 5030 subtest PASS, zero subtest failures/skips**. Focused reruns do not alter that failed full-suite record.
- Historical scope audit (2026-11-25, before E green02), `ses_dec0b2a13ffeISo4Gjcp0xsuD6`: affected scripts/production paths were base-identical; backup/accessibility stale oracles identified, Activity timing strongly implicated, interaction diagnosis then open. The interaction uncertainty is now resolved: preexisting native GET before HTMX settled, absent HX header versus successful HTMX GET/503. No production changes.
- Latest E `logs/navigation-fix01-green02.log` / `.exit`: exit 0, Activity and interaction **10/10 each (20 passes, zero skips), 100.795s**. The same independent reviewer verified scope/oracles/actual results. Retain failed intermediate logs, including ambiguous-locator `logs/navigation-fix01-green.log`; green02 supersedes that focused attempt.
- F completed; `f-final-handoff.md` and independent reviewer `ses_debb30f8bffeO89Ko1hD7KQD8X` verify `f-focused03/backup.log` / `backup.exit` **PASS**: non-race full 1 GiB, peak app plus PostgreSQL-child RSS **585,916,416 bytes**, below unchanged **768 MiB**. Non-race memory acceptance is documented; the earlier race RSS failure remains retained.
- `f-accessibility07/test.log` / `exit`: completed race-enabled **FAIL**, 142.70s, no race report; **four contrast-violation groups across both browsers**. Completed coverage: 36 routes, 524 snapshots, 128 interaction states, 7,693 passing text measurements. Actual Dispatch contrast is light badge **2.44:1**, dark text **1.7:1**, dark badge **1.53:1**, versus required **4.5:1**. Relevant production paths are unchanged from `e703bc55` by source comparison, not separately executed base verification.
- F review confirms equal measurement-parity snapshots, no assertion/threshold/scale weakening and a valid geckodriver privilege-flag correction. No speedup, native Firefox keyboard date-entry or persisted Dispatch-save claim is established. Retain focused01 failures, focused02 restart interruption (no exit marker), focused03 accessibility deadline failure, authorization-probe04 and accessibility05 completed stall failure alongside latest results.
- **Actual preexisting product contrast blocks final acceptance. Explicit user scope authorization is required for a narrow Dispatch production fix.** F released the DB with no active test/browser/driver processes per handoff. Full02, frozen-source quality checks, PostgreSQL 15, final AC review, staging and shutdown remain pending.
- Nine skips comprise eight subprocess-helper direct entries and the PostgreSQL <16 rejection case on PostgreSQL 18. Final PostgreSQL 15 rerun remains pending; reconcile helper parents in final evidence.

**Final full-race-02 after fixes is required and is not ready-to-pass.** No full-green, final AC acceptance, staging or shutdown claim is made. In the mapping below, “Pending D + final suite/review” now means D has passed but its final-source reconciliation and final suite/review are pending; it does not indicate an unfinished D run.

## Historical independent source and coverage approvals

Authority: `/tmp/opencode/issue101-evidence-20261125/source-review-approvals.md` (source/coverage approval only).

| Review | Reviewer | Completed scope |
| --- | --- | --- |
| Standards | `ses_deca69cb1ffe8J4j63rurDKpce` | Production source and final 30-test assertion delta approved; duplicate rent/date work addressed. Optional naming/discarded-detail judgments are not blockers. |
| Specification | `ses_deca67b72ffenCJLwZLr386ypD` | AC1–10 source approved, including browser integration, fresh acceptance versus immutable-artifact validation and fixed contracts; AC11 execution pending. |
| Domain | `ses_deca5315cffezhHMgLHxdx4Z1F` | Historical cancellation/initial entitlement and retained periods beyond +30 days corrected. |
| Reference security | `ses_deca7e814ffexd06PxLhlCX5OU` | Adapter and Selection delta approved: required anchors, original/current archive authority and midnight-independent rechecks. |
| Test coverage | `ses_deca1c24fffe3xffDmw2UfJMRT` | All eleven AC covered in source after exact generated-line, distinct settlement-ID and unexpired midnight-revocation oracle improvements; not execution approval. |
| Documentation | `ses_dec903218ffe5PfQn96MQvjb0L` | Coordinator reports all three corrections closed. |
| Additional adversarial authority | `ses_dec918b88ffeQuabbseuMySPJW` | Coordinator reports bounded AC1/6/7/8/9 review approved; not whole-ticket acceptance. |

## Final AC implementation and verification map

Source paths below are relative to `internal/`. Handler tests are in `handlers/lease_exports_integration_test.go` unless marked **race**, **API**, or **browser**, which refer to the corresponding `lease_exports_*_test.go`. **A** means `authorization/lease_export_projection_test.go`; **B** means `exports/leases_integration_test.go`. All AC1–11 are independently accepted by `ses_de68e5d50ffe5sJdg3eEv0Fuak` against the final durable logs and manifest; the final column records that approval.

| AC | Implemented source boundary | Exact test functions / retained verification obligations | Final result / approval |
| --- | --- | --- | --- |
| 1 | `authorization/lease_exports.go`, `exports/leases.go`, `handlers/lease_exports.go`: All Records Export/base View, independent Billing/related facets, operational isolation | `TestLeaseExportIndependentGrantsHTTP`; `TestLeaseExportOperationalPrivileged`; `TestLeaseExportBillingProvenanceAndIndependentFacets` | Independently accepted |
| 2 | `authorization/lease_report.go`, `lease_report_billing.go`, `lease_exports.go`: shared selected-date agreement/coverage projection, explicit unavailable history | `TestLeaseExportAgreementPositionCoherence`; `TestLeaseExportRealCompanyTimezoneBoundary`; **A** `TestLeaseExportHistoricalScheduledCancellationEntitlementProjection` | Independently accepted |
| 3 | Shared report projection and `exports/leases.go`: operational dates/ending range, effective coverage and independent placement | `TestLeaseExportCoveragePlacementAndArchiveHTTP`; `TestLeaseExportFutureCoverageReplacementHTTP`; `TestLeaseExportFilteredSummaryAndValidationHTTP` | Independently accepted |
| 4 | `authorization/lease_report_billing.go`, narrow owning billing helpers and CSV adapter: rent/tax/revisions, bounded queues, captured generation, ordinary settlement/adjustments | `TestLeaseExportBillingProvenanceAndIndependentFacets`; `TestLeaseExportQueueEntitlementBoundaries`; `TestLeaseExportHistoricalFailureRetryUnavailable`; `TestLeaseExportVoidReplacementAndWithdrawnCharge`; **A** `TestLeaseExportHistoricalGenerationWindowProjection` | Independently accepted |
| 5 | Billing projection/adapter retain generation financial and line snapshots separately from corrected current terms | `TestLeaseExportJuneGeneratedDraftHTTP`; `TestLeaseExportBillingProvenanceAndIndependentFacets` | Independently accepted |
| 6 | `authorization/lease_exports.go`, shared report and browser review: normalized filters, SQL Number/ID order, all-matching snapshot and unique-Lease totals | `TestLeaseExportAllMatchingAndPreservationHTTP`; `TestLeaseExportFilteredSummaryAndValidationHTTP`; `TestLeaseExportEmptySensitiveFiltersAndSummaryReferences`; **B** `TestLeaseExportDurableAdapterAllMatching` | Independently accepted |
| 7 | Reference authorization plus browser-only `handlers/exports.go` routing; retained `authorization/field_lease_coverage.go` indicator boundary | `TestLeaseExportArchivedOriginalFacets`; `TestLeaseExportEmptyArchivedSelectionSurvivesRestoreAsProtectedFact`; `TestLeaseExportArchivedDraftHistoryAndSuccessfulAudit`; **API** `TestLeaseExportAuthenticatedBearerAPIExclusion`; existing field/reference-client tests completed in final suite. | Independently accepted |
| 8 | `exports/leases.go`, `lease_references.go`, existing worker/download engine, catalog/recovery wiring: atomic fixed CSV, requester/expiry, strict references and temporally stable prepared bytes | `TestLeaseExportReadyAcrossCompanyMidnight`; `TestLeaseExportReplayExpiryAndFacetLoss`; `TestLeaseExportGrantGainPreservesBytes`; **race** `TestLeaseExportActualMutationSnapshotOrders`, `TestLeaseExportAtomicFailuresAndIntegrity`, `TestLeaseExportInterruptedWorkerAndCompetingRecovery`, `TestLeaseExportExpiryDuringTransferPreparation` | Independently accepted |
| 9 | Supplied authorization transactions and export audits; fresh acceptance validation separated from original/current reference checks | `TestLeaseExportOriginalAndCurrentAssignedAnchors`; `TestLeaseExportArchivedDraftHistoryAndSuccessfulAudit`; **race** `TestLeaseExportActualGrantRevocationBoundaries`, `TestLeaseExportIdentityAssignmentArchiveSerialization`, `TestLeaseExportMalformedRequiredReferences`, `TestLeaseExportAtomicFailuresAndIntegrity` | Independently accepted |
| 10 | Existing export storage/permission catalog reused; no new schema/grants, Lease/Invoice/settlement history remains owned by existing writers | `TestLeaseExportAllMatchingAndPreservationHTTP`; `TestLeaseExportBillingProvenanceAndIndependentFacets`; `TestLeaseExportJuneGeneratedDraftHTTP`; installed migration/upgrade preservation completed in final suite. | Independently accepted |
| 11 | Real DB/HTTP acceptance, deterministic race/fault fixtures, mobile-bearer API probes and production-CSRF browser download with independent CSV parsing | **browser** `TestLeaseExportPersistedBrowser`; **API** `TestLeaseExportAuthenticatedBearerAPIExclusion`; all mapped race/HTTP tests above; final quality, race/browser, same-source scale companion and PG15 gates completed as recorded above. | Independently accepted |

## Historical planning record — not current implementation status

Everything below preserves baseline findings and planning refinements. “Current”, “Gap / change”, absent adapters/routes and prospective “Proof” describe the approved-base planning state, not today's implementation or execution results. Historical line numbers may no longer match. The mapping and review status above supersede those status descriptions; normative contract refinements remain applicable.

## Approved review refinements (normative for implementation)

See [exact interfaces and ownership](v1-ticket-101-interfaces.md), now the authoritative handoff. Contracts/route accepted; no product blocker. AC1/6 require explicit operational mode to suppress financial queries even for privileged users; positive review/void predicates retain compatible Assigned Invoice/Pricing access, negative queues require complete All Records Invoice/Pricing/archive authority. AC2/3/5 require one selected-date agreement position for lifecycle/coverage/rent/endings/periods: corrected history, inclusive termination and cancellation; unavailable imported termination history cannot become current Terminated projected backward. Unprovable membership fails actionably; unavailable totals never become zero.

Final ownership is recorded with session IDs in interfaces: A authorization/narrow billing helpers; B adapters/reference serialization and adapter tests; C handler/UI/catalog/recovery/public docs; D every handler `lease_exports_*test.go` and browser script. Preserve existing SQL Number order (`document_number,id`), with no display-label parsing. Exact normalization/selection APIs and typed references use existing permission IDs; no new grants.

Final AC8/9 correction: split fresh temporal validation at new acceptance/generation/review from immutable artifact authority rechecks. Shared `AuthorizeSelection` and captured-reference checks MUST NOT compare as-of with today's date or reclassify queues. Ready explicit/empty-as-of pending/failed exports remain available across company midnight through Ready+24h; grant/resource loss still blocks. C owns the minimal optional `ValidateAcceptance` invocation in engine Accept, B implements the adapter hook, A owns fresh validation. Recovery can require revised date/new review, not invalidate Ready bytes. D adds `TestLeaseExportReadyAcrossCompanyMidnight` for explicit/empty dates, pending/failed, exact bytes, status, expiry, receipt replay, fresh review and grant-loss control.

Final AC4 correction: current queue covers nonarchived finalized agreements, including Scheduled initial charges and Terminated retained owed periods after shared entitlement/cancellation evaluation. Retained owed ungenerated periods beyond selected date +30 are `not_yet_eligible`, not pending/failed; the same known eligibility applies to historical future periods independently of unavailable historical retry status. No unbounded future enumeration is added.

Final AC7/8/9 selected-resource correction: every `LeaseExportReference` requires `Selection` (`json:"selection"`) of type `LeaseExportSelectionReference`, containing required nonnil lowercase `customers`, `premises`, `assets` arrays of `LeaseExportRelatedReference`. Each array is empty for its zero normalized Filter ID or contains exactly that selected ID once. Capture original archived state in the generation snapshot on every entry and summary, including zero matches. Recheck original AND current resource authority: selection while archived followed by restore still requires the originally used archived View grant. Current active access alone cannot authorize those prepared bytes. No temporal revalidation is added. A owns authorization/semantic checks; B owns strict required-object/array decoding; D updates direct-reference fixtures and adds `TestLeaseExportArchivedSelectionRestoredThenAuthorityLost`: archived selected resource → zero-match Ready → restore → lose archive grant → whole status/download denial. Cover Customer/premises/Asset and malformed/mismatched/missing/null selection anchors.

AC4 failure history is **not retained**: worker failure rows are updated/deleted on retry/success; attempts also contain ordinary pending messages. Use owning current unresolved-failure semantics only. Earlier dates expose failure history unavailable; historical pending/failed filters reject actionably. Known effective owed absence of retained generation is `ungenerated`, never inferred worker pending/no failure. Candidate eligibility is bounded through as-of +30 with relevant retained facts; no indefinite future enumeration. Operational date columns and ending range satisfy AC3/6 without another operational queue feature.

AC8 freezes one snapshot timestamp/date/timezone; exact 27-column operational and 59-column Billing contracts; stable revision ID + retained line ordinal, Invoice generation + obligation/revision linkage; unavailable/blank/zero distinction and deterministic child order. CSV engine row_count includes child/summary rows; matching_lease_count counts unique Leases. AC9 references cover original/current Assigned anchors, all summary contributors and empty sensitive selections. AC10 remains no schema change/no grants/no fabricated history.

AC11 required named tests: `TestLeaseExportHistoricalFailureRetryUnavailable`, `TestLeaseExportOperationalPrivileged`, `TestLeaseExportGrantGainPreservesBytes`, `TestLeaseExportEmptySensitiveFiltersAndSummaryReferences`, `TestLeaseExportOriginalAndCurrentAssignedAnchors`, `TestLeaseExportMalformedRequiredReferences`, `TestLeaseExportAgreementPositionCoherence`. These augment existing denial/failure/concurrency/browser/API/full-suite requirements; none has run as part of planning.

## Source inventory

- `internal/authorization/lease_report.go:56–171`: browser-only, base-View-fenced repeatable-read report, independent facets, strict enum validation, filtered totals, Number/ID sorting, page projection.
- `lease_report.go:173–330`: corrected effective coverage and association periods, inclusive notice termination, rent replay, separately labelled future schedule.
- `internal/authorization/lease_report_billing.go:36–233`: generated obligation/revision snapshot, record-time cutoff, independently authorized Invoice/Pricing/Settlement/Asset/actor details, complete-authority negative generation knowledge, shared entitlement dispositions and manual adjustments.
- `lease_report_billing.go:235–253`: ordinary Payment allocations/Credit Applications/Reversals, not a Lease ledger.
- `internal/authorization/lease_calendar.go:35–81`: read-derived lifecycle and non-drifting monthly boundary clamp.
- `internal/authorization/lease_workspace.go`: existing ending filters and starts/renewal/termination/placement/monthly/failure/review queues; those use today/current placements, so cannot simply substitute for historical report selection.
- `internal/authorization/lease_billing_revision.go:12–61`: retained per-Asset rent/tax before/after revisions available for explicit reporting.
- `internal/handlers/lease_report.go` and `internal/templates/lease_report.templ`: retained URL/cookie state, validation/failure/empty states and snapshot-linked report details; no export review/action.
- `internal/exports/exports.go`, `worker.go`, `download.go`: durable Definition adapter seam, selection authorization, fenced snapshot, transactional bytes/reference/audit/Ready, requester ownership, integrity, expiry and current whole-artifact authorization.
- `internal/exports/catalog.go`: no Lease adapter registered; `internal/handlers/exports.go`: no Lease export routes. `internal/exports/recovery.go` and handler recovery mapping require Lease additions.
- `internal/authorization/field_lease_coverage.go`: Boolean plus observation timestamp only. Existing field API/reference-client tests retain this boundary.

## AC 1 — All Records Export + base View; Billing independent

**Current:** Lease View and Billing View are independently checked in the report (`lease_report.go:61,71,80–95`). `lease.export` already exists as All Records in the catalog/scopes. Related labels and sensitive Invoice/Settlement facets are independently filtered. No Lease export definition or acceptance route exists.

**Gap / change:** Add operational and explicit Billing definitions requiring `lease.export` + `lease.view` All Records, with Billing View for the latter and any Billing predicate. Keep operational output free of financial fields even for broadly privileged users. Authorize selected inputs and current related facets under the transaction fence; no Role-name shortcuts or implicit Invoice/File authority.

**Proof:** Real custom-role browser/HTTP acceptance and download matrix: View without Export; Export missing compatible View (catalog rejection or tampered state); base+Export only; Billing without Export/base; valid Billing; missing Invoice/Pricing/Settlement. Assert job/audit absence for denial. Change hidden rents/taxes/review flags and compare base-only CSV rows/order/counts excluding snapshot timestamp. Browser controls and direct forged POST both tested.

## AC 2 — current/as-of read-derived lifecycle and coverage

**Current:** `leaseCalendar` derives Scheduled/Active/Month-to-Month with inclusive term end and clamped renewal. Report uses explicit company date, retained coverage projection and association periods; termination occurs when `ending < at` except immediate scheduled cancellation. Historical Draft snapshots are deliberately unavailable when absent. Existing `lease_report_history_test.go` covers leap/clamping and inclusive notice; `lease_report_snapshot_test.go` covers Draft history and correction concurrency.

**Gap / change:** Expose the same projection inside the export engine's transaction, with no worker prerequisite or second snapshot. Preserve effective corrected-history semantics. Verify notice revise/withdraw, imported terminated facts and factual corrections rather than infer compliance from today's row alone.

**Proof:** PostgreSQL fixtures across fixed end/end+1, termination/next day, leap/month-end and company timezone around UTC midnight; query without running workers. Export explicit earlier date after corrections/withdrawal; assert retained history, no invented timestamps or stale lifecycle. Deterministic concurrent correction versus snapshot must produce coherent before/after output and totals.

## AC 3 — operational starts/transitions/endings/coverage/placement

**Current:** Browser report has commencement/fixed end/notice end/current renewal fields and selected-date Assets. Association-period query distinguishes unknown/unplaced/wrong premises/placed; Asset return is not coverage removal. Workspace separately exposes operational queues.

**Gap / change:** No CSV. Add operational fixed columns/typed rows, ending range and first monthly transition date; retain lifecycle/placement filters without additional operational queue controls. Do not copy today's workspace predicates into as-of reporting. Coverage and placement remain separate columns/facts.

**Proof:** Finalize then return Asset: coverage stays, placement becomes unplaced. Move within Customer to wrong premises: distinguish from unplaced. Terminate Lease: post-end coverage disappears while physical placement stays. Atomic replacement and future additions must show appropriate effective Assets without current-place leakage into historical positions. Verify starts/monthly/end queue boundaries and hidden-Asset filter denial.

## AC 4 — authorized rent/generation/manual review/Invoice settlement/revisions/adjustments

**Current:** Report replays rent, lists generated snapshots and review/void/ungenerated facts, delegates entitlement to `effectiveLeasePeriodTerms`, exposes authorized ordinary settlement and manual adjustments. Billing revisions are consumed for rent but not returned as explicit before/after report rows. Report structs have no per-Asset tax breakdown; report filter lacks generation-failure queue. Existing worker/history/source functions own eligibility and failure/disposition state.

**Gap / change:** Add explicit authorized tax/rent/revision and generation-queue projections to the shared report/CSV contract. Preserve 30-day generation eligibility and distinct not-owed/void/failed/pending meanings. Reuse retained generation financial/line snapshots. Retain ordinary Invoice settlement facts, never aggregate them into a new Lease balance or infer absent Invoices as receivables.

**Proof:** Real DB generation inside/outside eligibility, failed attempt, retry, withdrawn coverage charge, void without replacement, authorized manual replacement; read with and without complete-generation authority. Billing revision tax/rent before/after and adjustment provenance remain correctly attributed. Payment/application/reversal fixtures reconcile to owning Invoice facts and do not double-count; missing Settlement permission removes status derived from settlement as well as entries. Test revisions/adjustments with hidden related Asset or actor identities.

## AC 5 — immutable generated snapshots / June scenario

**Current:** `lease_report_billing.go:41–44,73–78` uses generation's captured obligation revision financial JSON, not current terms. Review flags are bounded by source-change recording cutoff. `lease_report_test.go:170–212` already checks generated amount retention after abatement/cancellation; unowed periods use domain entitlement in `lease_report_snapshot_test.go`.

**Gap / change:** Carry captured financial/line provenance into CSV unchanged. Add the exact required June 1 / July Draft / June 10 decision / June 30 inclusive termination test; existing report tests are not that exact scenario. No change to owning generation behavior unless demonstrated failure.

**Proof:** Use controlled company clock and coherent inserted recording history in disposable fixtures. Generate July Draft June 1; on June 10 accept June 30 termination. Compare immutable generation JSON/Invoice identity before/after, export retained amount and review flag, June 30 in-force versus July 1 terminated, later unowed periods excluded from pending queue. Repeat after term/charge revision and compare explicit as-of recording cutoff for review flag. Never edit immutable production events to make the fixture pass.

## AC 6 — complete filters, stable Number order, prefiltered totals

**Current:** Report supports lifecycle, Customer, Asset, premises, archive, placement, search and review/void/ungenerated Billing predicates. Number/ID sorting occurs before filtering/paging; `Total` and rent accumulate after authorized matching. `LeaseReportQuery` lacks ending range and operational/failure queue filters. It returns only one page.

**Gap / change:** Add ending range/queue conditions to shared normalization, projection, browser validation and URLs. CSV consumes all matching rows from one snapshot, not a page loop or hard cap. Typed summary counts distinct Leases and sums rent once; child rows never multiply totals. Selected inputs require current access even on empty results.

**Proof:** More than 100 Leases, multiple Assets/Invoices each, nonlexical Number examples, ascending/descending ties, no page truncation; combine each filter and compare independent expected identities/totals. Hidden Billing/Invoice changes cannot alter operational count/order. Test invalid ranges/enums/unknown fields and unauthorized record IDs without leaking whether they exist. Browser review and regeneration retain normalized filters.

## AC 7 — archive history, independent links, field API indicator only

**Current:** Report archive inclusion requires `lease.archived.view`; related records use independent View/archived grants. Generated Invoice access uses owning access helper and Pricing; current report contains no File links. API actor is rejected by Lease report service and actual API route denial is covered in `lease_report_history_test.go:55–70`. Field coverage shape is only `covered`,`as_of`.

**Gap / change:** Export archived Draft/Terminated history explicitly with no new domain effects; include original references for all linked/disclosed records. Register browser-only routes; do not add API adapters, master exports, attachment export or offline recalculation. Loss of linked archived visibility blocks prepared bytes.

**Proof:** Archive Draft and terminated Lease, export current and earlier effective dates, verify no revived billing/coverage/placement or generated records. Deny archived filters without grant. Remove Invoice/Asset/Location archived visibility after Ready: entire artifact denied when those fields were disclosed. Actual mobile bearer requests to report/review/POST/status/download paths remain unavailable even for protected Administrator. Run existing field indicator and reference-client tests proving cached observation does not change with local clock.

## AC 8 — consistent durable all-or-nothing CSV; requester-only 24h

**Current:** Engine already supplies `Definition`, selected-input authorization, atomic bytes/references/generation-audit/Ready (`worker.go:58–104`), restartable Running work, current status/download checks, requester/company ownership, Ready+24h, integrity verification and no partial transfer preparation. It does not know Lease references until an adapter is registered.

**Gap / change:** Add versioned Lease definitions, strict private original-facet references and all-matching snapshot reader. Capture summary contributors and membership-sensitive prerequisites, not only obvious link IDs. Add authorized recovery review for new export after expiry/failure; never salvage a forbidden subset of an old artifact.

**Proof:** Another requester including Administrator denied; 24-hour exact boundary including crossing it during preparation; base/Billing/Export/linked facet/assignment loss before generation and after Ready; empty-result sensitive selection revoked. DB query failure after emitted rows must leave no partial export records/Ready/generated audit. Digest corruption and missing ordinal must produce no CSV. Cancel worker and recover Running; race two worker instances, assert one published set. Verify stored bytes remain immutable after domain edit and a fresh export reflects the new state.

## AC 9 — independent grants, commit-time checks, atomic audit

**Current:** Export acceptance runs `WithinPermission`; generation uses `WithinSnapshotPermission`; status/download use `WithinActor`. Acceptance/generation/download audit use protected audit in the same transaction. Lease report is read-only and does not add browsing audit. Domain writers already own their validation and immutable audit.

**Gap / change:** Adapter uses these supplied fences and never performs independent post-query authorization or out-of-transaction enrichment. Summary/selected-input checks must survive zero result and loss of membership-sensitive facets. Existing read-only report must remain accessible without Export. Do not add unrelated Lease mutations.

**Proof:** Deterministic real PostgreSQL fences: grant revocation, Role replacement/user disable and relevant Job assignment/archive against accept/generate/download; valid serialization only, no partial disclosure. Inject acceptance/generated/download audit failures and assert correct rollback. Same UUID/same intent one accepted job; changed intent rejected. Verify actor/effective Role/permission/scope/system executor in recorded export audits, and no false successful audit on expired/integrity-failed download.

## AC 10 — preserve installations and grants, non-destructive migration

**Current:** Lease source history, identifiers and durable export tables exist; `lease.export` is already defined. No new permission or schema is needed for the proposed adapter. Installed fixtures and existing migration-preservation tests remain relevant.

**Gap / change:** Expected none in schema. Store new versioned kinds/options/references in existing export tables, preserve earlier kinds' semantics, and do not grant anything to existing nonprotected Roles. If an unforeseen schema requirement appears, agree additive preservation design before implementing; missing historical facts stay unavailable.

**Proof:** Existing-installation fixture before/after: same Role grants, Lease/Asset/Customer/Invoice IDs, number sequences, relation/event/revision/financial JSON and settlement rows; only expected export jobs/records/audit added. Existing definitions remain readable. Run migration/upgrade tests in the full database suite and report actual results; do not invent an export-specific migration simply to produce a migration test.

## AC 11 — real database, browser/API denial, failures/concurrency, required checks

**Current:** Real DB/HTTP report tests exist in `lease_report_test.go`, `lease_report_history_test.go`, `lease_report_snapshot_test.go`, `lease_report_recovery_test.go`; browser script is `testdata/browser/lease-report.cjs`, enabled by `BROWSER_TESTS=1`. Existing export tests demonstrate durable engine races/failures but do not qualify a nonexistent Lease adapter. `Makefile` lint runs Go vet, test runs verbose Go tests with race detector. Missing `FREEFSM_TEST_DATABASE_URL` skips integration tests.

**Gap / change:** Implement ticket-specific adapter, production-route, browser download and API denial tests, with deterministic races and persisted effects assertions described above. Reuse ordinary fixtures, not the retired generalized harness or a helper-only mock service. Record exact actual commands/results and skipped coverage honestly.

**Proof / gate:** `make lint`; focused real-DB race tests; `BROWSER_TESTS=1 FREEFSM_TEST_DATABASE_URL='<actual disposable URL>'` browser run; `FREEFSM_TEST_DATABASE_URL='<actual disposable URL>' make test`. Database, browser prerequisites and results must be confirmed by implementation coordinator. Planning has not run these acceptance suites, and no AC is marked passed.

## Planning validation

Issue #101 has eleven unchecked AC and no additional issue comments returned by `gh issue view 101 --comments`. Parent body was read including LEA/PER/DISC/AUTH/TEST requirements. `gh issue view 74` and `96` report CLOSED; `94` and `97` report OPEN. Initial `git status --short` was clean. Only the two requested planning documents are intended outputs; no staging, implementation, commit, push, or issue closure is part of this task.
