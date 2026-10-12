Deploy
==========

## Prerequisites

- PostgreSQL 16+
- Matching-major `pg_dump` and `pg_restore` on the service's PATH (PostgreSQL client package)
- FreeFSM binary (build with `make build`)

Startup checks the database major (minimum 16) and requires both `pg_dump` and
`pg_restore` to report that same major on the **service's** PATH before migrations
or bootstrap. A missing or mismatched tool stops startup with installation
instructions. Recovery of an interrupted restore still runs first, preserving
the existing recovery ordering. See the acceptance report for the distinction
between the tested major matrix and full native release validation.

## Build

The Makefile is written for **GNU Make**. On FreeBSD, install `gmake` first:

```sh
# FreeBSD
sudo pkg install gmake

# Build on both Linux and FreeBSD
gmake build
# or on Linux
make build
```

After building, record the binary checksum:

```sh
gmake checksum
```

This records the local artifact's identity; it does not verify a published
release. See the per-target verification procedure below.

## Setup

### Whole-instance backup and recovery

The protected Administrator Role can create and restore encrypted whole-instance
archives at **Settings → Backup**. Other product Roles
do not gain this authority. Deployment Operators retain responsibility for host
configuration, dependencies, infrastructure backups and recovery storage.

`make build` / `make compile` declare clean exact tagged builds as releases and
other builds as development, retaining the available commit. Development includes
dirty, untagged and unidentified `go run` builds and supports backup and restore.
The page and archive manifest explicitly identify development. All restores must
match the trusted-local schema fingerprint and inventory; archived schema SQL is
never executed. Between releases the exact version **and commit** must also match.
Archives without a build kind are accepted only as valid legacy release identities.
Older binaries with strict manifest readers reject new manifests; use an updated
binary to read new archives. Startup recovery runs even when build metadata is
invalid; never change the DSN or upload root during incomplete recovery.

Set `FREEFSM_STATE_DIR` to persistent local storage owned by the service user. Its
default is a `state` sibling of `FREEFSM_UPLOAD_DIR`: `/var/lib/freefsm/state` on
Linux or `/var/db/freefsm/state` with the FreeBSD default. State and uploads must
be disjoint, non-symlink directories. All processes for one instance must share
the same state directory and filesystem locking; network/distributed storage is
not a supported coordination mechanism. The directory is private (0700), and
contains permanent lock files, email-disable state, generation markers and the
external recovery journal. Never delete lock files or recovery evidence manually.

Recovery runs before upload-root creation, migrations, clients or workers. A
failure blocks startup and retains maintenance and evidence. Investigate the
operator-facing startup error and restore storage availability before restarting.
The existing application database role must own the application-only public
schema and have CREATE privilege on its database; neither superuser nor CREATEDB
is needed. Tools must match the server major version.

Allow disk space for the encrypted transfer, decrypted staging, a complete local
database/file rollback copy and replacement output, plus a 256 MiB free-space
floor. Rollback copies are private but deliberately **not password encrypted**:
crash recovery must work without the archive password. Protect this persistent
storage with host controls. Needed recovery material has no expiry. Ready and
terminal transfer artifacts expire after one hour; active downloads retain their
file until closed. This is not a retained backup library.

Use the dedicated `/settings/backup` location in `linux/freefsm.nginx.conf` on
Linux or FreeBSD. It disables upload/download buffering, allows up to 16 GiB plus
archive overhead, and grants six-hour transfer timeouts. Application transfer
deadlines are raised only for upload/download before CSRF processing; ordinary
attachments retain their limits. Keep headers, bodies and cookies out of proxy
and tracing logs: operation status capabilities and passwords travel only there.
Status is POST-only and can report completion after sessions expire, but cannot
authorize downloads or mutations. Multi-process deployments need sticky routing
for this temporary in-memory transfer/status lifecycle, even though maintenance
and recovery locks coordinate across all processes.

After restore, log in with a restored account. Email remains persistently disabled
across restarts until an Administrator uses the dedicated enable action in
Settings. Review restored SMTP credentials and eligible pending/retry work first.
The archive contains database-backed settings; destination host secrets and paths
stay local. Preserve archive passwords independently; they cannot be recovered.

The 1 GiB Chromium/nginx acceptance run passed on 2026-10-05, including native
download, upload, review, restore, restored-account login and recovery cleanup.
See [browser acceptance results](../docs/backup-browser-acceptance.md) for sizes,
timings, peak memory, storage use and reproduction steps. This validates the
integration harness with injected release identities, not a genuine tagged
release artifact. Core engine measurements are in `internal/backup/README.md`.

The real PostgreSQL handler round-trip regression is
`TestBackupHTTPRoundTrip` in `internal/handlers/backup_integration_test.go`. It
uses two disposable databases and distinct non-superuser, NO-CREATEDB roles,
real migrations and sessions, different roots/settings/accounts, CSRF, fresh
password checks, metadata/destination review, `.age` transfer, session expiry,
restored-account login and explicit email enablement. Run it with
`FREEFSM_BACKUP_TEST_ADMIN_URL` pointing at a disposable trust-authenticated
cluster. `TestBackupHistoricalDomainRoundTrip` in
`internal/handlers/backup_history_integration_test.go` also exercises populated
settlement, conversion and delivery history across restore, including reversals,
conversion cycles, immutable delivery snapshots and provider evidence. It uses
the same disposable-cluster environment variable.

After first start, visit `/setup` on your server and enter the
`FREEFSM_SETUP_TOKEN` from your config to create the first admin account. The
token is required at every startup, so retain it in the config and protect it
like a secret. It is accepted for setup only while no users exist.

### Host-local existing-Administrator recovery

Run the installed binary as the service account, using the same configuration,
database and persistent state directory as the instance. Host access does not
create a product Role. `-recover-admin EMAIL` resets only an existing **enabled**
Administrator; it never bootstraps, creates, enables or promotes a user. Normal
service startup and recovery must use the same state directory so admission and
operation locks coordinate across processes.

Supply the temporary password twice, as two matching newline-terminated lines
on standard input. Do not put it in command arguments or environment variables.
For a root-owned Linux systemd EnvironmentFile, first make a temporary restricted
copy readable by the service user:

```sh
sudo install -o freefsm -g freefsm -m 600 /etc/freefsm.conf /run/freefsm-recovery.conf
```

In Bash with tracing disabled, use non-echoing prompts:

```bash
set +x
IFS= read -r -s -p 'Temporary Admin password: ' password; printf '\n'
IFS= read -r -s -p 'Confirm password: ' confirmation; printf '\n'
printf '%s\n%s\n' "$password" "$confirmation" |
  sudo -u freefsm /usr/local/bin/freefsm -config /run/freefsm-recovery.conf \
    -recover-admin admin@example.com
unset password confirmation
sudo rm /run/freefsm-recovery.conf
```

Run as the service identity so newly created recovery state retains the correct
ownership. Securely supplied equivalent environment settings also work. On FreeBSD use
`-config /usr/local/etc/freefsm.conf`; the installer makes this file service-owned
mode 0600. A root-readable restricted input file containing the two lines can
also be redirected into the command; protect and remove it when finished.

The command reconciles pending restore recovery, acquires the instance operation
lock, drains admitted requests/workers, reads current Admin identity and password
policy under database locks, then commits the password, forced-change flag,
revocation of all browser/mobile sessions and recovery links, ordinary activity,
and host-attributed immutable audit together. The shared protected audit ledger
records the host UID and recovery target, with no Product User actor, effective
Role or product permission scope assigned to the operator. No plaintext password
or credential hash is recorded in the event. Failure of either audit write rolls back
all account effects. Concurrent instance operations are rejected. A normal
policy/target failure releases maintenance; a failed maintenance drain stays closed
until startup recovery reconciles it. The command exits without starting HTTP,
workers, seed data or migrations. After success, the Admin must use browser login
and replace the temporary password before product access; old tokens remain invalid.

Use a password satisfying the saved company policy (bcrypt maximum 72 bytes).
Recovery requires an initialized, migrated installation. It does not supply a
way around disabled accounts, missing migrations, inaccessible storage or database
credentials. For an uncertain commit result, inspect account state before retrying.

## Linux (systemd)

1.  Create the `freefsm` user:
    `sudo useradd --system --user-group --create-home --home-dir /var/lib/freefsm freefsm`

2.  Copy the binary:
    `sudo cp dist/freefsm /usr/local/bin/freefsm`
    `sudo chmod +x /usr/local/bin/freefsm`

3.  Install the service and config:
     `sudo make install-linux`

    The installer creates service-owned log, upload and persistent-state roots;
    state is mode 0700, other writable roots mode 0750. The EnvironmentFile is mode
    0600 and its contents survive reinstall. `DESTDIR` stages installation without
    contacting systemd. The supplied unit runs as `freefsm:freefsm`; if overriding
    filesystem ownership with `SERVICE_USER`/`SERVICE_GROUP`, update the unit's
    User/Group consistently before activation.

4.  Edit `/etc/freefsm.conf` with your database credentials and session secret.

5.  Start the service:
    `sudo systemctl enable --now freefsm`

6.  (Optional) Set up the Nginx reverse proxy:
    `sudo cp deploy/linux/freefsm.nginx.conf /etc/nginx/sites-available/freefsm`
    Edit `/etc/nginx/sites-available/freefsm` — replace `example.com` with your domain.
    `sudo ln -s /etc/nginx/sites-available/freefsm /etc/nginx/sites-enabled/`
    `sudo systemctl reload nginx`

## FreeBSD (rc.d)

1.  Install prerequisites:
    `sudo pkg install gmake`

2.  Create the `freefsm` user:
    `sudo pw useradd freefsm -s /usr/sbin/nologin -d /var/db/freefsm -m`

3.  Copy the binary:
    `sudo cp dist/freefsm /usr/local/bin/freefsm`
    `sudo chmod +x /usr/local/bin/freefsm`

4.  Install the service and config:
     `sudo gmake install-freebsd`

    The service is installed in `/usr/local/etc/rc.d/freefsm`. The live config is
    service-owned mode 0600; reinstall preserves its contents and tightens legacy
    permissions. State is mode 0700, uploads/logs mode 0750, and their required
    directories are service-owned. If upgrading an installation with the old
    `/etc/rc.d/freefsm` path, stop it and review/relocate that script first; the
    installer refuses to leave duplicate boot services. Custom service identities
    require matching `SERVICE_USER`/`SERVICE_GROUP` installer values and rc.conf
    `freefsm_user`. `DESTDIR` supports disposable staging of this FreeBSD target.

5.  Add to `/etc/rc.conf`:
    `freefsm_enable="YES"`
    `freefsm_config="/usr/local/etc/freefsm.conf"`

6.  Edit `/usr/local/etc/freefsm.conf` with your database credentials and session secret.

7.  Start the service:
    `sudo service freefsm start`

## Per-target release verification

Linux and FreeBSD executables target different operating systems and are not
expected to have identical bytes. Verify **each downloaded artifact against its
own published SHA256**, using the release's exact OS/architecture filename. On
Linux use `sha256sum <artifact>`; on FreeBSD use `sha256 -q <artifact>`. Compare
that result with the checksum published for that artifact. A locally calculated
checksum without a published value is an identity record, not release verification.

For source builds, install the pinned ent and templ generators and Go version
listed in the root README, check out the intended tag, and run `make build`
(Linux) or `gmake build` (FreeBSD). Record `git rev-parse HEAD`, `git status
--porcelain`, `go version`, `go version -m dist/freefsm`, and `make checksum`
(`gmake checksum` on FreeBSD). Generated files must be present before running
`make lint` or the database-backed `make test` in a fresh checkout. A development
build does not replace execution of the actual versioned release artifact.

Native installation acceptance additionally requires service-managed bootstrap,
start/stop/restart and host reboot, matching-major PostgreSQL backup tools, and
persisted workflows without optional providers. Containers and cross-compilation
are supporting checks, not native FreeBSD or host-reboot evidence. The current
[native installation evidence and blockers](../docs/native-installation-acceptance.md)
record which gates have actually been exercised.

## Disposable installation regressions

After `make build`, run `make lint` and `make test` with
`FREEFSM_TEST_DATABASE_URL` explicitly naming a disposable trust-authenticated
cluster. Executable installation tests provision unique databases and NO-CREATEDB,
non-superuser application roles, so the test fixture connection needs role/database
creation rights; the application does not. Tests never install host services.

`cmd/freefsm/installation_integration_test.go` exercises the actual executable,
startup preflight, first Admin/company setup, provider-degraded persisted Customer
and Job workflows, CLI recovery, password replacement through production HTTP,
revoked browser/mobile credentials, denial, audit failure and concurrent identity
changes. Set `BROWSER_TESTS=1`, `BROWSER_EXECUTABLE`, and `NODE_PATH` to the existing
Playwright test dependency installation to include its persisted browser journey.
`FREEFSM_INSTALLATION_BINARY` can select an already-built executable for target
execution; record that artifact's identity separately.

For supporting PostgreSQL matrix evidence with rootless Podman:

```sh
make build
sh deploy/verify-postgres.sh 16
sh deploy/verify-postgres.sh 17
sh deploy/verify-postgres.sh 18
sh deploy/verify-postgres.sh 15  # rejection before any schema/bootstrap changes
```

The script compiles static test runners, provisions disposable network-isolated
containers with matching client tools, runs executable and real-database backup
HTTP regressions, records image digests and binary checksums, and removes the
containers. It uses no host DB/ports/services. Images may be downloaded. This
does not substitute for either native service installation or reboot acceptance.
