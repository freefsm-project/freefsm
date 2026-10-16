# Timesheet and clock GPS CSV exports

From Timesheets, **Export Timesheet CSV** and **Export clock GPS CSV** carry the
current date range, subject, Job (including standalone), lifecycle, source, notes
search, grouping and sorting into a confirmation. The confirmation selects an
explicit authorized scope: Own Records, Assigned Jobs or All Records. All matching
rows in that scope are included; there is no page-size truncation. A broader View
does not widen Export. Own and Assigned scopes are independent; when both are
available the confirmation permits choosing either.

Time requires `time.export` and compatible `time.view`. GPS requires
`time.gps.export`, compatible `time.gps.view` and `time.view`; it does not require
Time Export. These are separate artifacts: time-only output never contains
coordinates, capture times or accuracy, even for an Administrator. GPS-only
output contains bounded subject/Job identification and accepted observations,
not time intervals, notes, durations or correction history.

Both definitions are registered in `exports.Product` and use the existing
[durable CSV workflow](customer-csv-exports.md). Acceptance, generation and each
download are audited atomically with their effects. Pending/Running/Ready/Failed,
restart recovery, requester isolation, whole-artifact transfer preparation,
digest validation and **24 hours from Ready** are the same for every size. Each
download rechecks current Export and View for every original and current scope
anchor and every disclosed historical Job title. A correction moving the current
Job cannot authorize an old artifact after original assignment loss. A lost GPS
grant blocks the whole GPS artifact, never silently removes columns, and does
not block an independently authorized time artifact. Observed blocks remain
blocked; regenerate after reviewing current access. Downloaded copies are outside
subsequent access control.

Result and unavailable-download pages offer **Generate a new Timesheet export**
and **Generate a new clock GPS export** independently under current Export grants.
Each opens a fresh confirmation with currently authorized scope choices and
default filters; old stored filters are not recovered from the unavailable result.
To choose different filters, return to Timesheets and review a new export there.
View alone exposes neither regeneration link. Losing GPS authority leaves the
independently authorized Timesheet regeneration route available.

## Common fixed columns

Both version-one formats begin with these columns, in this order:

| Column | Meaning |
|---|---|
| `as_of` | Snapshot transaction instant, identical to the result's as-of instant |
| `timezone` | Snapshot company IANA timezone |
| `grouping` | `subject` or `job` |
| `group_id`, `group_name` | Stable grouping identity and bounded display label; Job zero means Standalone |
| `entry_id` | Stable Time Entry identity |
| `subject_id`, `subject` | Immutable subject identity and snapshot display name |
| `job_id`, `job_title` | Optional retained Job reference; empty identity for standalone |
| `source` | `manual` or `clocked` |
| `lifecycle` | `active`, `closed` or `void` at the snapshot |

Own access after Job loss retains only recorded Job identity/title. Current Job
names, Customer data and other context are not added. Non-own Job titles and
historical titles follow the report's independent current Job visibility.

Groups use stable numeric identity order. Within groups, selected start ascending
or descending uses stable entry identity for ties. The same authorized report
projection supplies selection, history, observations, company-day pieces and
totals inside the engine's fenced repeatable-read transaction. No second pool
read or later refresh supplies artifact facts.

## Time v1 suffix

`clock_in, clock_out, company_date, duration_microseconds, provisional, notes, revisions`

One row represents one entry's elapsed piece in a selected company date. Original
start/end instants repeat unchanged on each piece; a missing end is blank.
`duration_microseconds` is an exact integer. Sum this column for report, subject,
Job or company-date totals. Do not sum repeated interval endpoints or history.
The existing report's IANA transition-aware splitting handles short/long DST
days, skipped midnight and dates repeated by historical midnight rollback. No
payroll/display rounding modifies exported or stored duration.

Active durations are provisional at `as_of`. Void is opt-in through
`include_void=true` and contributes one row with blank company date, zero counted
duration and `provisional=false`, retaining a missing or invalid historical end.
It does not fabricate an active endpoint. Void date selection is identical to
the report, including retained-invalid interval semantics.

`revisions` is a JSON array (or `null` when no revision was recorded), repeated on
each day piece. Each revision retains `ActorID`, `Actor`, `AcceptedAt`, `Event`,
`Reason`, `Before` and `After`. States contain `user_id`, `job_id`, `job_title`,
`clock_in`, `clock_out`, `notes`, `voided_at`; they contain no GPS. Retained upgrade
history remains explicitly unknown where original attribution was unknown.
Current corrections count once, never as additional time from revision states.

## GPS v1 suffix

`clock_event, latitude, longitude, captured_at, accuracy`

One row per accepted immutable clock observation, `in` then `out` within an
entry. Entry filters use the same current interval/date/lifecycle semantics as
Timesheets; capture times are original facts, not moved to corrected instants or
filtered as a separate tracking timeline. Valid actual zero coordinates remain
zero. Missing, denied, explicitly omitted or uncaptured legacy GPS produces no
row. Accuracy is blank when not supplied and otherwise nonnegative meters.
Manual/legacy latitude fields are never converted into clock observations.
Correction and Void retain observations. GPS is not attendance proof or tracking.

All timestamps use RFC3339 with offset and exact supported microsecond precision.
UTF-8/RFC4180 quoting and reversible spreadsheet-leading-apostrophe encoding are
the shared CSV contract. An empty match is a Ready header-only file, not a failed
or pending export. Source, storage, audit or authorization failure never publishes
a partial artifact. Existing installations require no new migration, permission
grant or historical backfill. The public field API gains no export endpoint.
