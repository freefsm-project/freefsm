# Issue #97 — master and sensitive Item exports

Contract: live [#97](https://github.com/freefsm-project/freefsm/issues/97) and parent [#18](https://github.com/freefsm-project/freefsm/issues/18), read 2026-11-22. Normative requirements: PER-05/10/12, MAS-01/03/04/05/07 and DISC-05.

## Product integration

`exports.Product` is the shared HTTP/worker registry. Each export uses the existing durable Pending → Running → Ready/Failed CSV lifecycle; no alternate export engine or field-API endpoint is introduced. Collection pages and My CSV exports link to a review that shows filters, sorting, All Records scope, fixed columns and the 24-hour requester-only availability. Confirmation creates an immutable operation intent. Empty results use the same lifecycle and produce headers.

| Export | Review and request route | Independent prerequisites (All Records) |
|---|---|---|
| Contact | `/contacts/export` | `contact.export`, `contact.view` |
| Location | `/locations/export` | `location.export`, `location.view` |
| Asset | `/assets/export` | `asset.export`, `asset.view` |
| Project | `/projects/export` | `project.export`, `project.view` |
| Asset Service Entry | `/service-entries/export` | `service_entry.export`, `service_entry.view`, `asset.view` |
| Item base | `/items/export` | `item.export`, `item.view` |
| Pricebook | `/pricebook/export` | `pricebook.export`, `pricebook.view`, `item.view` |
| Item Cost | `/items/cost/export` | `cost.export`, `cost.view`, `item.view` |

Archived inclusion is explicit (`archived=1`) and additionally requires the corresponding archived View (Asset archived View for Service Entries; Item archived View for all Item facets). Assigned/own/context visibility cannot substitute for All Records. Customer financial exports remain separate.

Collection links preserve search and applicable Customer, Project Status, Asset Type/Status filters. Asset/Item archived-only collections additionally send `archived_only=1`; their exports do not accidentally include current records. Item lists with authorized SKU search send `search_sku=1`, which retains the Pricebook View prerequisite through generation/download even for base/Cost CSVs. Unsupported, malformed and repeated filter parameters are rejected rather than silently broadening selection. Pagination is not an export limit. Service Entry links on an Asset preserve the selected Asset ID.

## Fixed columns

The following order is versioned by each adapter's `Kind()`; CSV text uses the existing reversible spreadsheet-formula escaping. IDs identify separate records even when names or contact details match. Nested JSON cells preserve ordered provenance without expanding Jobs/documents into recursive rows.

- **Contact:** `contact_id, first_name, last_name, role_title, email, phone, service_notes, notes, primary_customer_default, customer_id, customer_label, customer_visibility, job_links_json, job_links_visibility, archived`.
- **Location:** `location_id, title, address_1, address_2, city, state, zip_code, latitude, longitude, access_notes, service_notes, notes, primary_customer_default, customer_id, customer_label, customer_visibility, job_links_json, job_links_visibility, archived`.
- **Project:** `project_id, name, description, status_id, status_label, status_category, displayed_progress_percent, retained_manual_progress_percent, planned_start_date, planned_end_date, service_notes, notes, legacy_review_reasons_json, legacy_captured_timezone, custom_fields_json, customer_id, customer_label, customer_visibility, location_id, location_label, location_visibility, archived`.
- **Asset:** `asset_id, number, name, ownership_class, association_kind, customer_id, customer, location_id, location, type_id, type, status_id, status, serial_number, manufacturer, model, installed_at, warranty_expires, service_notes, custom_fields, archived, association_periods, association_events`.
- **Asset Service Entry:** `service_entry_id, asset_id, asset_number, asset_name, asset_archived, job_id, job_title, service_date, summary, responsible_users, revision_id, revised_at, actor_id, actor, accepted_at, revisions`.
- **Item base:** `item_id, name, type, description, is_active, archived`.
- **Pricebook:** Item base columns followed by `sku, unit_price, taxable, tax_rate`.
- **Item Cost:** Item base columns followed by `unit_cost`.

Primary Contact/Location flags are Customer defaults, not Job links. Project Completed displays 100 while retaining manual progress separately. Company Asset placement is distinct from Customer ownership. Item facets share the same Item ID; base contains neither sale nor Cost fields, Pricebook contains no Cost, and Cost does not imply Pricebook access.

Contact/Location explicit Job links are independently disclosed only under All Records Job View (with explicit archived inclusion and archived Job View for archived links); IDs/titles and snapshot authority references remain separate from the primary-default flag. Detailed nested-cell and retained-data semantics are documented in [master records](v1-ticket-97-master-records.md), [Assets/Service Entries](v1-ticket-97-assets.md), and [Item facets](v1-ticket-97-items.md).

## Disposable verification environment

Fresh cluster initialized specifically for this run, not a reused evidence database:

```sh
initdb -D /tmp/opencode/issue97-evidence-20261122/pgdata -A trust -U issue97
pg_ctl -D /tmp/opencode/issue97-evidence-20261122/pgdata \
  -l /tmp/opencode/issue97-evidence-20261122/postgres.log \
  -o '-p 55997 -k /tmp/opencode/issue97-evidence-20261122 -h 127.0.0.1' start
createdb -h 127.0.0.1 -p 55997 -U issue97 issue97
psql 'postgres://issue97@127.0.0.1:55997/postgres?sslmode=disable' -v ON_ERROR_STOP=1 \
  -c 'CREATE ROLE issue97_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS; ALTER DATABASE issue97 OWNER TO issue97_runtime;'
export FREEFSM_TEST_DATABASE_URL='postgres://issue97_runtime@127.0.0.1:55997/issue97?sslmode=disable'
export FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://issue97@127.0.0.1:55997/postgres?sslmode=disable'
export FREEFSM_INSTALLATION_TEST_ADMIN_URL='postgres://issue97@127.0.0.1:55997/postgres?sslmode=disable'
export BROWSER_TESTS=1
export BROWSER_EXECUTABLE=/usr/bin/chromium-browser
export NODE_PATH=/tmp/opencode/issue97-evidence-20261122/browser/node_modules
export GODEBUG=netdns=go
export TMPDIR=/tmp/opencode/issue97-evidence-20261122/tmp
```

Playwright installed outside the repository with `npm install --prefix /tmp/opencode/issue97-evidence-20261122/browser playwright`. Verified module resolution and a real headless Chromium launch; browser version `157.0.8103.78`. Exact logs and environment script are under `/tmp/opencode/issue97-evidence-20261122/`.

## Verification results and acceptance matrix

Initial generation/build and `make lint` (`go vet ./...`) succeeded. Initial routed runs found a real pgx statement-cache collision between Item facet cursor result shapes; the Item adapter owner corrected it. Initial fixture grant-edit errors were corrected in tests. Earlier logs are retained rather than overwritten.

The runtime is unprivileged and remains in origin trigger mode. Snapshot concurrency fixtures use the separate administrator only for backend-local out-of-band source mutation while the production exporter holds its authority fence; they verify a normal runtime writer remains blocked. The initial superuser-only setup was insufficient for this existing fixture and was replaced before final evidence runs.

### Acceptance matrix

These are executable evidence pointers, not a substitute for the final command results. HTTP tests use the migrated real-PostgreSQL fixture, production router/authorization middleware and persisted authenticated sessions. Adapter-focused PostgreSQL tests are supplemental domain evidence.

| #97 criterion | Evidence |
|---|---|
| Eight documented fixed-column exports, independent Export + compatible View | Column contract above; `TestMasterExportsIndependentAuthorityHTTP` tests all eight routes, every prerequisite, sibling export denial and empty Ready files. `TestMasterExportOwnTimeAndItemEditingDoNotImplyExportHTTP` verifies Own Time authority and Item/Pricebook/Cost editing do not confer any of these exports. |
| Immutable Contact/Location Customer, duplicate identities, primary defaults vs Job links | `TestMasterExportCollectionFiltersHTTP`, `TestMasterExportsRelationshipsSortedHTTP`; `TestMasterRecordContactLocationDisclosuresPostgres` additionally checks primary-only versus explicit deduplicated Job links, independent Customer/Job disclosure, archived links and lost-grant whole-artifact denial. |
| Asset Number/class, current owner/placement, authorized historical associations, old Job nondisclosure | `TestAssetExportBoundedAssociationsAndDisclosureRevocationHTTP`, `TestMasterExportAssetFilterAndArchiveHTTP`, and `TestAssetExportTransferCorrectionArchivedLabelsAndHistoricalJobHTTP` exercise origin and regular event corrections, archived bounded labels/redaction and historical Job nondisclosure after transfer. |
| All Records Service Entry lifetime export with Asset/Job/date/responsible/current summary/revision provenance; Assigned denial | `TestServiceEntryExportLifetimeRevisionsAndAssetRecheckHTTP` covers renamed disabled participants, revision predecessors, unlink/Job archive, selected Asset and whole-result archive recheck; `TestMasterExportsIndependentAuthorityHTTP` covers Assigned View denial and empty-result prerequisites. |
| Project direct Customer/Location, semantic Status, Completed 100 and retained progress, no recursive expansion | `TestMasterExportsRelationshipsSortedHTTP`; `TestMasterRecordProjectSemanticsPostgres` adds legacy uncertainty, restored manual progress, independent relationship visibility, calendar dates and retained/retired Custom Fields. |
| One Item identity; base/Pricebook/Cost separation; separate Cost authority | `TestMasterExportsItemSnapshotFacetsHTTP` verifies all three 260-row CSVs; `TestTicket97ItemExactNumericDurableFacets` seeds and compares exact SQL numeric text (zero, cents and maximum-sized amounts) without float-based expectations; `TestTicket97ItemSKUSearchDurableAuthority` proves optional SKU membership authority and whole-artifact revocation. |
| Explicit archives, SKU reservations, retired values, existing line snapshots and no invented history | Collection archive/filter tests; `TestMasterRecordOwnArchiveAndCollectionSearchPostgres` checks Contact/Location/Project own-row archive inclusion/exclusion and post-Ready authority; `TestMasterExportsPriorSchemaPreservationHTTP`; `TestMasterExportPreservesArchivedSKUAndLineSnapshotsHTTP` compares complete Item/SKU-claim/Job/Estimate rows after all three exports and rejected SKU reuse; Project retained/retired/unknown-history tests; Asset unknown-origin/history tests. Exports have no domain-write or migration path. |
| All matching sorted rows, one snapshot; requester-only 24 hours; full-result rechecks | 260-row Item facet and Contact/Location/Project cursor tests plus `TestMasterExportAssetAndServiceEntryCursorHTTP` cover every adapter beyond 128 rows. `TestMasterExportsItemConcurrentSnapshotHTTP` pauses after ordinal 128 and commits an independent source edit while verifying the runtime writer fence. All-eight authority tests cover requester-only receipts/downloads, expiry and empty artifacts. `TestMasterExportsItemWholeArtifactRecheckHTTP` restricts the second sorted row and verifies no partial CSV. |
| Commit-time authority/invariants and immutable atomic audit | All-eight queued grant-revocation acceptance tests; `TestMasterExportAcceptanceAuditFaultHTTP` injects acceptance audit failure, checks zero committed requests/audits, retries the same intent, verifies correct action/actor/All scope for all three lifecycle audit events and rejects audit mutation. `TestMasterExportsAuditAtomicityHTTP` injects generation/download audit failures and races four workers, requiring one generation and no partial artifacts. |
| Existing-install preservation, stable data/relationships, no permission auto-grants | `TestMasterExportsPriorSchemaPreservationHTTP` compares complete seeded rows/grants across the durable-export migration, including duplicate masters, Project progress, archived Item SKU and monetary facets, Asset/Job links and Service Entry revisions/responsible identities. No #97 migration is added. |
| Real database, browser and public API evidence, denial/failure/concurrency | Above routed tests plus `TestMasterExportsPersistedBrowser`: Chromium navigates all eight hub links, reviews/confirms, leaves/returns to persisted receipts and downloads both populated/empty artifacts. It additionally starts from the filtered archived Item collection and verifies exact identity, SKU-membership authority and excluded sale/Cost fields. Authenticated public API GET and POST export/request/receipt/download routes remain excluded. Final repository command results follow below. |

Final required commands succeeded; exact results and skips are recorded below. Earlier attempts are retained: the first full suite exposed PostgreSQL's default lock-table capacity (`out of shared memory`, SQLSTATE 53200) during concurrent migrations and was stopped, not counted as a completed run. The disposable cluster was resized and restarted with:

```sql
ALTER SYSTEM SET max_locks_per_transaction = 1024;
ALTER SYSTEM SET max_connections = 200;
```

`pg_ctl -D /tmp/opencode/issue97-evidence-20261122/pgdata -l /tmp/opencode/issue97-evidence-20261122/postgres.log restart` succeeded. Effective settings are retained in `pg-capacity-verified.log`; earlier failed logs remain retained.

The 60-minute full run (`GOFLAGS=-timeout=60m make test`, `make-test-final.log`) exited 2. The handler package reached a new lease test at the cumulative deadline (0 seconds in that test, runnable fixture-migration stack), leaving 491 of 1,257 handler tests unstarted. It was a cumulative timeout, not evidence that the named lease test hung. That run also exposed three duplicate-label browser selectors caused by the new navigation link; the sidebar is now labeled **CSV exports**, distinct from existing **My CSV exports** content links. A focused race run of Activity, Protected Audit, Customer and new master export browser journeys passed after the fix (`browser-regression-fixed.log`, 23.468s).

Two backup environment problems were reproduced and resolved without changing unrelated product code:

- Copied the repository's `internal/backup/testdata/extension/backup_preflight_fixture*` files into the owned `share/extension/` directory, configured `extension_control_path='/tmp/opencode/issue97-evidence-20261122/share:$system'`, and reloaded this cluster. `TestBackupCreateUnsupportedLayout` then passed (`backup-extension-fixed.log`). No host/shared extension directory was modified.
- `TestPendingActivityBlocksNewWorkAndReplaysExactly` also failed in isolation. A bounded diagnostic stack showed libc `getaddrinfo` waiting on the fixture hostname `unused`; `PGCONNECT_TIMEOUT=5` did not bound this lookup. Selecting Go's resolver with `GODEBUG=netdns=go` made the unchanged race-enabled test pass in 8.593s (`backup-preflight-go-resolver.log`). The final environment script retains that setting.

The informed full-suite retry was `source /tmp/opencode/issue97-evidence-20261122/env.sh; GOFLAGS=-timeout=180m make test`, retaining the Makefile's `go test -v -race ./...` and normal package parallelism. It finished without timeout: backup passed in 411.462s; handlers finished in 5580.147s; overall exit 2 (`make-test-complete.log`). Its only failures were nine existing browser tests whose scripts call `path.join(process.env.TMPDIR, ...)`: the environment had not explicitly defined `TMPDIR`. All issue97 tests passed in this full run.

Created the owned `tmp/` directory and retained `TMPDIR` in `env.sh`. The exact nine previously failing Office Payment and Time/Timesheet browser tests then passed together under `-race` (`browser-tmpdir-fixed.log`). No product changes were needed. The unchanged full command subsequently **passed, exit 0**, with this complete environment (`make-test-attempt4.log`). The earlier 60-minute run's 32 skip records and unstarted-test inventory remain in `full-suite-summary.txt`; they are superseded by the completed-run inventory in `full-suite-final-summary.txt`.

### Final command results

All log filenames below are relative to `/tmp/opencode/issue97-evidence-20261122/`. DB/browser commands sourced `env.sh`, whose full explicit values are documented above. PostgreSQL was 18.6. Normal Go package parallelism was retained; `-timeout=180m` changes only the package deadline.

| Actual command | Result | Exact log |
|---|---|---|
| `make build` (includes vocabulary, Ent and templ generation) | PASS, exit 0, after final navigation fix | `build-post-browser-fix.log` |
| `make lint` (`go vet ./...`) | PASS, exit 0, after final build | `lint-post-build-final.log` |
| `go test -v -race ./internal/exports ./internal/handlers -run '^Test(MasterExport\|MasterRecord\|Ticket97Item\|AssetExport\|ServiceEntryExport)' -count=1` | PASS; exports 1.194s, handlers 89.448s; no selected skips | `ticket97-verified.log` |
| `go test -v -race ./internal/handlers -run '^TestMasterExportAssetAndServiceEntryCursorHTTP$' -count=1` | PASS, 3.085s; 260 rows through each routed lifecycle | `asset-service-cursor.log` |
| `go test -v -race ./internal/handlers -run '^Test(ActivityExportPersistedBrowser\|ProtectedAuditPersistedBrowser\|CustomerExportPersistedBrowser\|MasterExportsPersistedBrowser)$' -count=1` | PASS, 23.468s, after duplicate-label fix | `browser-regression-fixed.log` |
| `GOFLAGS=-timeout=180m make test` (`go test -v -race ./...`) | **PASS, exit 0**; handlers 5404.200s, backup 406.392s, executable package 33.278s; 44 explicit skip records | `make-test-attempt4.log` |
| `git diff --check` | PASS, exit 0 | `diff-check-final.log` |

The final Go command reported some unchanged packages, including `internal/exports` and `internal/templates`, as **cached**, not newly executed. Their earlier actual runs remain retained; the new issue97 race selection was explicitly uncached (`-count=1`). Final package-by-package dispositions, skips and log line numbers are in `full-suite-final-summary.txt`. Exact earlier focused commands are also retained in `coordinator-commands.txt` and `master-export-commands.txt`.

### Explicit skips in the successful full run

There were **44 skip records**, including subtests. None is claimed as passed. No issue97-selected integration/browser test was skipped. `[no test files]` package results are not test passes either.

- **Unsupported-version negative case (1):** `TestStartupRejectsUnsupportedServerBeforeBootstrap` requires PostgreSQL below 16; the disposable server was 18.6.
- **Standalone subprocess entry points (8):** `TestAuthorizationRestartProbe`, `TestBackupCrashChild`, `TestExactSourceHistoryInitializer`, `TestAssetIdentityRestartProbe`, `TestProtectedAuditWorkerProcess`, `TestCustomerExportWorkerProcess`, `TestTransferRestartProbe`, `TestNotesReceiptRestartProbe`. These skip when invoked without their parent-supplied subprocess context; their parent workflows ran separately.
- **Separate opt-ins (2):** `TestBackupBrowserNginxScale` (scale/proxy opt-in); `TestSharedAccessibilityPersistedBrowser` (`ACCESSIBILITY_TESTS=1` not enabled).
- **Actual Secret Service/offline-key opt-in (33):** `FREEFSM_OFFLINE_KEY_TESTS=1` with a real Secret Service was not configured. Exact skipped names:

```text
TestClockSyncProtectedReferenceHTTP
TestClockSyncProtectedDenialHTTP
TestClockSyncProtectedInstalledIdentityHTTP
TestClockSyncProtectedDependencyRepairHTTP
TestClockSyncProtectedExecutableHTTP
TestClockSyncProtectedMalformedGPSRepairHTTP
TestClockSyncProtectedNullGPSRepairHTTP
TestClockSyncProtectedDeniedGPSRepairHTTP
TestClockSyncProtectedStandaloneWithoutJobGrantHTTP
TestCommentSyncEncryptedReferenceHTTP
TestCommentSyncReceiptFirstScopeLossHTTP/assignment
TestCommentSyncReceiptFirstScopeLossHTTP/document
TestCommentSyncReceiptFirstScopeLossHTTP/assignment-failure
TestCommentSyncReceiptFirstScopeLossHTTP/document-failure
TestCommentSyncOriginalEligibilityHTTP/convert-revert
TestCommentSyncOriginalEligibilityHTTP/archive-restore
TestCommentSyncOriginalEligibilityHTTP/relink
TestCommentSyncOriginalEligibilityHTTP/relink-back
TestCommentSyncOriginalEligibilityHTTP/assignment-aba
TestJobFieldSyncEncryptedReferenceHTTP
TestJobFieldsMixedExecutionProtectedReferenceHTTP
TestJobFieldsProtectedReferenceHTTP
TestOfflineFilesProtectedReferenceHTTP
TestOfflineFilesTransportRecoveryHTTP
TestOfflineFilesProtectedNoopHistoryHTTP
TestOfflineFilesDocumentLossPreservesIndependentWorkHTTP/snapshot
TestOfflineFilesDocumentLossPreservesIndependentWorkHTTP/restore
TestOfflineFilesDocumentLossPreservesIndependentWorkHTTP/transport
TestOfflineFilesDocumentLossPreservesIndependentWorkHTTP/receipt
TestOfflineFilesDocumentLossPreservesIndependentWorkHTTP/publication
TestOfflineFilesDocumentLossPreservesIndependentWorkHTTP/publication-receipt
TestOfflineNotesProtectedReferenceHTTP
TestOperationBatchProtectedReferenceHTTP
```

### Independent review P2 follow-up: Asset collection/search parity

Independent review identified a P2 collection-to-export mismatch after the full
suite recorded above: Asset export search concatenated Number, name, serial,
manufacturer and model. An Asset with name `Pump` and serial `SN1` therefore
matched `Pump SN1` in CSV but not in `/assets`. The authenticated routed
regression reproduced this exact failure before the fix: collection IDs `[]`,
exported IDs `[1]`.

The original Asset adapter owner changed only its search predicate to OR the
individual Number/name/serial/model/manufacturer fields. It now reproduces the
production list's Ent `ContainsFold` PostgreSQL behavior: `ILIKE` with the
Go-lowercased search pattern and literal escaping of `%`, `_` and backslash.
There is no internal-ID search and no cross-field concatenation. Existing
filters, archived semantics and sorting are unchanged; collection links retain
`sort=updated`, while the export hub default remains `name`.

`TestAssetExportCollectionSearchParityHTTP` uses the migrated production router
and persisted authenticated session. It compares collection Asset links against
parsed CSV identities by following the actual collection export link through
review, POST acceptance, `exports.Product.ProcessOne` and download. Its nine
cases cover the cross-field phrase, case folding, all five searchable fields,
and literal percent/underscore/backslash behavior, including a wildcard-decoy
Asset. The existing Asset and Service Entry tests remain in the post-fix race
selection, including archive/filter and 260-row cursor coverage.

All following log paths are relative to
`/tmp/opencode/issue97-evidence-20261122/`. Database commands first ran
`source /tmp/opencode/issue97-evidence-20261122/env.sh`; tests used isolated
migrated schemas in the retained disposable database. Commands were captured
with `set -o pipefail`, timestamped `tee` logs and the Go/make exit status.

| Actual command | Actual result | Log |
|---|---|---|
| `go test -race ./internal/handlers -run '^TestAssetExportCollectionSearchParityHTTP$/cross-field_phrase$' -count=1 -v` (before fix) | Expected regression failure, exit 1; collection/export membership mismatch reproduced | `asset-search-p2-before-20261122T203145Z.log` |
| `go test -race ./internal/handlers -run '^Test(AssetExport\|ServiceEntryExport\|MasterExportAsset)' -count=1 -v` (after fix) | PASS, exit 0, 11.470s; seven top-level tests and all nine search subtests passed, no selected skips | `asset-search-p2-race-20261122T203239Z.log` |
| `make lint` | PASS, exit 0 | `asset-search-p2-lint-20261122T203347Z.log` |
| `go build ./internal/exports ./internal/handlers` | PASS, exit 0 | `asset-search-p2-build-20261122T203347Z.log` |

`gofmt` was applied to the changed Go files. **The successful full-suite run
above is pre-P2-fix evidence, not a full-suite verification of this final search
change.** Per the requested scope, the approximately 90-minute full suite was
not repeated; the uncached race selection, lint and targeted build above are
the post-fix verification. This follow-up changed only `internal/exports/assets.go`,
`internal/handlers/asset_service_exports_integration_test.go` and this evidence
document. No git mutations were performed.

### Handoff state

No unresolved issue97 implementation or required-check blocker remains. No git staging, commit, push, reset or rebase was performed. Adapter files were edited only by their original owners; integration changes own the shared registry, routed review/request flow, navigation/templates and evidence tests. Generated template changes are retained. An earlier Clock browser run with unset `TMPDIR` had created `internal/handlers/undefined/c`; that confirmed test profile was moved intact to `earlier-undefined-browser-profile/` under the evidence directory. `git-status-final.log` records the remaining unstaged/untracked source/document state. The disposable PostgreSQL cluster and exact logs remain available for coordinator review; they are not a reused external evidence database.
