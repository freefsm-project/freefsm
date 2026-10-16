# Authorized Timesheets

`GET /time-entries` reports all matching current Time Entries, grouped by subject
or Job. It is a read-only projection, not a new record, payroll ledger, tracking
map or export artifact. Time Entry View at Own, Assigned Jobs or All scope bounds
every row, filter option, history, group and total. A selector never grants access.
Subject names are bounded Time Entry identities, not directory access. Own time
after Job access loss retains only its recorded Job identity/title, without a Job
link or other context. Other subjects' standalone entries require All authority.

## Filters and ordering

- `date_from`, `date_to`: inclusive ISO company dates. Include intersecting
  intervals and count only elapsed time within those dates, including entries
  begun earlier. The original start/end remain displayed unchanged as instants.
- `user_id`: exact subject; `job_id`: exact Job, or `standalone`.
- `source`: `manual` or `clocked`; empty includes both.
- `lifecycle`: `active`, `closed`, or `void`; empty includes all eligible states.
- `include_void=true`: explicit terminal Void opt-in. Selecting lifecycle `void`
  alone does not opt in. Void never contributes to any total and a missing end
  remains missing. An open Void is selected by its recorded start date, not an
  invented reporting end.
- `search`: case-insensitive current notes substring.
- `group_by`: `subject` (default) or `job`. Groups use stable identity order;
  Standalone is the Job group with identity zero.
- `sort`: `start_asc` (default) or `start_desc` within each group, then stable
  entry identity. Options use name/identity ordering. A requested selector with
  no authorized option remains selected with a generic label, disclosing no
  existence or hidden name.

The URL retains filters through native submission, keyboard Enter, reload and
Retry. Clear filters resets them. There is no report-page truncation. Empty
authorized results are distinct from unavailable reads and denied access.

## Exact elapsed time and coherence

The report acquires the existing authority fence **before** its repeatable-read
snapshot. Current identity/session/grants, company timezone, entries, original
revision history and separately authorized clock observations use that snapshot.
It rechecks identity before completing. Concurrent correction, Void, clock-out,
assignment or grant changes therefore produce a coherent report or explicit
failure, never partial rows with a fabricated zero total.

One displayed `Updated at` instant bounds every active duration. Active rows and
their affected totals are provisional as of that refresh, not live attendance
evidence. Closed rows use their recorded ends. All totals use exact integer
microseconds, matching PostgreSQL precision; aggregate subject time does not
overflow the shorter nanosecond `time.Duration` range. No payroll rounding is
applied. Unrepresentable/corrupt counted intervals fail explicitly rather than yielding
plausible zero or negative totals.

Company-day splitting follows actual IANA zone transitions, including short/long
days, repeated hours and skipped midnights. Date filters select the actual local
date segments, including historical rollbacks from midnight into the preceding
date; they do not assume each date is a single contiguous UTC interval.
Changing company timezone changes
presentation/day boundaries only. The read path writes no Time Entry instants,
local date facts, relationships, history or grants. Invalid stored timezone or a
failed required read makes the whole report unavailable with a filter-preserving
Retry link. No partial report or successful zero result is rendered.

Current corrections contribute once; authorized before/after revisions retain
actor, acceptance time and reason. Terminal Void history remains inspectable
only in the opt-in report or separately authorized entry detail. GPS uses its
independent compatible View scope, preserves original capture times/accuracy,
and is absent from the report without that authority. No GPS is inferred from a
corrected interval, manual entry or missing legacy capture.

## Integration boundary

`authorization.Service.Timesheet` supplies the coherent authorized projection and
explicit elapsed-microsecond values. It reuses existing Time access and snapshot
authorization rather than replacing export infrastructure. Ticket #100 owns CSV,
independent Export grants, durable export states, retention and complete download
reauthorization. The public field API remains clocks-only and gains no report,
manual Create, Correct, Void or export endpoint.

No migration or permission auto-grant is introduced. Existing correction/Void,
clock GPS, immutable history, common operation binding and uncertain-Commit
quarantine contracts remain in force.
