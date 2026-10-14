# Clock acceptance boundary

Clock-in/out are Own-only actions. Broad Time Create/Correct grants never
substitute for either clock grant. Job clocks additionally require current
Job View and an active Job; Completed/Canceled are still active until archived.
Clock-out names the exact entry reviewed by the user. Its immutable subject and
retained Job link survive assignment loss/archive, without returning Job context.

All accepted commands use the canonical authority transaction fence. Interval
checks use exact half-open instants: adjacent intervals are allowed, overlapping
nonvoid entries and a second active entry require explicit repair. No trim,
split, rounding, reassignment or standalone fallback is performed.

## Field API

- `POST /api/v1/time-entries/lease` with `{}` obtains the current device's staging
  window. A lease is evidence of a window, never authority at acceptance.
- `POST /api/v1/time-entries/clock-in` accepts an optional `job_id`.
  `/api/v1/jobs/{id}/clock-in` binds the same command to that exact Job.
- `POST /api/v1/time-entries/clock-out` requires `entry_id`.
- Every clock command requires a UUID `operation_id`. Offline clocks additionally
  provide RFC3339 `occurred_at` and `lease_id`; online clocks use acceptance-time
  server instants. Web clocks always use server time.
  Optional means omitted: supplied JSON `null` is invalid, including optional
  GPS accuracy. Property names are exact-case; unknown and duplicate properties
  reject before any clock effect or accepted receipt. Explicit zero IDs never
  become omitted targets.
  Stored instants have PostgreSQL microsecond precision: submitted sub-microsecond
  occurrences/captures are explicitly invalid, never silently rounded. Offset
  timestamps retain exact supported precision across correction forms as well.
- Optional `gps` contains a complete latitude/longitude pair, `captured_at`, and
  optional nonnegative `accuracy`. GPS is an immutable point-in-time clock fact,
  not tracking, attendance proof or a service-location map source.
- `GET /api/v1/time-operations/{operation}` returns the immutable disposition and
  effect ID for the same user/device, even after losing Job access. No Job name,
  notes or GPS is included. Transient storage/audit errors have no terminal
  receipt; retry the same identity. Terminal repairs require a new identity.
- Same-intent replay creates once. A new exact-entry out with the same recorded
  offline end is a no-op; a differing end requires web correction. Neither can
  close a newer entry.
  A new operation proposing GPS for an already accepted out returns
  `gps_immutable`, rather than silently dropping or appending the capture. Query
  the original operation receipt, or explicitly submit a location-free same-end
  no-op; the original capture remains unchanged.

Invalid GPS is never discarded on the server. Explicit **Submit without
location** retains the staged occurrence and target, removes only GPS, and uses
a new operation identity. Unavailable/denied location permits a separately
chosen location-free clock. Conflicts remain visible; nothing auto-replays.
The reference client's explicit recovery also supports malformed GPS timestamps
and loss of the independent GPS Capture grant. It stages a new pending command,
retaining occurrence, lease, target and notes. Generic validation/permission
errors do not prove GPS was the only problem: current clock authority and all
invariants are checked again when the user submits the repaired command.
Accepted, unknown and retryable outcomes cannot use this new-ID recovery.

## Upgrade and ownership

Recorded entries and legacy coordinates are retained. New GPS evidence uses a
separate clock-event table; unknown historical captures/accuracy are not filled
in. Protected audit, clock effects and receipts commit atomically. New
GPS audit events record known operation occurrence separately from capture and
acceptance; protected audit browse and CSV retain that occurrence. Unknown
historical occurrence is not backfilled. Existing nonprotected Roles receive
no new permissions; pending legacy authorization
requires the existing authenticated Administrator review before clocks operate.

The obsolete role-label API mutation contract is denied. The unqualified
destructive Time Entry Delete contract is also denied: successor #59 owns the
reasoned correction/Void interface and its retained-history presentation.
This ticket establishes invariants for the existing correction/manual writers;
#83 owns composed offline synchronization, and #62 owns Job archive eligibility.
