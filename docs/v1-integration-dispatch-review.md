# Spec #18: dispatch integration and eight-ticket acceptance review

## Accepted merge

Ticket **#36**, source `0659db4134ab765aa6b5df122111204e3ba37dca`, is accepted for its owning dispatch slice on integration base `0bede469d802e4966b39f3db27fc2e830b569508`. The merge was conflict-free. Template regeneration produced no additional differences. No production repair was needed.

The independently reviewed criteria cover the shared interval/arrival window, explicit DST ambiguity, equal independent assignees, disabled/incompatible-user handling, independent schedule/assignment grants, reviewed drag proposals, current overlap acknowledgment, local due dates, non-destructive reviewed legacy cutover, commit-time authority and atomic immutable audit.

## Withheld candidates

All source worktrees were clean, matched their result pins, and contained the integration base. These seven tips were **not merged or accepted**:

| Ticket | Pinned source | Independently reproduced blocker |
|---|---|---|
| #39 | `e4699a3965321cdc45862ba578d6db5194bf0450` | Identifiable invalid Notes/Status requests have no terminal receipt; corrected intent under the same UUID succeeds and persists an effect. |
| #40 | `d587f0698d933633c5214dd556c28e814dc2c85d` | Empty Job detail hides checklist/history navigation from an independently authorized maintainer; invalid completion similarly leaves its UUID unbound. |
| #41 | `f62df3fccb932c72417e611861fb94720f88d2fe` | Occurrence-bearing Custom Field API commands reject the occurrence property, preventing required occurrence/acceptance audit attribution. |
| #44 | `a7b48ff96fee8ac84d8572d747fdc9997342a19a` | Job detail/list pricing emits JSON numbers instead of API-04 exact two-decimal money strings. |
| #47 | `c70314d82d593c3c0320c6837f7205bf46e4d658` | The same UUID accepts both a field mutation and commercial creation, in either order, for both document types. |
| #85 | `b9fde5c11272a0c414a2899e99f07129e56bb97a` | The same UUID accepts both a field mutation and transfer start, in either order. |
| #102 | `de54b176aac3f0af1e4d7a482c17ecdf15be9e58` | Separate Estimate/Invoice Custom Field saves omit per-record/cross-record/export history; injected history failure does not prevent the save. Job controls pass. |

Additional source-review gates remain: #40 double transport failure has no usable original-operation reconciliation control; operation-type binding must cover all mutation families; #85 needs nonsensitive terminal reconciliation after parent loss; and #47 reduced commercial audit/action shapes do not compose with #102's tenant-dependent activity trigger. The latter composed candidate was not constructed and is not presented as a dynamic reproduction.

The suspected same-count archived Item recapture issue in #47 was downgraded to an unresolved representation/design concern. Allowed snapshot editing cannot presently be distinguished from same-count replacement. The unambiguous committed-removal/archived-recapture regression **passed for both types**; it is not a rejection finding.

## Fresh checks

Executed in the integration worktree, under blocking
`/home/adam/repos/freefsm/.agent-state/v1-spec-18/heavy-tests.lock`:

```sh
export GOMAXPROCS=2 GOFLAGS=-p=2
export TMPDIR=/home/adam/repos/freefsm/.agent-state/v1-spec-18/m3639
export GOTMPDIR="$TMPDIR"
export FREEFSM_TEST_DATABASE_URL='postgres://adam@127.0.0.1:55736/merger_test?sslmode=disable'
export FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://adam@127.0.0.1:55736/postgres?sslmode=disable'
export BROWSER_EXECUTABLE=/usr/bin/chromium-browser
export NODE_PATH=/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules

make templ
make lint
BROWSER_TESTS=1 go test -race ./internal/handlers ./internal/database \
  -run '^(TestJobDispatch|TestGuardedStructuredJobConflict|TestTypedReviewCorrectionRetains|TestJobPlanningPersistedBrowser|TestJobRepairOfficeContactsBrowser)' \
  -count=1 -v -timeout=20m
make test GO='bash /home/adam/repos/freefsm/.agent-state/v1-spec-18/merge-36-39-40-41-44-47-85-102/go-check.sh'
```

All passed, **2026-10-14 04:14:04–04:34:07 UTC**. The wrapper adds only a 45-minute package timeout, retaining Makefile `go test -v -race ./...`. Fresh owning acceptance: handlers 39.966s, database 2.292s. Full suite: handlers 892.814s, backup 677.285s, database 122.283s, authorization 34.886s. Unchanged small packages show ordinary cache reuse.

Actual persisted Chromium journeys passed for dispatch, minimal Job planning and office Contact reading. Dispatch covers drag/cancel, full interval/equal assignees, overlap acknowledgment, stale comparison, retained proposal, lost-response reconciliation, validation, keyboard dismissal and 320px rendering. Authenticated HTTP verifies fenced authority loss, audit-failure rollback, concurrent overlap/replay and API planning exclusion. Upgrade verifies explicit review and original-source preservation.

The full suite recorded **45 top-level skips**: unrelated opt-in browsers/native/scale cases and subprocess-only entry points. They are not passes. The three applicable browsers were enabled separately. No native/release/full-browser-matrix/1GB certification is claimed.

After the integration suite, unchanged pinned source branches were tested with durable Go overlays under the same lock and owned database, **04:34:07–04:36:22 UTC**. All seven blocker probe groups failed expected compliant assertions; every probe compiled and exercised production authenticated HTTP with migrated PostgreSQL. Cascading assertions after the first unexpected effect are not additional independent defects. The #47 archived-recapture and #102 Job-history positive controls passed.

## Durable evidence and boundaries

Evidence root:
`/home/adam/repos/freefsm/.agent-state/v1-spec-18/merge-36-39-40-41-44-47-85-102/`.

- `check.sh`, `go-check.sh`, `runner.log`, `generation.log`, `lint.log`, `acceptance.log`, `full-test.log`, `dispatch-phone.png`.
- `probes.py`, `probe-results.json`, `probe-runner.log`, `probe-{39,40,41,44,47,85,102}.log`, immutable prepared probe sources and per-branch overlays.
- Complete owning-criterion matrices, source/evidence references and final exact integration HEAD are in sibling `context/merge-36-39-40-41-44-47-85-102-{review-a,review-b,result,progress}.md` files.
- Fresh PostgreSQL 18.6 UTF8 cluster: `pg/`, port **55736**, system identifier **7696367845124030278**. Documented backup fixture configured only for this cluster. Database files and ownership/shutdown evidence are retained.

Deferred Lease/Delivery/timeline/cascade/retry contracts remain mandatory with their owning slices. No fallback authorization, invented historical mapping, source-branch mutation, push, issue closure or final whole-spec review was performed. Seven withheld candidates require repair and independent verification before acceptance.
