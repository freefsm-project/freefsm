# #34 verification evidence

Base: `6fb21dfc3587025d33d02fcb72757f7f57f5502f` on
`implement/v1-34`, worktree `/var/tmp/freefsm-v1-34`.

## Acceptance coverage

| Ticket criterion | Implemented and exercised evidence |
|---|---|
| Immutable unique Number/class; owner/placement cardinality | Migration 073 constraints, immutable/delete guards, production registration/Edit tests; unplaced Company-owned browser journey. |
| Active Type, optional descriptive Status, repeated serial, descriptive identity | Existing vocabulary selection locks retained; registration, optional Status clear, two browser registrations sharing a serial, manufacturer/model/dates/Service Notes persistence. |
| Eligible same-Customer Location; no ordinary reassociation | Direct cross-Customer input rejection, immutable Customer edit rejection, unplaced location constraint, waiting Location archive test. |
| Concurrent and archived Number uniqueness | Two concurrent production POSTs with one Number have one successful publication; archived Number reuse rejects; restore retains the original Number. |
| Retired definitions remain visible and retained edits succeed | Existing active/retired Status tests and browser journey; retained-global Type/Status regression covers show/list/Edit; foreign vocabulary is absent from new-registration choices. |
| Reviewed pre-v1 upgrade | Read-only preflight, mandatory attributed snapshot-bound resolutions, no inferred class/start date; missing review blocks before other migrations; stale/duplicate/changed-known resolutions reject; archived identity, notes, Customer and Job references survive upgrade/repeated startup migration. |
| Independent master/context authority and closed fields | Ent master-read boundary and independent picker View; explicit API field-key allowlist; Customer viewer with Asset C cannot enumerate sibling equipment; arbitrary API Asset master routes absent; assignment loss returns 404. |
| Racing dependencies; upgrade/restart | Waiting Customer/Location archive and Type retirement reject Asset publication with no success audit; real subprocess HTTP edit/read across two fresh processes retains Number/class/notes; persisted browser reads. |
| Commit-time authority and atomic immutable audit | Shared authorization fence, Asset create/edit before-after audit, injected audit rollback, waiting edit revocation regression, independent nonprotected Archive/Restore grants. |
| Preservation and no automatic ordinary-Role grants | Additive migration; explicit reviewed identities and unchanged references; no grant insertion or Role-name inference. Customer cascade preserves class distinction and restores only the Customer until separate Asset Restore. |
| Required verification | Generation, lint, full uncached race-enabled DB suite with backup admin, focused DB/HTTP/API/restart tests, four persisted Chromium journeys, application/preflight executable builds. |

Transfer/Place/Return/effective timelines remain #35. Service Entries and Lease
coverage remain their owning slices. The bounded context contains actual current
equipment fields; it does not fabricate Service Entries, coverage, or historical
association dates. No whole-v1, physical-device, cross-browser or native reboot
certification is claimed.

## Storage and reproducible commands

All disposable database/build/test output belongs to this ticket under
`/var/tmp/freefsm-v1-34-output`. PostgreSQL was initialized with UTF8, trust auth,
and the dedicated port **55434**. Database: `freefsm_v1_ticket_34`; backup admin
database: `postgres`. No shared PostgreSQL cluster was restarted or reconfigured.
The owned cluster's `extension_control_path` points to this worktree's
`internal/backup/testdata`, making the repository fixture extension available.

Common build settings:

```sh
export TMPDIR=/var/tmp/freefsm-v1-34-output/tmp
export GOTMPDIR="$TMPDIR"
export GOMAXPROCS=2
export GOFLAGS=-p=2
export FREEFSM_TEST_DATABASE_URL='postgresql://adam@127.0.0.1:55434/freefsm_v1_ticket_34?sslmode=disable'
```

Passed:

```sh
make generate
make lint
make BUILD_DIR=/var/tmp/freefsm-v1-34-output/dist compile
CGO_ENABLED=0 go build -o /var/tmp/freefsm-v1-34-output/dist/freefsm-asset-preflight ./cmd/freefsm-asset-preflight
git diff --check
```

The final full suite passed, with explicit DB and backup integration enabled:

```sh
GOFLAGS='-p=1 -count=1 -timeout=45m' \
FREEFSM_BACKUP_TEST_ADMIN_URL='postgresql://adam@127.0.0.1:55434/postgres?sslmode=disable' \
BROWSER_TESTS=0 MAKEFLAGS='BUILD_DIR=/var/tmp/freefsm-v1-34-output/dist' \
make BUILD_DIR=/var/tmp/freefsm-v1-34-output/dist test
```

Log: `full-final.log`. All tested packages passed; this invokes `go test -v -race
./...`. Notable package durations: backup 385.122s, handlers 530.505s, database
50.586s, services 33.438s. Earlier `full-first.log` timed out after reporting
fixture-review/seed/test-extension failures; `full-second.log` failed one obsolete
ordinary Asset-reassignment assertion. Those attempts are not passes. The final
run includes explicit fixture reviews, the corrected assertion that Edit cannot
transfer an Asset, and the configured fixture extension.

Focused acceptance, also passed with `-race`:

```sh
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
NODE_PATH=/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules \
go test -race ./internal/handlers ./internal/database \
  -run '^TestAsset|^TestTypedFieldsPersistedBrowser$|^TestVocabularyPersistedBrowser$' \
  -count=1 -timeout=10m -v
```

Log: `asset-verified.log`. Chromium **154.0.8037.97**. Selected persisted browser
journeys: Asset identity, Asset Status clear/retirement, typed-field inline Asset
creation, and vocabulary management. The standalone subprocess helper skips when
not launched by its owning test; `TestAssetIdentityRestartHTTP` ran both children.

The full suite deliberately skipped browser opt-ins; the four journeys above ran
separately. Other browser/accessibility/device/1-GiB nginx scale opt-ins were not
run for this ticket. PostgreSQL-under-16 rejection is skipped on the PostgreSQL 18
cluster. Standalone authorization/backup crash helper entry points skip outside
their parent subprocess runs. Backup-admin integration was enabled, not skipped.

Red/green evidence includes `identity-red.log` (unplaced creation rejected),
`context-red.log` (missing operational Asset projection),
`placement-red-browser.log` (placed archive and shifted date),
`global-list-red.log` (lost retained vocabulary), `global-foreign-red.log`
(foreign choices), and `selection-leak-red2.log` (sibling equipment enumeration).
The final focused/full logs supersede those demonstrated failures.
