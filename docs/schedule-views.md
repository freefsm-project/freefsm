# Schedule views

The schedule is an operational projection of active Jobs. `job.view` controls
every view; Assigned Jobs users see only their currently assigned Jobs. A Role
name, saved filter, previous page or known Job ID never grants access. Directory
View independently controls unassigned directory choices; Job participant names
remain part of the authorized Job card.

## Reading and planning

List and dispatch retain an unscheduled backlog, including assigned Jobs with no
dates. Finished and unassigned Jobs are included by default. Status, assignment,
search and sort filters are explicit. Links preserve the requested filters and
period; a per-user, HttpOnly presentation cookie remembers the last query on
return. It stores no Job representations or authorization decisions. Existing
database tab/period preferences remain the fallback. Phone first navigation
defaults to list/day; explicit navigation retains the other views.
The automatic phone fallback and a remembered return do not rewrite the recorded
database preference. Back navigation restores contained calendar/dispatch/map
scroll positions as well as the window position, after reauthorization.
The cookie only initializes a visit: each rendered workspace binds subsequent
refreshes to its server-normalized query. Bare visits acquire an explicit history
URL so another tab's remembered query cannot change their results on reload/Back.

Cards use the same authorized snapshot for title, permitted Customer/Location
context, full interval, arrival window, semantic Status, every equal assignee,
unavailable people, overdue and common-assignee overlap indicators. Conflict
calculation excludes unauthorized Jobs before computing the indicator. Context
grants expose only the first-link operational allowlist on a currently assigned
Job, never master financials or unrelated records.
List/day retains the latest Invoice link and effective-status badge only with
independent current Invoice View, including its narrower Assigned Jobs scope.

Dragging is a proposal for the existing **Dispatch Job** review. It cannot write
directly. Open a Job and use **Dispatch Job** for the keyboard/non-drag path.
Independent schedule/assignment grants, current review, commit-time locking and
atomic audit remain the [reviewed dispatch contract](job-dispatch.md).

## Failure and return

The map uses service Location coordinates only, including valid `(0, 0)`.
Unmapped cards explain missing Locations, missing coordinates, unavailable
context or provider failure. Provider failure does not change saved coordinates
or disable manual address, list, calendar or dispatch workflows. Maps respect
reduced motion.

Refresh clears authority-bearing cards before loading. Failure leaves a distinct
unavailable/retry state with filters retained, rather than a successful empty
result or stale links. Returning from a hidden or back/forward-cached page
reloads current authorized data. Only presentation/scroll state is retained.
The website remains online-only; a rendered card is not continuing authority.
Navigation is built from the validated query even on an initial database error.
Calendar, month keyboard and dispatch handlers are loaded with the workspace,
independently of results, so successful Refresh/return restores working controls
without evaluating scripts from response documents. Drag still only opens review.

## Upgrade

This view change does not rewrite saved schedules, intervals, assignee IDs,
display preferences, coordinates or grants. The existing `076_job_dispatch`
preflight stops ambiguous/invalid historical data non-destructively and requires
an explicit current Administrator-reviewed mapping. See the exact review-table
and source-preservation procedure in [job-dispatch.md](job-dispatch.md). Do not
guess missing dates, normalize recorded instants, or invent historical overlap
acknowledgments. Runtime incomplete intervals are labelled for reviewed repair.

## Acceptance seams

- `internal/handlers/schedule_acceptance_integration_test.go`: migrated real
  PostgreSQL and production authenticated HTTP; visibility, presentation state,
  failure, waiting grant/assignment races and persisted Chromium navigation.
- `internal/handlers/job_dispatch_integration_test.go`: the shared browser/API
  and transactional dispatch boundary, including audit rollback, replay and
  commit-time authority.
- `internal/handlers/schedule_recovery_browser_test.go`: two-tab remembered-query
  coherence, assignment loss, and real initial SQL failures followed by working
  calendar/month/dispatch proposals and period navigation in Chromium. The
  headless visibility lifecycle is explicitly dispatched; requests and database
  results are real, and unchanged persisted intervals are checked after proposals.
- `internal/database/job_dispatch_migration_integration_test.go`: stopped
  ambiguous cutover, explicit review, preserved preferences/coordinates/identity
  and no nonprotected permission grants.

Executed commands and results belong in the ticket's durable result report;
this document does not certify an unexecuted browser or platform matrix.
