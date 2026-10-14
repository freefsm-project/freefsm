# Integration acceptance: #35, #37 and scoped #107

Reviewed and executed 2026-10-14. Starting integration was clean
`a4848b5318e7574be4b97701c2cf696e071f5b53`. Validated composed source is
`721b73bb0c0870e90f78833ce9b12156d318942e`; this evidence-only commit follows it.

## Decisions

| Ticket / frozen candidate | Decision | Integration |
|---|---|---|
| #35 `deab6f0b681b4c717d20e98510a1adb55905152c` | Accepted for all owning criteria | Merge `b548d18` |
| #37 `db6cdb337dfec37ba442d692111feb1543ceab8e` | Rejected: two reproduced schedule state/recovery defects | Unmerged |
| #107 `48c1999caf30c0e36d31e65f530fa9c67e506d31` | Scoped acceptance of four source/native repairs only; issue remains OPEN | Merge `721b73b` |

Both merges were conflict-free. Source branches remain frozen and clean. Original
dirty checkout and other owners' work were preserved. No push or issue closure.

### #35 owning acceptance

Independent review covered all eleven criteria: separate reasoned association
actions and immutable identity/evidence; valid backdating; ownership/placement
cardinality; canonical operational links; historical nondisclosure; append-only
factual corrections and distinct periods; reviewed non-destructive upgrade;
concurrency/archive invariants; independent current grants and atomic audit;
preserved source/history without nonprotected auto-grants; actual database,
HTTP/API, browser, fault and race checks.

The original five reviewer probes passed on both the frozen source and composed
integration: historical Invoice PDF disclosure, Job-only Asset-label disclosure,
association-history related-master filtering, compatibility editor reuse of
explicit links, and initial-observation correction without an invented transfer.
Only their evidence-output root was relocated through an external Go overlay;
all original assertions and immutable failure evidence were preserved. Both
`076_asset_associations` and `076_job_dispatch` migration identities remain,
with additive `077_asset_association_repairs`. Reviewed upgrade tests passed.
Service Entry/Lease downstream contracts retain their owners.

### #37 rejection evidence

Two real persisted Chromium probes confirmed owning AC5/AC7 failures:

1. A page opened at bare `/schedule` restores Alpha filters from its cookie.
   Another tab changes that cookie to Beta. Refresh on the first page then shows
   Beta calendar/results under unchanged Alpha date, filters and list controls.
   Explicit-query refresh is a positive control; assignment revocation still
   removes inaccessible links. This is presentation-state mismatch, not an
   authorization bypass.
2. A fresh schedule document loaded during an injected PostgreSQL query failure
   returns 503. After restoring the schema, Refresh returns 200 and displays
   cards, but calendar/dispatch handlers remain undefined and period links are
   absent. Real drag and month keyboard actions fail. Full navigation restores
   controls and opens correct dispatch proposals without persisting changes.

The initial combined probe completed F1 and calendar F2 before a dispatch fixture
timeout caused by its intentional prior assignment revocation. A separate
dispatch-only probe confirmed the remaining case. Schema restoration and
unchanged stored schedules were verified. Neither run altered frozen source.
Required repair: bind refresh to the rendered normalized query, and restore the
complete functional workspace after initial read failure, with browser regressions.

### #107 scoped acceptance and open gates

Accepted repairs are: interrupted fresh initialization provenance/checkpointing;
FreeBSD application-log ownership preservation/repair; rc.d status matching the
daemon supervisor PID; and detached supervisor standard descriptors. Changed
initialization data still requires reviewed legacy recovery. Historical databases
without provenance are not silently reclassified as fresh installations.

Actual native evidence for exact clean candidate `48c1999` was independently
reviewed: Debian 13 amd64 and FreeBSD 14.5 amd64, PostgreSQL/tools 16.15, native
static development builds, service stop/start/restart/status, interruption and
reboot recovery, HTTP bootstrap/recovery/persisted work, and reinstall/config
preservation. These native runs were not redundantly rerun. They are **not
published-release proof** (`Version=dev`, `ExactRelease=false`).

#107 remains OPEN for published artifacts and independently published checksums;
the finite supported OS/architecture/PostgreSQL matrix and unsupported-major
checks; broader browser/device coverage; and applicable final topology, egress,
upgrade, native backup/scale and broader acceptance gates.

## Fresh composed verification

All commands ran under the shared heavy-test lock and `appheavy.slice`, with
`GOMAXPROCS=2`, `GOFLAGS=-p=2`, explicit fresh PostgreSQL 18.6 application and
backup-admin URLs, and uncached tests (`-count=1`). Full execution was
2026-10-14 13:10–13:39 UTC.

| Gate | Result |
|---|---|
| `make lint` (`go vet ./...`) | PASS |
| `make test`, retaining `go test -v -race ./...`, 45-minute timeout wrapper | PASS: 845 top-level passes, 46 explicit skips |
| Original five #35 probes on integration overlay | 5 PASS, no skips |
| Asset/dispatch owning, browser, upgrade, authority/audit/race/PDF checks | 36 PASS, no skips |
| Installation/host-recovery production executable checks and browser | 8 PASS, 1 unsupported-server skip |
| Initialization interruption/changed-data/reviewed upgrade checks | 5 PASS, no skips |
| Dedicated persisted Chromium recovery journey | PASS |

Four browser skips from the full suite were subsequently executed successfully:
Asset associations, Job dispatch, installation/core recovery and dedicated
recovery Chromium. Remaining skips include the unavailable PostgreSQL <16
negative-server fixture, standalone subprocess helpers, other opt-in browsers,
accessibility and backup browser/Nginx scale checks. Skips are not passes.

Before composition, fresh #35 owning browsers initially failed because the
Chromium Unix socket path was too long. A short durable temporary-directory
alias fixed the runner environment; only those failed browser runs were retried.
Initial logs remain preserved. No product fix was needed.

## Durable evidence and database disposition

Repository-local state root: `.agent-state/v1-spec-18/` (not committed).

- `context/merge-ready-review-{35,37,107}.md`: full independent source/spec reviews.
- `context/merge-ready-focused-result.md`: frozen-pin executions and original hashes.
- `context/merge-ready-37-probe-result.md`: rejection reproduction and positive controls.
- `context/merge-ready-composed-result.md`: complete composed commands and skip inventory.
- `context/merge-ready-35-37-107-result.md`: final clean HEAD and acceptance pointer.
- `merge-ready-35-37-107/composed/`: exact argv/environment/exits, complete logs,
  `summary.json`, SQL/PDF/browser artifacts and `evidence.sha256`.
- `merge-ready-35-37-107/review37/`: immutable rejection probes and screenshots.
- `native-vms/evidence/repair-linux-20261014T062218Z/` and
  `native-vms/evidence/repair-freebsd-20261014T070316Z/`: native candidate evidence.

Owned cluster: port 40893, system identifier `7696505834135488665`, PGDATA beneath
`merge-ready-35-37-107/pgdata`. UID/path/PID/live-directory ownership was verified
before shutdown; control state is **shut down**, and its PID file is absent.
Database, backup extension fixture, logs and recovery evidence remain durable.
No other cluster was stopped. Source/evidence preservation hashes were verified.
