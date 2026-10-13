# Project contract — #32

## Domain and authorization

Projects retain an immutable Customer identity. Creation requires current Project
Create, Project View and independently authorized active Customer selection; a
selected Location additionally requires Location View and active same-Customer
ownership. Editing an existing Project does not imply Customer or Location View.
Unchanged historical links remain available for repair without revealing a hidden
master. Detail writes use the existing `DetailsTransaction`, including the shared
authorization fence, record locks, Custom Field validation and protected audit.
Archive/Restore use their independent grants and the same committing boundary.

`completion_percentage` is the retained manual integer, never a cached Completed
display. Completed projects present 100 without writing that value; their edit
forms omit manual progress. A changed manual value submitted while Completed
rejects, including an editor that waited behind a concurrent status change. All
configured semantic statuses remain selectable without a transition graph.
Manual 100 is not completion or archive eligibility.

Archive requires Completed/Canceled and does not stop or inspect continuing Jobs.
Restore requires an active Customer and changes neither status nor historical
links. To reopen a finished Project with an archived Location, first use Edit to
clear or replace that Location, then select the desired unfinished status.
Project Location never changes a direct Job's independently selected Location.

Direct Jobs are permission-filtered before counting/pagination, ordered by title
and ID, with 25/50/100 rows and query-preserving page links. There is no hierarchy
or recursive document collection. The assigned Job master-context endpoint adds
an explicit Project allowlist: name, description, status, presented progress,
planned dates, linked Location title and Service Notes. Internal Notes, Custom
Fields, master collaboration and sibling Jobs are excluded. Assignment loss
removes that projection. Project maintenance is not added to the field API.

## Non-destructive local-date migration

Migration `073_project_contract.up.sql` adds DATE-typed `planned_start_date` and
`planned_end_date`, and separate `service_notes`. Original `start_time`, `end_time`,
progress, IDs and Customer links are not rewritten. Existing timestamps are
captured as calendar dates using the installation's configured timezone once;
the captured timezone and entire original Project row are retained in
`project_legacy_review`. Later timezone changes never rewrite calendar dates.
Optional dates are ordered; a present empty date clears it, omission retains it.
Overdue is read-derived after the target local date except Completed/Canceled,
and never changes status.

Legacy ownership uncertainty, unavailable timezone, unordered dates, noninteger
progress and Completed-with-recorded-100 are explicitly reported in the review
table. In particular, recorded Completed 100 is **not** treated as evidence of a
known prior manual value. Project detail pages expose historical review notices;
the retained recorded value is not replaced by a fabricated value or Job count.
Notices describe uncertainty at migration and are not automatically erased by
later edits. An operator can inventory unresolved historical evidence with:

```sql
SELECT project_id, captured_timezone, review_reasons, recorded
FROM project_legacy_review
WHERE cardinality(review_reasons) > 0
ORDER BY project_id;
```

No new permissions or nonprotected-Role grants are introduced. The database also
rejects direct changes to Project Customer ownership.

## Executable evidence

Tests in `internal/handlers/project_contract_integration_test.go` exercise the
production router against migrated PostgreSQL: minimal creation, immutable
Customer, bounds/fractions/malformed input, date clear/ordering/timezone stability,
semantic progress/restoration, manual-100 archive denial, independent grants,
cross-Customer Location denial, archived-Location repair, independent Job Location,
direct pagination, audit failure rollback, completion/progress contention,
Customer/Location archive and grant-revocation races, and closed web/API context
with assignment loss. The migration test preserves original JSON byte-for-byte
and checks honest unknown-progress reporting and local-date capture.

The persisted 390px Chromium journey creates and edits a Project through the
actual forms with sessions and CSRF, reloads its recorded state, and checks
immutable Customer, progress, planned dates, overdue, completion and reopening.

Verification uses the exclusively owned PostgreSQL 18 cluster on port 55432,
database `freefsm_v1_ticket_32`, with Go/temp/build output under
`/var/tmp/freefsm-v1-32`. Shared PostgreSQL was unavailable and was not restarted.

```sh
export TMPDIR=/var/tmp/freefsm-v1-32/tmp
export GOTMPDIR="$TMPDIR" GOMAXPROCS=2 GOFLAGS='-p=1 -timeout=45m'
export MAKEFLAGS='BUILD_DIR=/var/tmp/freefsm-v1-32/dist'
export FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55432/freefsm_v1_ticket_32?sslmode=disable'
export FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55432/postgres?sslmode=disable'
make generate
make lint
BROWSER_TESTS=0 make test
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
 NODE_PATH=/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules \
 go test -race ./internal/handlers ./internal/database -run '^TestProject' -count=1 -v
```

Full race-suite and focused browser/database runs passed. The full suite disables
browser opt-ins; the Project journey is separately enabled and passes without
selected skips. Other browser/accessibility journeys, the 1GiB nginx backup
scenario and the PostgreSQL <16 startup-rejection opt-in were not run for this
ticket. Subprocess helper entry points skip standalone and run through their
owning tests. Backup-admin integration is enabled and passed.

Earlier unsuccessful attempts are retained in the evidence logs: one full run
hit the shell's 20-minute limit after a backup crash-probe timeout and a missing
test-extension lookup; the owned cluster's lookup was corrected and both focused
tests passed. A subsequent complete run found two legacy service expectations
that allowed Project Customer changes. They now require rejection and unchanged
Customer/Location; the final full suite passed.
