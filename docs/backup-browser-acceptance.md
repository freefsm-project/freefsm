# Issue 17: Chromium/nginx scale acceptance

## Status: passed — Chromium/nginx integration acceptance

After the UI owner corrected the backup/restore completion contract and
regenerated the template, the 2026-10-05 rerun **passed the full >=1 GiB exercise**:
native Chromium download, raw browser upload, authenticated archive review,
restore, restored-account login, disabled-email response, restored-file hashes,
session invalidation, and staging/recovery cleanup. The email-disabled gate also
survived reopening instance control after shutdown/restart recovery.

This result is **acceptance integration harness validation, not validation of a
genuine release artifact**. Matching release identities are injected only into
test composition. See the measurement boundaries below.

### Earlier blocker (resolved by UI owner)

On 2026-10-05, the new real-browser harness reached encrypted backup completion
with **1,073,741,824 bytes (1 GiB) of incompressible attachments**, through real
nginx, but the production UI displayed **“Restore complete. Email is disabled…”**
instead of exposing the download form. That earlier attempt did not pass:
download, upload, review, restore, and their subsequent assertions were not
reached. Its historical measurements are retained separately below.

Coordinator/UI finding:

- `internal/backup/backup.go` finishes backup jobs as `Kind: "backup", Phase:
  "complete"`; `OpenDownload` requires that completed state.
- `internal/templates/backup.templ`, `follow`, interprets every `complete` as
  successful restore and only reveals the download form for `ready`.
- The existing mocked browser fixture supplies `backup:ready`, so it does not
  reproduce the real engine contract.
- The owning UI work reconciled this contract and regenerated the template. The
  acceptance harness does not rewrite status responses, template code, or hidden
  form state.

## Scope and composition

Files:

- `internal/handlers/backup_scale_test.go` (Linux-only, opt-in).
- `internal/handlers/testdata/browser/backup-scale.cjs`.

This is an **acceptance integration harness, not a genuine release artifact**.
Both managers receive matching `v1.2.3` / `abcdef1234567` identities exclusively
in test composition. The later issue #17 policy revision enables development
builds in production composition: any restore involving development requires
matching trusted-local schemas, while release-to-release still requires the exact
version and commit. `TestBackupHTTPRoundTrip` covers development, differing dirty
identities, both development/release directions, and rejection before destination
modification for schema and release mismatches. This scale run used release test
identities; it does not represent a rerun of the 1 GB acceptance with development.

It composes the real backup managers, instance control, migrated PostgreSQL
databases, sessions, administrator authorization, bcrypt reauthentication, CSRF,
backup handlers, rendered template/inline JS, and static JS. Chromium performs
native form download, file-backed raw `File` upload, real capability polling,
archive review, and restore confirmation. There are no mocked network routes or
Go/JavaScript buffers containing the complete archive. Initial login sessions
are provisioned by the test's real session service.
Normal application routes are additionally composed with real maintenance,
connection-refresh and CSRF middleware, so the restored account logs in through
the real login form and visits Settings in Chromium. An authenticated browser
POST to the real test-email endpoint must return `503` with a disabled message.
This checks the email gate and persistent notice; it does not start background
delivery workers or prove every email flow against a fake SMTP transport.

The harness starts two isolated rootless Podman nginx containers on ephemeral
loopback ports. It extracts the **entire** `location ^~ /settings/backup` from
`deploy/linux/freefsm.nginx.conf`, inserts it byte-for-byte, verifies its presence,
and records its SHA-256. Only the outer HTTP-only listener and upstream address
are fixture-specific; this does not exercise TLS termination. Application
listeners use `httptest.Server`; this is not the production executable's startup
composition. The supported location's buffering, body limits, headers, and
transfer timeouts are unmodified.

The default source has 64 database-linked customer attachments, each 16 MiB of
`crypto/rand` bytes. The destination has distinct users/settings, a marker, and
128 MiB of old incompressible file content for the pre-restore recovery copy.
Application database roles are distinct `NOSUPERUSER NOCREATEDB NOCREATEROLE
NOREPLICATION` roles; provisioning privileges belong only to the disposable
cluster fixture. The workstation database is not used.

On a completed run, assertions require:

- Native download and raw upload byte counts equal, each at least the requested
  logical attachment size; browser download size and upload Content-Length agree.
- Authenticated source, capture date, version/commit review and exact destination
  confirmation; actual status polling reaches `upload:ready` and
  `restore:complete`.
- Every restored attachment's SHA-256 matches, destination-only files disappear,
  and restored database file paths point at the destination upload root.
- Source settings and database-backed SMTP password replace destination values;
  outbound email is disabled and old source/destination web/mobile sessions fail.
- A source account can log in after restore, Settings displays the email-disabled
  notice, and test email receives a truthful `503` disabled response. The gate
  remains disabled when instance control is reopened after restart cleanup.
- Restore staging/recovery directories and journal are removed. Retained source
  download material is removed by real shutdown/restart recovery, rather than
  changing the documented one-hour download TTL.

Financial/conversion/delivery history is covered by the separate historical
workflow suite; this harness targets the scale/browser/proxy criterion.

## Reproduce

Requirements: Linux, Go, native PostgreSQL tools matching the server, rootless
Podman, Chromium, Node, and `playwright-core` 1.58.2. The existing UI dependency
installation can be reused via `NODE_PATH` below. On a fresh checkout, use
`npm ci --prefix internal/templates/testdata/browser` first.

Provision a **new** disposable cluster (choose an unused port and new directory
if these names already exist). All connections below are password-free trust
connections confined to the new loopback-only test cluster:

```sh
ls /tmp/opencode
mkdir /tmp/opencode/issue17-browser-pg /tmp/opencode/issue17-browser-tools
initdb -D /tmp/opencode/issue17-browser-pg -U acceptance_admin -A trust --no-locale -E UTF8
pg_ctl -D /tmp/opencode/issue17-browser-pg \
  -l /tmp/opencode/issue17-browser-pg.log \
  -o '-h 127.0.0.1 -p 55447 -k /tmp/opencode/issue17-browser-pg' start

FREEFSM_BACKUP_BROWSER_SCALE=1 \
FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://acceptance_admin@127.0.0.1:55447/postgres?sslmode=disable' \
FREEFSM_BACKUP_BROWSER_REPORT=/tmp/opencode/issue17-browser-tools/acceptance.json \
NODE_PATH="$PWD/internal/templates/testdata/browser/node_modules" \
go test ./internal/handlers -run '^TestBackupBrowserNginxScale$' -count=1 -v -timeout 30m

pg_ctl -D /tmp/opencode/issue17-browser-pg stop -m fast
```

`BROWSER_EXECUTABLE` defaults to `/usr/bin/chromium-browser`.
`FREEFSM_BACKUP_NGINX_IMAGE` defaults to `docker.io/library/nginx:alpine`; an image
digest can be supplied for reproducibility. Containers and application
databases/roles are removed by test cleanup; the cluster remains operator-owned
until stopped explicitly. Successful browser JSON and server measurements are
included in the report. Normal `go test` skips this opt-in expensive exercise.

`FREEFSM_BACKUP_BROWSER_BYTES=16777216` runs a 16 MiB smoke test, **not scale
acceptance**. `ScaleTargetMet` is only true after a successful >=1 GiB run.
`TMPDIR` may point at disk-backed scratch. The harness requires at least eight
times the logical dataset plus 1 GiB free before starting; the observed `/tmp`
was a 16 GiB tmpfs with enough headroom. Do not run concurrent large scale jobs
against that tmpfs. The guard checks filesystem capacity, not a guarantee of
system-wide available memory.

## Measurements and their limits

The sampler runs every 100 ms from immediately before Chromium launch until the
browser exits. App RSS is the Go handler/engine test process; app-plus-children
adds direct native `pg_dump`/`pg_restore` children. Shared RSS pages may be counted
twice. This includes Go test overhead and both source/destination managers.
Browser, Node, nginx and PostgreSQL server RSS are **not** included in app memory.
Brief peaks shorter than the sampling interval may be missed. The >=1 GiB test
asserts a fixed **768 MiB app-plus-tool RSS ceiling**, independent of archive size.
Use the documented non-race command for memory acceptance: Go's race detector
adds substantial shadow-memory overhead. A separate 16 MiB `-race` smoke run
reproduced the same UI blocker without a race report; its memory is not comparable
to the non-race acceptance figures.

Scratch storage records both logical bytes and allocated blocks under the test
root: source/destination uploads, server archives/staging/recovery, Chromium's
download, and fixture metadata. It excludes PostgreSQL cluster/WAL, browser
profile directories, container layers, and build cache. This is a measured
scratch high-water estimate, not a whole-machine storage requirement. Post-restore
and post-restart values describe server artifact lifecycle; browser shutdown may
also remove native temporary downloads. In the passing run the explicit download
directory retained the browser archive until final test-directory cleanup;
post-restart storage therefore includes source uploads, restored destination
uploads, and that browser archive. The temporary test root is removed on test
exit.

### Passing 1 GiB run, 2026-10-05

| Measurement | Value |
|---|---:|
| Source logical attachment bytes (64 × 16 MiB) | 1,073,741,824 |
| Encrypted native browser download | 1,074,319,606 bytes |
| Raw browser upload received by destination | 1,074,319,606 bytes |
| Create/poll until backup available | 15.389 seconds |
| Native download | 3.415 seconds |
| Raw upload request | 1.296 seconds |
| Upload plus validation/review | 10.629 seconds |
| Restore through completion polling | 12.383 seconds |
| Browser workflow including login/email check | 42.321 seconds |
| Measured process workflow including browser launch/close | 43.454 seconds |
| Entire Go test including setup, file assertions and cleanup | 53.03 seconds |
| Real status polls | 24 |
| Sampled app peak RSS | 572,542,976 bytes |
| Sampled app plus native PG children peak RSS | 572,542,976 bytes |
| Peak scratch logical bytes | 6,562,486,230 |
| Peak scratch allocated bytes | 6,562,553,856 |
| Scratch logical bytes after restore | 4,296,127,451 |
| Scratch logical bytes after restart cleanup | 3,221,807,845 |
| Restored account login / email-disabled notice | Passed / passed |
| Test email | HTTP 503, disabled message |
| All restored attachment hashes and DB file references | Passed |
| Old source/destination web/mobile sessions invalidated | Passed |
| Restore staging/recovery and restart download cleanup | Passed |
| Reopened instance-control email gate | Disabled |

The report contains `Passed: true` and `ScaleTargetMet: true`. The observed
app-plus-tool maximum is about 546 MiB, below the fixed 768 MiB acceptance ceiling.
The maximum coincided with application-only memory; adding sampled PostgreSQL
tool RSS did not produce a higher maximum. This demonstrates bounded memory for
the exercised dataset, not an unlimited-size guarantee.

The exact successful invocation was the reproduction command above, with no
`FREEFSM_BACKUP_BROWSER_BYTES` override. JSON was written to
`/tmp/opencode/issue17-browser-tools/acceptance.json`. The supported nginx location
hash and software versions below also apply to this passing run. Application
roles/databases and nginx containers were cleaned up, and the disposable
PostgreSQL cluster was stopped after checks.

### Observed blocked 1 GiB run, 2026-10-05

| Measurement | Value |
|---|---:|
| Source logical attachment bytes | 1,073,741,824 |
| Downloaded/uploaded bytes | **0 / 0 (blocked)** |
| Browser workflow until failure | 16.523 seconds |
| Entire Go test including fixture setup/cleanup | 23.37 seconds |
| Sampled app peak RSS | 485,445,632 bytes |
| Sampled app plus native PG children peak RSS | 485,445,632 bytes |
| Peak scratch logical bytes | 3,339,655,105 |
| Peak scratch allocated bytes | 3,339,685,888 |
| Restore/cleanup assertions | **Not reached** |

Environment: Chromium `154.0.8037.57`, nginx `1.31.3`, PostgreSQL `18.6`.
Official nginx image digest:
`sha256:1d40e3eb3bf4f138de1d67193f2aa5309fcaf343eb5ffadbf5e9439de1eb1ebb`.
Deployment location SHA-256:
`11c994832c78f1330be10990ddb03259d7f40b9a4ef497f3ab5009b729c353b8`.

Quality checks: `make lint` passed. `make test` passed with
`FREEFSM_TEST_DATABASE_URL=postgres://acceptance_suite@127.0.0.1:55447/acceptance_suite?sslmode=disable`
and the disposable backup admin URL above; the suite role was separately created
without superuser/CREATEDB/CREATEROLE. The first full-suite command hit the runner's
120-second timeout; rerunning with a 600-second runner timeout completed. The
browser scale and template-browser tests remain opt-in and were skipped by that
standard suite. The initial explicit scale test failed as recorded above; the
post-UI-fix full-size rerun passed as recorded in the passing-run table.
