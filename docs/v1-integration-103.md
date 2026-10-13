# Ticket #103 integration acceptance

Accepted the protected administrative audit slice from source
`434a2c32f17d87894c3fa537872f8ebec848a655` on integration base
`23da4af7a3f3242d7a30188723449e048c2b2da2`, with the recorded-Role filter repair
below. Both worktrees were clean before composition; the base is an ancestor of
the pinned source. No other ticket tip was merged.

## Independent finding and repair

Role creation/storage permits names longer than 200 bytes, but audit filters
rejected them. Real authenticated HTTP reproduced 400 responses for browsing
and export review with 201-byte ASCII and multibyte event-time names after a
Role rename. The repair removes only the incompatible Role-name cap, preserving
exact recorded-name SQL matching and current sealed authorization.

`TestProtectedAuditLongRecordedRoleFilterHTTP` verifies 26 real customer-create
events, exact audit/target identities and numeric order, 25+1 browsing with
preserved filters, historical versus renamed Role evidence, and complete CSV
with count/as-of independent of a submitted browse cursor. Its initial expected
query sorted the text-cast output alias; qualifying the numeric source column
corrected that test oracle. Both the genuine red and oracle failure are retained.

## Owning acceptance criteria

| Criterion | Verified evidence |
|---|---|
| Sealed View/Export, no grant/name/API bypass | Protected-identity and independent sealed checks; `TestProtectedAuditSealedDenialsAndAuthorityLossHTTP`. |
| Event-time actor/Role/Permission/scope/target/time/channel/outcome; system/API facts | Insertion-time UUID/name/device snapshots, closed projection, known session/operation/occurrence evidence; event-role, API and worker export tests. |
| Recorded filters, not current authority/history substitution | Exact recorded-column predicates; renamed-role tests and merger long-name regression. |
| Durable access accounting; ordinary browse/search and denial distinction | Atomic browse/generation/download evidence; failure rollback, ordinary-navigation and actual security-log tests. |
| Confirmed fixed-column complete consistent export | Documented columns and review; 260-row snapshot/concurrent insertion/replay tests; persisted browser CSV transfer. |
| Durable states/restart/requester/24h/current whole-artifact authority | Failure and real worker subprocess recovery; ownership, expiry and authority-loss redaction tests. |
| No secrets/raw content/removed-text leakage | Closed projection and typed historical metadata tests; retained Comment text retains sealed-plus-parent checks, passed in full suite. |
| Immutable Role/restore/log history, no identity impersonation/erase | UPDATE/DELETE/TRUNCATE rejection; real encrypted restore with numeric identity collisions, exact artifact preservation, old-session rejection and log rotation. |
| Current authority and atomic effects/audit | Fence-before-snapshot implementation; waiting revocation, generation/storage/download fault and concurrent replay tests. |
| Non-destructive migration and honest unknown history | Old fields/grants retained, historical new fields NULL; explicit metadata only, no relative timestamp invention or unreviewed cutover. |
| Real DB/HTTP/API/browser/concurrency/upgrade and mandatory checks | Fresh runs below passed. |

## Actual checks

Durable evidence root:
`/home/adam/repos/freefsm/.agent-state/v1-spec-18/merge-103/`.
`run.sh` contains exact commands, environment and the shared blocking lock.
All heavy commands held
`flock /home/adam/repos/freefsm/.agent-state/v1-spec-18/heavy-tests.lock` with
`GOMAXPROCS=2`, `GOFLAGS=-p=2`, durable short `TMPDIR=.../m103`, and
`GOTMPDIR=.../merge-103/go`.

Fresh owned PostgreSQL 18.6 was initialized at `merge-103/pg` on verified-free
port **55703**. Both database variables were explicitly set:

```sh
FREEFSM_TEST_DATABASE_URL='postgres://freefsm@127.0.0.1:55703/freefsm_test?sslmode=disable'
FREEFSM_BACKUP_TEST_ADMIN_URL="$FREEFSM_TEST_DATABASE_URL"
```

- Focused regression: expected red in `logs/red.log`, test-oracle failure in
  `logs/green.log`, final PASS in `logs/green-final.log`.
- `make templ`: regenerated successfully, zero updates.
- `BROWSER_TESTS=1 go test -v ./internal/handlers -run '^TestProtectedAudit' -count=1`:
  PASS, 14 top-level tests, `logs/acceptance.log`. Real 320px/desktop Chromium
  used `/usr/bin/chromium-browser`; `audit-browser.png` inspected. Standalone
  subprocess helper SKIP is explicit; its parent actually verifies the subprocess.
- `make lint`: PASS (`LINT_EXIT=0`).
- `FREEFSM_TEST_DATABASE_URL="$FREEFSM_TEST_DATABASE_URL" GOFLAGS='-p=2 -timeout=30m' make test`:
  PASS (`TEST_EXIT=0`), full `go test -v -race ./...`, backup enabled.
  `logs/quality.log`: backup 659.178s, database 84.262s, handlers 741.907s.
- Applicable composition browsers: Customer export, Comments, Roles/directory
  all PASS with `BROWSER_TESTS=1`; `logs/composition.log`. The log retains
  navigation-related context-cancelled messages without concealing them.
- `git diff --check`: PASS.

The full suite leaves opt-in browser/scale tests, unsupported-server-major
fixture and standalone subprocess probes explicitly skipped. Those are not
claimed as passes. Relevant audit/composition browsers ran separately above.
No native/release/1GB or full browser-matrix acceptance is claimed by this slice.

The documented empty backup extension fixture is newly retained under this
owned root; only this cluster's extension search path was configured. Verified
data directory, port, PID and absence of application sessions, then cleanly
stopped the cluster. Recovery data/logs remain durable; archived and host
clusters were not used. See `logs/database-setup.log` and
`logs/database-shutdown.log`.

No remaining owning-ticket blockers were found. Existing deferred domain and
release contracts remain with their owning tickets. No final whole-spec review
was performed; issue verification/closure stays with the coordinator.
