Deploy
==========

## Prerequisites

- PostgreSQL 16+
- Matching-major `pg_dump` and `pg_restore` on the service's PATH (PostgreSQL client package)
- FreeFSM binary (build with `make build`)

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

## Linux (systemd)

1.  Create the `freefsm` user:
    `sudo useradd --system --user-group --create-home --home-dir /var/lib/freefsm freefsm`

2.  Copy the binary:
    `sudo cp dist/freefsm /usr/local/bin/freefsm`
    `sudo chmod +x /usr/local/bin/freefsm`

3.  Install the service and config:
    `sudo make install-linux`

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
