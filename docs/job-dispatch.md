# Reviewed Job dispatch

Open **Dispatch Job** from an active Job, or drag a calendar/dispatch card into a
slot. A drag only opens the same proposal dialog. It preserves the exact elapsed
duration and shifts an existing arrival window by the proposed start delta;
adding a person retains the other equal assignees. Nothing changes before
**Review dispatch → Confirm dispatch**. Cancel discards the proposal.

The form supports scheduled/unassigned and unscheduled/assigned Jobs. Clear both
planned times and the arrival window to unschedule, or use **Propose
unscheduling**. The independent due date remains in Job details. It is a local
date: changing the company timezone does not reinterpret it. A Job becomes
overdue on the following company date unless its semantic Status category is
Completed or Canceled. Dispatch never changes Status.

## Time and permissions

- All four operational instants are edited in the displayed company IANA
  timezone. Ambiguous daylight-saving wall times require an explicit `±HH:MM`
  offset. Nonexistent wall times and offsets inconsistent with that zone are
  rejected. Review displays the selected offset. Seconds and fractional seconds
  are preserved; drag geometry never determines persisted duration.
- `job.schedule` controls only interval/window changes; `job.assign` controls
  only the equal-assignee set. `directory.view` independently authorizes new
  person selections. Neither requires `job.edit`. Details/create cannot submit
  scheduling, assignments or legacy visits around this boundary.
- Disabled people remain visible as unavailable when retained. New assignments
  to them are rejected. Missing compatible Job View grants warn; assignment
  does not grant any authority. No primary or assignment-role semantics apply.
- Overlap requires a common assignee and intersecting half-open intervals.
  Completed/Canceled active Jobs still participate. Every current overlap must
  be explicitly acknowledged. Other simultaneous Jobs require no acknowledgment.
  Beyond-due scheduling warns but remains permitted.

## Transaction and retry contract

The production web command is `GET/POST /jobs/{id}/dispatch`. Initial POST stores
a user/Job-bound, 30-minute review of the entire proposed state, current Job
revision, company zone, assignee warnings and overlap revisions. Confirmation
submits that opaque review identity and, when needed, `acknowledge_overlaps=true`.
Submitted fields cannot alter the stored proposal during confirmation.

The command takes the shared authority mutex before reading current identity,
session, effective grants, lifecycle, assignments and overlaps. Existing Job,
directory, assignment and authority writers share this mutex. The Job and review
are locked; all selected people are checked under the same transaction. Any
new/changed overlap, changed assignment set, warning or baseline requires a fresh
review. The interval/window, normalized assignments, legacy read projection and
immutable protected audit commit together. Audit/storage failure rolls back the
whole command. Unchanged desired state preserves Job attribution. Repeating a
committed review never repeats effects or audit; the existing online save-action
receipt reconciles lost HTTP responses before retry.

Legacy POST drag URLs (`/schedule/dispatch`, `/schedule/move`,
`/schedule/calendar/move`) redirect to proposals, never mutate. The public field
API deliberately has no dispatch action, including for an Administrator bearer.

## Existing-installation cutover

Migration `076_job_dispatch.up.sql` retains each existing Job and assignment row
in `job_dispatch_legacy_inventory`. Valid timestamp-with-timezone intervals and
normalized equal-assignee relationships retain their identities and instants.
The due date is captured once in the configured company timezone into a SQL
`date`; its original timestamp remains in the inventory. No overlap
acknowledgments, historical actors, dispatch times, or nonprotected Role grants
are invented.

Unknown timezone, incomplete/reversed interval, invalid arrival window, legacy
visits, assignment labels/primary semantics, and unmapped legacy assignment JSON
stop the migration transaction before recorded data is changed. The error names
the affected Job IDs. Inspect the original values and resolve them with the
existing product Administrator before retrying. Do not guess a timezone, offset,
person, interval or historical acknowledgment.

For an approved mapping, first create **the exact
`job_dispatch_legacy_approvals` table DDL at the start of the migration** in the
pre-upgrade database. Save one row per reviewed Job:

```sql
INSERT INTO job_dispatch_legacy_approvals
  (job_id, source, plan, reviewer_id, reason)
SELECT j.id,
       jsonb_build_object(
         'job', to_jsonb(j),
         'assignments', coalesce((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id)
                                FROM job_assignments a WHERE a.job_id=j.id), '[]')),
       :reviewed_plan::jsonb,
       :existing_product_administrator_id,
       :review_reason
FROM jobs j WHERE j.id=:reviewed_job_id;
```

`reviewed_plan` must explicitly contain every key:

```json
{
  "start": "2020-11-01T01:30:00-05:00",
  "end": "2020-11-01T02:15:00-05:00",
  "arrival_start": "2020-11-01T01:00:00-05:00",
  "arrival_end": "2020-11-01T01:45:00-05:00",
  "due_date": "2020-11-02",
  "assignee_ids": [42, 57]
}
```

Use JSON `null` for absent interval/window/date values and `[]` for no assignees.
Every instant requires an explicit offset. All recorded normalized assignees
must be retained in this mapping; an ordinary post-cutover dispatch can remove
one prospectively. Existing assignment IDs and historical labels are preserved;
labels are no longer operational semantics. Legacy visits remain recorded
historical source, never additional schedules. New relationships may only name
reviewed same-company people. The reviewer must currently be an enabled product
user with the protected Administrator identity, not merely a legacy Role label
or host operator. Stale/incomplete/invalid mappings reject atomically. The source,
reviewer, review time and reason become immutable cutover evidence; they do not
claim historical dispatch approval.

An explicitly recorded legacy user ID may be normalized while that user is
disabled; it remains unavailable and is never enabled by migration. This narrow
cutover exception does not permit new disabled selections. The migration holds
the shared authority fence and restores the normal availability trigger in the
same transaction; failures roll back the temporary trigger change as well.

Take the ordinary database/files backup before upgrade. A stopped migration
does not serve a partially upgraded application. Retry only after the source and
explicit mapping agree; use the documented binary-plus-backup rollback process.

## Acceptance evidence

`internal/handlers/job_dispatch_integration_test.go` exercises production routing
and migrated PostgreSQL: review/cancel boundary, exact cross-midnight interval,
DST gap/folds, independent grants, unavailable people, incompatible grants,
current overlap acknowledgment, simultaneous scheduling, commit-time authority
loss, audit rollback, due-date independence and same-review replay. The Chromium
journey uses real CSRF/session middleware, calendar drag, shared dialog, phone
layout, keyboard dismissal, validation retention and a lost post-commit response.
`internal/database/job_dispatch_migration_integration_test.go` proves failed
cutover preservation and explicit reviewed mapping without historical invention.
