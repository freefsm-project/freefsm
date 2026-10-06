# Whole-instance backup engine

`backup` is a single deep module. Its public surface is:

```go
type Config struct {
    DSN, UploadDir, StateDir, Version, Commit string
    BuildKind BuildKind // BuildRelease or BuildDevelopment
    Control *instancecontrol.Control
    RecoveryOnly bool // recover unsupported builds without permitting transfers
    ResetConnections func()
    ReportFailure func(Diagnostic) // optional structured operator log sink
    ActivitySink func(context.Context, ActivityEvent) error
}
func New(Config) (*Manager, error)
func (*Manager) Recover(context.Context) error
func (*Manager) StartBackup(actorID int64, sourceName, password string) (Operation, error)
func (*Manager) StageUpload(context.Context, int64, io.Reader, string) (Operation, error)
func (*Manager) StartRestore(ctx context.Context, actorID int64, operationID,
    destinationName, confirmation string) (Operation, error)
func (*Manager) StartBackupFor(ActivityActor, string, string) (Operation, error)
func (*Manager) StageUploadFor(context.Context, ActivityActor, io.Reader, string) (Operation, error)
func (*Manager) StartRestoreFor(context.Context, ActivityActor, string, string, string) (Operation, error)
func (*Manager) RecordDownload(context.Context, ActivityActor, string) error
func (*Manager) Status(actorID int64, id string) (Operation, error)
func (*Manager) StatusCapability(id string) (Operation, error)
func (*Manager) OpenDownload(actorID int64, id string) (io.ReadCloser, string, int64, error)
func (*Manager) RefreshConnections() error
func (*Manager) Shutdown()
```

`Operation` contains `ID`, `Kind`, `Phase`, `Error`, `SourceName`, `Version`,
`Commit`, `BuildKind`, `ArchiveDigest`, `CapturedAt`, `ExpiresAt`, and `Failure`. Phases include `queued`,
`preflight`, `capturing`, `encrypting`, `validating`, `ready`, `recovery-copy`,
`replacing-database`, `replacing-files`, `protecting-instance`, `committing`,
`rolling-back`, `complete`, and `failed`. Errors exposed through status and startup
recovery are sanitized; structured operator diagnostics are described below.

## Composition contract

1. Construct the shared instance control and manager with persistent local
   storage outside uploads. Supply an explicit release/development `BuildKind`
   separately from actual version/commit metadata. Development identities need
   no semantic version or commit and may be dirty or git-describe distances.
   Declared releases require valid release identities and clean exact-tag metadata
   in composition. Between releases version and commit must match exactly;
   development involvement permits differing identities only with matching trusted
   local schemas and inventories. Archived schema SQL is never executed.
   Missing build kind is inferred only from valid legacy release fields; invalid
   or unknown kinds fail closed. New manifests include `BuildKind`; older strict
   readers reject that unknown field rather than silently misinterpret it.
   `RecoveryOnly` retains recovery for invalid build metadata, without transfers.
   Construction validates upload-root ancestors before creating uploads.
2. Call `Recover` **before migrations, normal admissions, or workers**. A failure
   blocks startup and leaves maintenance and recovery evidence intact. Keep the
   DSN and upload-root configuration unchanged during incomplete recovery: the
   journal binds both, without storing the DSN itself.
3. `ResetConnections` must reset all application pools (for example
   `pgxpool.Pool.Reset` for each pool). It is required to start a restore. Each
   process must call `RefreshConnections` after admission and before database
   access, including background attempts. The durable generation changes before
   schema replacement, so processes other than the operation owner also discard
   prepared-statement caches. Treat refresh failures as admission failures.
4. Handlers enforce protected Administrator authorization on every boundary.
   Require fresh **account-password** verification immediately before download
   and restore start. The archive password is independent. Obtain actor ID,
   source name and destination name from trusted server state. Only typed
   `confirmation` comes from the confirmation field. Bind any reauthentication
   token to operation ID plus `ArchiveDigest`; staged content is immutable and
   `ready` is consumed once by restore start.
5. Status IDs are random 256-bit capabilities. `StatusCapability` exposes status
   only, allowing completion polling after sessions are invalidated. Keep these
   tokens out of logs/referrers. Download and mutation always require actor-bound
   methods and caller authorization.
6. Controlled endpoints must sit outside ordinary admission. `StageUpload`
   synchronously streams the HTTP body to disk, then authenticates and validates
   asynchronously. Other work uses independent, six-hour job contexts. Set HTTP
   transfer limits/timeouts in deployment composition and close download readers.
   `Shutdown` stops accepting jobs and waits for running jobs/input staging.

## Archive and database trust boundary

The portable file is standard **age scrypt encryption**, containing a tar archive
with an authenticated manifest and SHA-256 inventory. Every upload-root regular
file is included, independently of current business visibility. Passwords are
never written to disk. Extraction rejects unsafe paths, links, duplicate entries,
conflicting inventory, excess content, incorrect digests and incomplete age EOF.

An administrator who knows an archive password can manufacture an archive. Its
SQL therefore remains **untrusted even after authentication**. The engine renders
all native dump sections with `pg_restore` but never executes uploaded SQL.
Normalized pre/post-data schema fingerprints must match a separate trusted local
schema dump and the later recovery capture. Data is parsed with a restricted
grammar: exact public-table COPY headers, streamed COPY bodies, and numeric
sequence assignments. Settings/comments are ignored; other executable statements
are rejected. Table, column and sequence inventories must match the destination.
COPY bytes use PostgreSQL's COPY protocol and sequence assignments use parameters.

Replacement drops the application-owned `public` schema, restores **local trusted
pre-data schema** using native `pg_restore`, imports validated data, then restores
**local trusted post-data schema**. This permits immutable application triggers
without privileged trigger disabling. Both restore and rollback use the existing
database and application credentials; no CREATEDB, owner/ACL replay, or source
PostgreSQL roles are needed. Preflight requires an application-only public schema,
matching local PostgreSQL tool/server major versions, schema ownership, database
CREATE privilege, and sufficient local staging/recovery capacity. Extensions,
large objects and extra application schemas are rejected rather than silently
omitted. PostgreSQL application errors, including server-side capacity failures,
remain rollback-protected; validation is not a promise that applying data cannot
fail.

The manifest maps each live `files.file_path` and
`company_settings.invoice_logo_path` value to a validated upload-root-relative
payload. Validation checks every such reference in COPY data. Successful restore
uses a temporary mapping table and targeted updates to rebase those two columns.
Historical snapshots and delivery evidence are not rewritten. All restored
sessions receive past `expires_at` and current `revoked_at`; persistent email
disablement precedes the durable success decision.

## Recovery and resources

### Backup activity

Production composition supplies `services.WriteBackupActivity` using the configured
DSN before `Recover`; its short-lived pgx connection works before Ent startup and
after schema replacement. Call the `For` APIs with freshly authenticated actor and
company **name snapshots**, never user-submitted labels. Legacy ID-only methods
remain for callers without a sink; a configured sink requires a name snapshot.
After opening a download, call `RecordDownload` before sending bytes. An insert
failure denies that download attempt without changing its backup operation.

The engine publishes backup/validation outcomes, including synchronous upload read
failures, without relying on polling. One bounded `pending-activity.json` record
outside disposable staging directories stores a stable key and occurrence time.
An interrupted attempt retains a safe interrupted-failure event. Publication failure
blocks further operations until startup replay succeeds. This prevents stale
destination outcomes from being merged into a subsequently restored archive.

Restore journals store the trusted actor snapshot and independent random keys for
`restore_started` and its outcome. Start is inserted before capturing the rollback
copy. Successful replacement restores the archive's activity history plus this
restore's start/completion; unrelated destination history is not carried forward.
The recorder stores NULL actor links and attributes ownership to the current
company, so missing or colliding restored numeric user IDs cannot impersonate the
initiator. Event metadata contains only allowlisted facts, never passwords,
capabilities, filesystem paths or raw errors.

After database/files/session/email protection and the durable commit decision,
recovery replays start/completion idempotently before deleting any recovery copy.
A publication failure at this point leaves the restore committed (status remains
`complete`, with `Failure`/`Error` reporting required recovery), admission closed,
and evidence intact for startup retry. It never emits `restore_failed` or rolls
back a durable commit. Rolled-back attempts replay start/failure into the original
database. Keys and event times survive retries; the database's unique event key
prevents duplicate rows even after a crash following insertion. Legacy v0.7
journals without activity fields recover without inventing actor snapshots.

New activity-bearing journals require migration 054 already present in their
database/recovery material, as guaranteed by this build's migrated runtime and
schema validation. Ordinary startup without pending events never queries the
activity schema before migrations.

`TestRestoreActivityDurablePublication` covers real restored actor-ID collisions,
missing actors, company ownership, history selection, publication failure, rollback
and stable replay timestamps. `TestRestoreActivitySIGKILLReplay` kills real child
processes before publication, after publication and before commit, then recovers
twice in fresh processes. Ordinary pending outcomes and download gating have
separate tests; integration tests require `FREEFSM_BACKUP_TEST_ADMIN_URL` pointing
at a disposable PostgreSQL cluster.

The external atomic/fsynced journal transitions through:

```
capturing -> replacing -> committed
                     \-> rolled-back
```

Before `replacing`, a complete password-free local database/file recovery copy and
its digest inventory are durable. Any surviving `replacing` decision rolls back
both database and files; recovery copies are copied, never consumed, so another
interruption can retry. The prior persistent email gate is restored on rollback.
An ambiguous commit fsync retains all recovery evidence and maintenance. Only a
durable committed/rolled-back decision permits recovery-copy removal. Maintenance
is explicitly cleared while both instance locks remain held.

Both new transfers and `StartRestore` check for a journal **under OperationLock**,
before allocating/consuming operation state. Any existing journal entry (including
directories and dangling links), or inability to inspect it, returns
`ErrRecoveryRequired`. A previously staged archive cannot supersede another
operation's incomplete recovery. Its `ready` state remains available after
successful recovery and fresh handler reauthentication.

### Failure diagnostics

`Operation.Failure` contains `Category`, `Phase`, `Message`, and `DiagnosticID`.
`Operation.Error` retains a convenient actionable message for existing callers.
Categories distinguish authentication, archive integrity/format, release/schema
mismatch, file references, storage/capacity, resource limits, tool availability/
compatibility, database connectivity/privileges/application/layout, cancellation, and
required recovery. A failed rollback retains both original and recovery causes;
its primary category is `recovery-required`.

Preflight layout failures use `database-extra-schemas`, `database-extensions`, or
`database-large-objects`, with fixed safe reasons and actionable messages. They
are distinct from `schema-mismatch`, which still reports an archive/destination
schema mismatch. Layout messages ask the operator to inspect the unsupported
objects before any cleanup; backup never removes them automatically.

`TestBackupCreateUnsupportedLayout` exercises these failures against a disposable
cluster with real migrations and verifies source preservation and sanitized
diagnostics. Its empty test extension is in `testdata/extension/`. On PostgreSQL
18+, start the disposable server with
`extension_control_path='<absolute-path-to-internal/backup/testdata>:$system'`;
on earlier versions, install those two fixture files into that disposable
server's extension directory. This fixture is only for test servers.

The latest diagnostic is atomically persisted as
`StateDir/backup/last-failure.json`, replacing the previous diagnostic rather than
growing a log. Optional `Config.ReportFailure` receives the same sanitized value
for an operator log sink; it should return promptly. A persistence failure is
indicated to that callback with `PersistenceFailed`. Correlation IDs are separate
from operation/status bearer capabilities.

Diagnostics retain at most four causes, validated SQLSTATE codes, fixed engine
reason strings, tool names/exit codes, and at most eight fixed-vocabulary stderr
signals. Stderr scanning is limited to 64 KiB with a 4 KiB partial-line buffer;
remaining bytes are drained and discarded. Raw stderr, COPY rows, SQL error
DETAIL/CONTEXT, SQL statements, object names, filesystem paths, command arguments,
DSNs and passwords are never included in persisted/emitted diagnostics. Native
tool errors produce categories from recognized error prefixes, not raw message
logging. Unknown content is deliberately not echoed.

Internal producers attach a typed failure category directly to each engine error;
the diagnostic layer does not classify internal errors by matching their prose.
Reason wording can change without changing the category. Arbitrary external
errors that happen to contain an old engine message are not treated as engine
errors. The fixed-token recognizer remains confined to external tool stderr.

Streaming limits are 16 GiB per archive/payload-output budget, 100,000 inventory
entries, 32 MiB manifest/schema-section budgets, bounded path metadata, and 32
pending transfers. These are defensive resource ceilings, not the 1 GiB acceptance
target. COPY rows stream in 64 KiB fragments, including large bytea values; full
archives and database payloads are never buffered in memory. Scrypt's work factor
is capped at 18 on upload. Capacity checks retain a 256 MiB local free-space floor.

Transfer artifacts expire one hour after terminal/ready state, with timer and
request-driven cleanup. Per-transfer flock leases prevent another process's
startup cleanup from deleting a live staged upload or download. Active downloads retain their file until closed; expiry
prevents new downloads. Failed jobs and completed restores promptly discard their
transfer material. Startup discards interrupted/abandoned transfer directories.
Needed journal recovery material has **no TTL**. This is temporary recovery state,
not a backup history.

## Verification

The core integration test provisions unique disposable databases and distinct
NO-CREATEDB/NOSUPERUSER roles from an explicitly supplied disposable-cluster admin
URL. It covers encrypted cross-root/cross-role transfer, file rebasing, immutable
trigger schema, session expiry/revocation, persistent email disablement,
password-free interrupted-replacement recovery and repeat recovery. It also runs
all real application migrations and exercises native pre/data/post restoration of
that schema. Additional tests cover authenticated EOF, wrong passwords, tampering,
truncation, executable data rejection, large COPY rows, pool generations, failed
recovery admission, and safe cleanup decisions.

`TestStagedRestoreCannotOverwriteFailedRecovery` stages two real encrypted
archives, fails the first restore after database replacement and partial file
removal, then fails rollback. It verifies that the second restore is rejected
without changing the original journal, recovery copy, maintenance state or staged
operation, and succeeds only after recovery.

`TestProcessCrashAndRepeatedInterruptedRollback` launches real test-binary child
processes through `StageUpload`/`StartRestore` and SIGKILLs them after durable
`replacing`, schema drop, partial file replacement, and session/email protection.
For each case, two new `RecoveryOnly` processes are also SIGKILLed during rollback
(after trusted pre-data restoration and during file removal). Further fresh
startup processes must restore the original database/files/email gate and recover
idempotently. Journals are produced by real engine operations, never fabricated
by these tests. Recovery children receive neither an archive path nor an archive
password. All migrations run in the fixture; domain-rich financial/conversion/
delivery fixture assertions remain the separate workflow integration exercise.

Fault barriers are an unexported manager hook configured only by package tests.
The subprocess environment entry point exists only in `_test.go`; production has
no environment-variable fault-injection bypass.

`TestProcessCrashAfterCommitAndDuringCleanup` covers the successful durable
decision using actual public-API restores. It SIGKILLs the restore process after
the committed journal fsync and, separately, during recovery-copy deletion. Two
further recovery processes are killed while making additional durable cleanup
progress. Every interruption must preserve the committed journal, source database
and upload bytes, expired/revoked restored web and mobile sessions, removal of old
destination sessions, and persistent email disablement. Two clean restarts then
finish cleanup and prove idempotence. Recovery-copy entries are removed and their
directory fsynced individually; an incomplete copy after commitment is never used
for rollback. Maintenance remains closed until cleanup finishes.

```sh
FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://.../postgres?sslmode=disable' \
  go test -race -v ./internal/backup

FREEFSM_BACKUP_TEST_GB=1 FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://.../postgres?sslmode=disable' \
  go test -count=1 -run TestEncryptedCrossRoleRestoreAndRecovery -v ./internal/backup
```

Review follow-up verification used a newly initialized disposable cluster:

```sh
initdb -D /tmp/opencode/issue17-review-pg --auth=trust --no-locale \
  --encoding=UTF8 -U backup_review_admin
pg_ctl -D /tmp/opencode/issue17-review-pg \
  -l /tmp/opencode/issue17-review-pg.log \
  -o '-p 55441 -h 127.0.0.1 -k /tmp/opencode' start

FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://backup_review_admin@127.0.0.1:55441/postgres?sslmode=disable' \
  go test -race -count=1 -v ./internal/backup
make lint
GOOS=freebsd GOARCH=amd64 go test -c ./internal/backup \
  -o /tmp/opencode/backup-review-freebsd.test
```

The full package race run passed in 116.932 seconds. After strengthening the
two-staged-operation regression to demonstrate a valid restored database with
partially removed destination files, the final diagnostic/regression subset was
rerun with `-race -count=1` and passed in 40.819 seconds:

```sh
FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://backup_review_admin@127.0.0.1:55441/postgres?sslmode=disable' \
  go test -race -count=1 -v ./internal/backup \
  -run 'Test(StagedRestoreCannotOverwriteFailedRecovery|AsyncFailuresAreActionableAndSanitized|SanitizedDiagnosticsRetainActionableCauses|CommandDiagnosticsNeverReturnRawStderr|StderrDiagnosticResourceBounds|StartRestoreRejectsAnyJournalEntryBeforeMutatingJob)'
```

The URL above is a password-free, loopback-only disposable test fixture. Tests
create/drop unique `crash_<nanoseconds>` databases and NO-CREATEDB/NOSUPERUSER
application roles, with uploads/state and private child specifications under
`testing.T.TempDir()`. The child helper's standalone invocation is intentionally
skipped; the parent crash tests execute it in actual child processes. No database
integration cases were skipped. The cluster is stopped after verification with
`pg_ctl -D /tmp/opencode/issue17-review-pg stop -m fast`.

The final committed-cleanup/typed-producer review used a separate fresh UTF-8
disposable cluster at `/tmp/opencode/issue17-final-pg`, log
`/tmp/opencode/issue17-final-pg.log`, on `127.0.0.1:55443`. It was initialized with
`initdb --auth=trust --no-locale --encoding=UTF8 -U backup_final_admin`; fixtures
again used unique non-superuser, NO-CREATEDB application roles. This focused race
run passed in **101.751 seconds**, including both committed crash scenarios,
repeated interrupted rollback, the two-staged-operation regression, and typed/
sanitized diagnostic tests. `make lint` also passed.

```sh
FREEFSM_BACKUP_TEST_ADMIN_URL='postgres://backup_final_admin@127.0.0.1:55443/postgres?sslmode=disable' \
  go test -race -count=1 -v ./internal/backup \
  -run 'Test(ProcessCrash.*|StagedRestoreCannotOverwriteFailedRecovery|AsyncFailuresAreActionableAndSanitized|SanitizedDiagnosticsRetainActionableCauses|InternalFailureProducersCarryCategories|CommandDiagnosticsNeverReturnRawStderr|StderrDiagnosticResourceBounds|StartRestoreRejectsAnyJournalEntryBeforeMutatingJob|RecoveryPredestructiveAndCommittedCleanup)'
make lint
pg_ctl -D /tmp/opencode/issue17-final-pg stop -m fast
```

The 2026-10-04 core scale run used 1,073,741,824 incompressible upload bytes plus
database/history fixtures, producing a 1,074,019,510-byte encrypted archive. The
round-trip/recovery test took 67.47 seconds; `/usr/bin/time -v` reported 548,076 KiB
maximum RSS. Source/destination storage immediately after completed restore and
transfer cleanup was 3,221,503,261 bytes (including source uploads and its retained
encrypted download); staging/recovery needs additional transient capacity. This
measures the core streaming seam on disposable local storage.
The issue's browser/proxy acceptance and populated financial/conversion/delivery
workflow assertions belong to the handler/deployment integration exercise; this
core result does not substitute for that end-to-end run.
