# #96 Customer CSV export verification

Date: 2026-10-12. Implementation: `800a2fd17be2c1afbae5a7b46a39074cef62cf24`.
Branch/worktree: `implement/v1-96`, `/var/tmp/freefsm-v1-96`.
Base and checked integration tip:
`6fb21dfc3587025d33d02fcb72757f7f57f5502f`.
`git merge integration/v1-spec-18` reported **Already up to date**;
`git merge-base --is-ancestor integration/v1-spec-18 HEAD` passed.

## Acceptance evidence

All tests below exercise production HTTP handlers with migrated PostgreSQL;
worker calls use the production durable service, including an actual killed and
replacement worker subprocess. Test names have prefix `TestCustomerExport`.

| Criterion | Evidence |
|---|---|
| Explicit Export + compatible View, confirmation, operational-only fields | `DurableAcceptanceHTTP`, `SearchFacetsAndCommitRecheckHTTP`, `SnapshotHTTP`, `CSVValuesHTTP`, `PersistedBrowser`; fixed contract in `customer-csv-exports.md`. |
| All sorted matching rows; coherent snapshot/count/as-of | `SnapshotHTTP` exports 106 rows despite a submitted 25-row page. `SnapshotConcurrentEditHTTP` pauses after 128 rows, changes all source names and inserts another Customer, then verifies all 260 original rows and count, without mixing states. |
| Durable lifecycle and acceptance | `DurableAcceptanceHTTP`, `RestartConcurrencyAndEmptyHTTP`, `ProcessCrashRecoveryHTTP`; same-operation replay and changed-intent denial, eight concurrent accepts/workers, real process kill during publication, one generation audit. |
| Requester isolation and exact 24-hour cutoff | `AuthorityExpiryAndFailureHTTP` denies another Administrator and expiry; `ExpiryDuringTransferPreparationHTTP` crosses expiry inside a slow audit insert and verifies no CSV or successful-download audit. |
| Current whole-snapshot row/field authority | `AuthorityExpiryAndFailureHTTP`, `SearchFacetsAndCommitRecheckHTTP`, `EmptySnapshotRechecksContractHTTP`; one archived row or revoked Export/View/search facet blocks all content, durable block survives regrant/restoration, even empty snapshots recheck requested facets. |
| Failure, concurrency and restart | `StorageAndAuditFailuresHTTP`, `AuthorityExpiryAndFailureHTTP`, `ProcessCrashRecoveryHTTP`; record-storage and generation-audit faults roll back publication, transfer-temp failure and corrupt bytes disclose no CSV, download-audit failure discloses no CSV, actual worker death recovers one logical job. |
| Deterministic and safe CSV | `CSVValuesHTTP`, `SnapshotHTTP`; duplicate names/ID tie-breaks, commas, quotes, line breaks, Unicode, formula-leading and whitespace-prefixed formulas, leading apostrophes; explicit reversible encoding documented in confirmation and contract. |
| Audit and understandable UI | `StorageAndAuditFailuresHTTP` verifies two successful downloads have two audit events; generation audit atomic with Ready. `PersistedBrowser` verifies filtered confirmation, navigation recovery, count/as-of, copy warning and real download at 390px. Empty/Failed/expired/blocked outcomes tested through HTTP. |
| Commit-time authorization/invariants | `SearchFacetsAndCommitRecheckHTTP` queues acceptance behind a grant revocation. Authority fence acquired before repeatable-read snapshot; requester/session and authority rechecked through commit; immutable shared audit. |
| Non-destructive upgrade | `UpgradeAndFieldAPIExclusion` migrates genuine pre-export schema, preserves recorded Customer identity/Service Notes and exact Role grants, and proves even an Administrator bearer cannot expose field-API exports. |
| Required checks | Generation, lint, compile, uncached complete race suite with explicit real database and backup-admin URL, plus focused race-enabled production HTTP/browser suite: passed. |

## Reproduction and logs

Owned PostgreSQL **18.6**, UTF8, port **55496**:

```sh
export TMPDIR=/var/tmp/freefsm-v1-96-runtime/tmp
export GOTMPDIR="$TMPDIR"
export GOMAXPROCS=2
export FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55496/freefsm_v1_ticket_96?sslmode=disable'
export FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55496/postgres?sslmode=disable'
export MAKEFLAGS='BUILD_DIR=/var/tmp/freefsm-v1-96-runtime/build'

GOFLAGS=-p=2 make generate
GOFLAGS=-p=2 make lint
GOFLAGS=-p=2 make BUILD_DIR=/var/tmp/freefsm-v1-96-runtime/build compile

BROWSER_TESTS=0 GOFLAGS='-p=2 -count=1 -timeout=60m' make test

BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
NODE_PATH=/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules \
GOFLAGS=-p=2 go test -race ./internal/handlers \
  -run '^TestCustomerExport' -count=1 -v
```

All final commands exited 0. Full post-merge results include:
`internal/backup` **818.691s**, `internal/handlers` **811.705s**;
the focused export suite (including browser) **24.752s**.
Chromium was **154.0.8037.97**. The helper-only
`TestCustomerExportWorkerProcess` skips direct invocation and runs successfully
inside its crash-recovery parent. All 13 actual export scenarios passed.

Evidence root: `/var/tmp/freefsm-v1-96-runtime/`:

- `generate-final.log`, `lint-final.log`, `compile-final.log`
- `exports-final.log`: all export scenarios, including persisted Chromium
- `full-test-postmerge.log`: complete uncached race suite on implementation commit
- `full-test-final.log`: preceding successful complete serial-package run
- `browser-final.log`: additional successful focused Chromium run

The owned cluster is `/var/tmp/freefsm-v1-96-runtime/postgres`. Its test-only
`extension_control_path` includes this worktree's backup fixture extension.
No shared cluster was stopped/restarted/reconfigured, and no original-checkout
files were changed. Temporary/build/database outputs use the owned `/var/tmp`
root. Disposable files/logs remain available for acceptance review; only the
owned cluster is stopped after verification.

## Honest failures, skips and scope

TDD initially failed on the absent HTTP export route, absent worker, missing
browser entry point, empty-result facet revocation, and expiry during audit.
Those regressions now pass. The browser script was corrected to await HTMX
navigation completion instead of reading the prior page during its update.

The first full attempt had an unrelated existing backup encryption timeout in
`TestAsyncFailuresAreActionableAndSanitized` under concurrent host verification
load, then hit the tool's 20-minute command limit. It is **not** counted as a
pass. Both subsequent complete runs passed, including that backup test. An early
browser invocation used unavailable `/usr/bin/chromium`; final runs use the
installed `/usr/bin/chromium-browser`.

The full DB suite deliberately has `BROWSER_TESTS=0`; unrelated browser/accessibility
opt-ins were not rerun for this ticket. The Customer export browser was run
separately and passed. PostgreSQL-before-16 startup testing and the optional
1-GiB nginx backup scenario were not enabled. Subprocess-only authorization and
backup helper entry points skip standalone execution and are exercised by their
parent tests. No broad browser/device matrix or native reboot proof is claimed.

No known unimplemented #96 criterion remains. Downstream report/export contracts
are not implemented by this ticket: they reuse `exports.Definition`, the single
`exports.Product` registry, and the durable engine. PostgreSQL capacity, temporary
transfer capacity and the shared writer fence tradeoff are documented in the
contract; no unbounded-capacity or latency guarantee is claimed.
