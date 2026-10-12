# Native installation acceptance — #107

## Disposition

**Implementation and isolated Linux checks completed; native release acceptance
still blocked**, observed 2026-10-11. Issue #107 remains open. The current results
below supersede the explicitly historical initial checkpoint later in this file.

## Current implementation and TDD evidence

- CLI `-recover-admin EMAIL` accepts a password and matching confirmation on stdin,
  resets only an existing enabled Admin, enforces the saved password policy, forces
  browser password replacement, and revokes browser/mobile sessions and recovery
  links. Operation/maintenance locks drain in-flight requests before database
  locks recheck identity/policy. Password, credential revocation and host-attributed
  audit commit together; audit failure rolls back. No new user or product Role.
- Startup now rejects PostgreSQL <16 and absent/mismatched `pg_dump`/`pg_restore`
  before any migrations/bootstrap. Interrupted-restore recovery retains priority.
- FreeBSD live config is service-owned mode 0600; existing values survive reinstall,
  legacy modes tighten, and symlinks are rejected. Required state/upload/log roots
  have service ownership. The rc.d script installs in `/usr/local/etc/rc.d`; the
  old `/etc/rc.d` location must be reviewed/relocated to prevent duplicate services.
- Linux's former root-owned log directory conflicted with its service-user log
  file setting. The staged Linux installer now creates service-owned writable log,
  upload and state roots; `DESTDIR` suppresses systemd reload. Both platform
  Makefile install targets were exercised in temporary destination roots.
- `deploy/verify-postgres.sh` runs isolated container executable/DB compatibility
  regressions and backup HTTP round trips, without host ports or services.
- Backup HTTP regression now uses the production embedded migration filesystem,
  so its compiled runner works without a source checkout inside the target.

Red → green observations: missing-tool startup previously migrated and listened
until the test timeout; recovery initially failed with `flag provided but not
defined`; staged installer initially had no safe installer entry point. Each was
followed by its implementation and a passing test. Supplementary denial/failure/
concurrency tests then verified current policy, unknown/non-Admin/disabled targets,
password confirmation, audit rollback, concurrent Role change, competing-process
exclusion, old browser/mobile denial, and actual HTTP forced password replacement.

The native foreground executable also passed first Admin/company bootstrap and
persisted Customer/Job create/list workflows with SMTP absent and with SMTP/maps
pointing at an unreachable loopback endpoint. A real Chromium journey passed
recovered login → forced password replacement → persisted Customer creation →
Job creation → both lists. This new browser test uses the actual executable and
database, not intercepted engine responses or fabricated sessions.

### Coordination boundary with #19

Integration now recognizes only an enabled user assigned the protected Role in a
ready authorization installation. Recovery takes #19's transaction mutex before
locking authorization state, Roles, policy, users and credentials. There is no
legacy discriminator/name fallback or grant assignment. Pending cutover denies
host recovery; it does not manufacture a protected identity from legacy data.

Executable regression tests exercise real reviewed cutover: a former Dispatcher
becomes protected Administrator and is recoverable, while the former legacy
Admin assigned empty grants is denied. Concurrent protected-role demotion and
atomic denial/revocation tests pass. The persisted Chromium recovery journey now
uses that upgraded installation. See `docs/v1-integration-19-107.md` for integration
commands, catalog findings and remaining limitations. The earlier branch-only
results below retain their original provenance.

### Current commands/results

The native disposable PostgreSQL 18.6 cluster is on port **55507**. Its existing
empty test extension was enabled without touching host extension files:

```sql
-- Executed ONLY against the disposable cluster on 127.0.0.1:55507.
ALTER SYSTEM SET extension_control_path = '/tmp/opencode/freefsm-v1-107/internal/backup/testdata:$system';
SELECT pg_reload_conf();
```

`TestBackupCreateUnsupportedLayout`, including the extension case, then passed.
This resolves the initial environment failure recorded below.

```sh
make build
make lint
FREEFSM_TEST_DATABASE_URL='postgres://adam@127.0.0.1:55507/freefsm_test?sslmode=disable' \
FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://adam@127.0.0.1:55507/postgres?sslmode=disable' \
BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
NODE_PATH=/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules \
make test
```

**Passed**, including race-enabled real DB integration, existing backup suites,
both existing browser regressions, the new executable/HTTP/browser tests, and
staged platform installs. Local full log:
`/tmp/opencode/freefsm-v1-107-implementation-test.log`. Remaining skips in this
invocation: the <16 rejection test (run separately on 15 below), the intentional
crash-child helper (run by its parent tests), and the opt-in 1 GiB nginx scale
harness. Skipped scale is not a pass. Later staged-installer checks additionally
passed `go test -v ./deploy -count=1`; shell syntax and `git diff --check` passed.

Both install scripts were also run as container root with `postgres:postgres`
as a distinct disposable service identity. `stat` recorded Linux config
`600 root:root`, FreeBSD config `600 postgres:postgres`, both log roots
`750 postgres:postgres`, and both state roots `700 postgres:postgres`. This checks
real ownership separation in disposable Linux storage; it is not systemd/rc.d
execution or native FreeBSD proof.

```sh
sh deploy/verify-postgres.sh 16
sh deploy/verify-postgres.sh 17
sh deploy/verify-postgres.sh 15
```

| Disposable container | Actual result | Image digest |
| --- | --- | --- |
| PostgreSQL/tools 16.15 | Executable bootstrap, core workflows, recovery success/denial/failure/concurrency; backup history round trip 3.48s, six HTTP compatibility cases 17.94s: passed | `sha256:75adc2a65806c96bc0e59030230bfe3f1c1b0d6d8f8181a8913e2b8b53bdc166` |
| PostgreSQL/tools 17.11 | Same executable matrix; backup history round trip 3.49s, six HTTP compatibility cases 18.21s: passed | `sha256:3cec7eb015ba8adb28139fa5c83b8489cdf0e666e53dfdf20f598ae0cc8739e3` |
| PostgreSQL/tools 15.19 | Actionable unsupported-server failure with zero tables created: passed | `sha256:a5f9ead8ed7cb25abc36bea51fb9bb2be8d5579ed4ef3d28876027e438391ff0` |

The 16/17 runners intentionally skip the <16 negative case; the 15 runner runs
only that rejection case. No support for running the application on 15 is claimed.
Container test binaries are static, without the race detector; the native 18
suite above runs `go test -v -race ./...`. All containers use `--network none`
and temporary databases, with application roles NOSUPERUSER/NOCREATEDB.

Matrix executable SHA256:
`4185af83211bfbd16f52e81187bd6c152e6fa923c84cd1556572704f1936377c`.
Built by `make build` from `dda1f44` plus this implementation's working changes;
identity is **development**, not a published release. Backup HTTP tests retain
their explicitly injected compatibility identities. Local matrix logs are
`/tmp/opencode/freefsm-v1-107-pg16-matrix.log` and `...-pg17-matrix.log`.

Initial matrix attempts exposed two harness issues that were corrected and rerun:
private SELinux `:Z` relabeling of one shared executable collided between parallel
containers (now shared `:z` for that artifact); backup HTTP tests assumed on-disk
migrations (now production embedded migrations). Neither failed attempt counts as
acceptance. Native browser harness development also corrected an ambiguous button
selector and supplied the browser's real Origin header in HTTP form submissions.

### Current external acceptance gates

1. Published candidate tag/commit, target architecture list, actual Linux/FreeBSD
   artifacts and their independently published checksums are still needed.
2. Native service-managed install/start/stop/restart and full reboot remain unrun
   on both OSes. The dev host is authorized only for builds/tests and isolated
   instances, not workstation reboot or system-service mutation. Supply disposable
   systemd/FreeBSD rc.d targets with persistent storage and reboot access, or
   separately authorize a specific Linux service-install test. No native FreeBSD
   target or usable VM tooling was established, and no host packages were installed.
3. Release owner must declare the supported-major/OS matrix. Real native Linux
   PostgreSQL 18 tests and container 16/17 tests are useful evidence, not proof
   of native FreeBSD capability or every future major implied by “16+”.
4. Operator TLS/firewall/proxy boundary and zero default telemetry/update-check
   egress still need execution/capture in the actual deployment topology.
   Network-disabled containers do not prove absence of attempted egress.
5. Protected-identity recovery integration and its rerun are complete. The #19
   follow-up adds migration 057 and shared immutable host-attributed audit alongside
   ordinary activity; failure of either write rolls back recovery. See
   `docs/v1-integration-19-107.md` for evidence. Native upgrade/reboot data preservation
   and the scale gate remain unproven by these tests.

## Initial checkpoint (historical)

The remainder records the initial investigation and its then-missing proof.
Implementation/fixture/seam blockers resolved above are not current blockers.

Worktree: `/tmp/opencode/freefsm-v1-107`, branch `implement/v1-107`.
Starting source: `580e95ab9c3eed179ee0a5164c3e144126b15caf`.
`git merge-base --is-ancestor integration/v1-spec-18 HEAD` succeeded before work;
both references initially pointed at that commit. The original working checkout
was not edited. No host services, production database, or workstation reboot were
used. All SQL execution targeted the new disposable cluster on port **55507**.

Normative requirements: #18 OPS-01, OPS-02, ACC-03, TEST-05, TEST-06 and #107's
acceptance criteria. Existing backup recovery is retained; authorization changes
belong to the concurrent #19 implementation.

## Environment and artifact identity

| Item | Observation |
| --- | --- |
| Native host | Fedora Linux x86_64, kernel `7.2.9-200.fc44.x86_64` |
| Go | `go1.26.9-X:nodwarf5 linux/amd64` |
| Native database/tools | PostgreSQL, `pg_dump`, `pg_restore` 18.6 |
| Disposable DB | `/tmp/opencode/freefsm-v1-107-pg`; loopback only; private Unix socket directory; trust auth; test database UTF8 |
| Source build | `make build` completed ent/templ generation and compilation; no tracked generated changes |
| Linux executable | `dist/freefsm`, static ELF amd64, `CGO_ENABLED=0` |
| Executed identity | `version=dev`, commit `580e95ab9c3eed179ee0a5164c3e144126b15caf`; development, not release |
| SHA256 | `5f2da62e3fc103daecb07bf171a5e31897cce6ba1bcbd6efea0f879b0db47752` |
| Published releases | `gh api repos/freefsm-project/freefsm/releases --jq 'length'` returned `0`; local tags end at v0.7.0 |
| FreeBSD / VM tooling | No `qemu-system-x86_64`, `qemu-kvm`, or `virsh` found on PATH; no native FreeBSD target supplied |
| Disposable container | Rootless Podman; PostgreSQL 16.15 image digest `sha256:75adc2a65806c96bc0e59030230bfe3f1c1b0d6d8f8181a8913e2b8b53bdc166` |

The artifact checksum above records the observed build, before this documentation
change. It is not a published checksum or a reproducible-build claim. The build
metadata was inspected with `go version -m dist/freefsm`. Linux and FreeBSD
artifact checksums must be verified independently; the deployment guide's former
cross-OS byte-equality instruction was incorrect and has been corrected.

## Checks actually executed

From the implementation worktree, after `make build`:

```sh
make lint
FREEFSM_TEST_DATABASE_URL='postgres://adam@127.0.0.1:55507/freefsm_test?sslmode=disable' make test
```

Both passed. `make test` invokes `go test -v -race ./...`; real PostgreSQL tests
ran. Local full output: `/tmp/opencode/freefsm-v1-107-required-test.log`.
These local logs are ephemeral, not durable release artifacts. Backup tests
requiring `FREEFSM_BACKUP_TEST_ADMIN_URL`, the scale test, and opt-in browser
tests skipped in this invocation; those skips are not passes. The crash-child
helper also intentionally skips unless launched as a child.

Additional existing integration/browser checks:

```sh
FREEFSM_TEST_DATABASE_URL='postgres://adam@127.0.0.1:55507/freefsm_test?sslmode=disable' \
FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://adam@127.0.0.1:55507/postgres?sslmode=disable' \
go test -v -race ./internal/handlers -run '^TestBackup(HTTPRoundTrip|HistoricalDomainRoundTrip)$'

BROWSER_TESTS=1 BROWSER_EXECUTABLE=/usr/bin/chromium-browser \
NODE_PATH=/home/adam/repos/freefsm/internal/templates/testdata/browser/node_modules \
go test -v ./internal/templates -run '^TestBackupBrowserWorkflow$' -count=1
```

Both passed. Historical-domain round trip: 10.80s; HTTP round-trip suite: 63.69s,
all six development/release compatibility cases. Browser suite: 44.29s. HTTP
tests use real disposable databases and non-superuser application roles, but
injected identities and an HTTP harness. Browser engine responses are intercepted;
this is not a persisted installation journey. Existing Node dependencies were
read from the original checkout; that checkout was not modified.

Earlier attempts are not hidden:

- The first `make lint && FREEFSM_TEST_DATABASE_URL=... FREEFSM_BACKUP_TEST_ADMIN_URL=... make test`
  was terminated by the 120-second tool timeout.
- A repeat with both database variables and a longer timeout failed. It exposed
  `CREATE EXTENSION backup_preflight_fixture` unavailable in
  `TestBackupCreateUnsupportedLayout/extensions`. Provisioning that test extension
  must happen in a disposable PostgreSQL installation, not the host's shared
  extension directory. The same run had nil generated ent timestamp defaults in
  database, handler and service tests. Code generation was running concurrently
  with that attempt, so it is not a valid post-build baseline. After generation
  finished, the required suite and targeted HTTP suites passed as recorded above.
  Do not run generation concurrently with compilation/tests. Failed-run output:
  `/tmp/opencode/freefsm-v1-107-test.log`.

## Process startup observations

Native Linux development binary, empty disposable UTF8 database
`freefsm_107_startup`, no SMTP configuration, explicit loopback bind:

```sh
timeout --signal=TERM 8s env \
  FREEFSM_DB_HOST=127.0.0.1 FREEFSM_DB_PORT=55507 \
  FREEFSM_DB_NAME=freefsm_107_startup FREEFSM_DB_USER=adam FREEFSM_DB_PASSWORD= \
  FREEFSM_SESSION_SECRET=disposable-107-session FREEFSM_SETUP_TOKEN=disposable-107-setup \
  FREEFSM_ADDR=127.0.0.1:55107 \
  FREEFSM_UPLOAD_DIR=/tmp/opencode/freefsm-v1-107-startup/uploads \
  FREEFSM_STATE_DIR=/tmp/opencode/freefsm-v1-107-startup/state ./dist/freefsm
```

Observed startup, DB connection, completed migrations, listening on the configured
address, and shutdown after SIGTERM. This was foreground execution, not systemd.
The disposable test values above are not deployment credentials. Timeout-driven
termination is not evidence of a natural zero-exit service stop or bounded drain.

Also executed the same binary in a disposable PostgreSQL 16 container with
`--network none`, an ephemeral `/tmp`, and no exposed ports. Inside it:

```sh
initdb -D /tmp/pg --auth=trust --no-locale --encoding=UTF8
pg_ctl -D /tmp/pg -l /tmp/postgres.log -o '-p 55507 -h 127.0.0.1' -w start
createdb -h 127.0.0.1 -p 55507 freefsm
postgres --version
pg_dump --version
pg_restore --version
timeout --signal=TERM 8s /opt/freefsm
```

All three tools reported 16.15. With equivalent disposable environment settings,
migrations completed and the binary listened on `127.0.0.1:55107`, then logged
shutdown. The container was removed. An initial Podman invocation rejected an
unsupported tmpfs `uid` mount option before execution; using `/tmp` mode 1777 and
the container's postgres user resolved that provisioning error.

This proves only development-binary startup with PostgreSQL 16 in a container
without external networking. It does **not** prove native service operation,
backup/restore on 16, provider-degraded Customer/Job workflows, absence of attempted
egress, or reboot durability.

## Blocking implementation observations

These require regression tests and fixes, not a weaker release contract:

1. **Host-local Admin recovery absent:** `cmd/freefsm/main.go` and executable `-h`
   expose only `-config` and `-seed`. No supported existing-Admin recovery command
   or deployment procedure was found. Required password policy, forced change,
   session/recovery revocation, atomicity and denial/concurrency proof remain open.
   Coordinate protected Admin identity with #19 rather than implementing another
   Role model.
2. **Compatibility preflight is not startup preflight:** startup connects then
   migrates; it does not reject PostgreSQL <16 or missing/mismatched backup tools
   before bootstrap. `internal/backup/database.go` checks matching tool majors
   during backup preflight, but does not establish the required startup contract.
3. **FreeBSD config permissions:** `install-freebsd` copies a mode-0644 sample to
   the live credential file using `cp`, normally producing a world-readable live
   file. Fix with an explicitly restricted, service-readable config install;
   verify existing-file preservation and actual daemon-user access natively.
4. **Installation completeness:** native service users/ownership/config reading,
   writable state/log paths, rc.d status/stop behavior and systemd sandbox behavior
   have not been exercised. Static inspection is not a passing installation test.

No authorization, schema, account, or recovery implementation changes are included
in this evidence checkpoint. TDD was loaded; confirmation was requested for the
CLI/startup, production HTTP + disposable DB, and disposable installation/service
command seams. New regression automation is pending that confirmation.

## Exact acceptance gaps and external prerequisites

| Gate | Remaining evidence / input needed |
| --- | --- |
| Actual versioned releases | Release owner supplies candidate tag/commit, Linux and FreeBSD artifacts, architecture matrix and published per-artifact checksums. No release assets were available from GitHub at observation time. |
| Native Linux and FreeBSD | Disposable targets with native systemd/rc.d, administrator access, persistent disks and reboot control; OS/architecture choice and target access from operator. Never use the workstation as the reboot target. |
| Source build on each OS | Linux source build recorded; repeat published instructions on native FreeBSD and verify clean candidate identity. |
| Service lifecycle | Record install paths, owner/group/modes, effective config precedence and service identity; bootstrap, stop/start/restart and full reboot with retained data on each target. |
| PostgreSQL majors | Release owner declares a finite validated-major matrix for the advertised 16+ support. Native 18 integration and container 16 startup are partial; 17 untested; full matching-tool backup/restore and unsupported-combination fail-before-bootstrap checks remain. |
| Optional providers / browser / API | Persisted first Admin/company setup, Customer creation, Job creation and list journeys on the real executable with SMTP/maps absent and unreachable; denial/failure/concurrency cases and setup closure. Existing harness tests do not establish this. |
| Network boundary | Operator supplies intended TLS terminator, firewall/proxy topology and trusted forwarding boundary. Verify configured bind, HTTPS cookies/CSRF and direct-backend access restrictions; capture startup/idle egress and DNS attempts. Network-disabled container startup does not prove zero attempts. |
| Host recovery | Implement and test existing-Admin-only recovery under current password policy with forced change and browser/mobile/recovery revocation; no operator product Role. |
| Existing data / grants / audit | Exercise nonempty upgrade fixtures, bootstrap races and recovery atomicity against #19's protected identity; no automatic nonprotected grants or invented historical records. No migration change in this checkpoint. |
| Full quality gates | Required lint/database suite passes after generation. Broader backup suite needs disposable custom extension provisioning; persisted browser/native release matrix remains open. |

For each future native run retain OS/version/architecture, binary version/commit
and published-checksum comparison, database/tool versions, redacted service config,
UID/GID/ownership, command results, browser/API results and boot IDs before/after
reboot. Keep evidence on reboot-persistent storage. Do not mark any unchecked gate
passed from compilation, injected identities, process restart, or unit tests.
