# Instance admission contract

All processes for an instance must share the same persistent local state directory
(outside the database and upload roots being replaced). The directory is private
0700; marker and permanent lock files are 0600. Linux and FreeBSD use native
`flock`, with a separate open file description per admission. Never delete or
replace the lock files, including during cleanup.

## Engine lifecycle

1. Acquire `OperationLock()` before inspecting/recovering or starting a job. It
   fails immediately with `ErrOperationBusy` for a competing process/job.
2. Acquire `Maintenance(ctx)` before capture/replacement/recovery. It durably
   closes new admissions before draining existing requests and worker attempts.
   A marker from an earlier crash does **not** prevent recovery acquisition.
3. Complete the engine's journal/recovery decision. The production restore engine
   is authoritative for session invalidation: expire/revoke web and mobile
   sessions and call `DisableEmail()` before reopening.
4. Only after the selected state is verified coherent and the engine's decision
   is durable, call `ClearMaintenance()` **while still holding both locks**.
5. Release maintenance, then the operation lock.

Maintenance release **never removes the marker**. On an error, cancellation,
crash, or failed recovery, leave it present. `KeepClosed()` explicitly persists
the marker if needed. A failed acquisition after intent has been written also
leaves the marker present for recovery. A `ClearMaintenance` error must be treated
as a recovery failure: call `KeepClosed` and do not reopen/start normal work.

Do not acquire maintenance from a request holding shared admission; start the
engine outside that admission context. Do not inherit request cancellation into a
destructive job. Journal ownership and safe reopening are engine responsibilities.

## Application wiring

- Wrap the entire normal application with `Control.Middleware` **outside** any
  authentication middleware that reads/writes sessions. It returns HTTP 503 for
  every normal route during maintenance. Controlled operation endpoints must be
  selected by the outer router and enforce their own engine/authorization rules.
- Before starting requests/workers, call `EmailService.SetInstanceControl(c)` and
  `delivery.Service.SetInstanceControl(c)`. Constructors retain nil-control behavior.
- Other background writers must hold `Enter(ctx)` through their complete work.
  Delivery `ProcessOne` already holds it from before claim through final attempt
  commit. Disabled email leaves pending work and attempt counters untouched.
  Set `delivery.Service.SetBeforeWork(manager.RefreshConnections)` at startup;
  the hook runs after admission and before any claim/database access. Refresh
  failures prevent the attempt. Normal HTTP and authenticated backup actions also
  refresh after admission, before session access. Ent uses pgx's uncached
  extended-protocol execution; the cached pgx pool is reset on generation changes.
- Pass the returned context to nested calls. Admissions are reference-counted,
  reentrant across controls for the same absolute directory, and remain held until
  all nested releases run. Retained contexts cannot bypass a released admission.
- `DisableEmail` persists the gate; use it under maintenance when in-flight sends
  must first drain. `EnableEmail` only clears that gate; the caller must enforce
  existing administrator authorization. Neither method changes maintenance state.
- `EmailService.CheckAvailability(ctx)` / `EmailDisabled()` expose the gate for
  handlers/settings. `Control.EnterEmail(ctx)` owns admission and the persistent
  gate check for email services and delivery work, releasing admission on failure.
  All production SMTP paths recheck it at transport entry.
  Queue and manual retry reject disabled actions with `ErrEmailDisabled`.

Call setters only during initialization. A missing/inaccessible state directory
or unreadable marker state fails closed. Startup recovery must precede migrations,
workers, and normal requests; constructing a Control alone does not recover state.
