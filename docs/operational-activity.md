# Operational activity and retained history

`/activity` is the operational feed. Filters are `type` (repeatable),
`object_type` + `object_id` (one exact record), `action` (repeatable), `actor`,
`from`, and `until`. From is inclusive and Until exclusive, at midnight in the
company timezone. Exact-record filters can be combined with action, actor and
dates. Results use acceptance time descending, then stable activity ID
descending. Opaque paging cursors are bound to the filters.

Refresh activity runs a new authorized query. Updated at describes the completed
query, not a live subscription. A query failure preserves filters and withholds
previous rows until current access can be checked; it is not an empty result.
Record pages link to their filtered history and retained conversion lineage.

## Authority and facts

The shared operational projection checks canonical current record View,
archived View, current assignment and immutable Time Entry subject before
producing rows, pagination information, or links. Assigned Job Context is not a
master-history grant. Contacts and Locations have independent history authority.
Independent pricing, Pricebook, Cost, financial, settlement, time, GPS and
Service Entry facets also apply to facts/events that use them. A Role's label
does not authorize a row. Export uses this same projection.

Structured facts contain field, before, after, before_known and after_known.
Absent historic facts are explicitly **Not recorded**, rather than fabricated
empty values. Nested line items and Subtasks use closed field lists; Item Cost
and arbitrary nested metadata never enter a document pricing snapshot.
Historical author IDs remain the event's author, not the current editor.
Recorded author-name snapshots without a linked identity are labelled as such;
missing attribution is **Unknown historical actor**. Acceptance time is always
shown; occurrence time is shown only when the source recorded it. Unknown
occurrence is not replaced with acceptance time.

Comments are safe action summaries, without retained text previews. File
history can show recorded name/size, never file bytes, internal paths or removed
content. Credentials, tokens, security/configuration events and raw audit
payloads are excluded from ordinary activity. Administrative history has the
separate protected `/settings/activity/history` route with audited reads.

Conversion lineage requires independent provenance View on a currently visible
source. Hidden conversion/reversion targets may appear as labelled historical
identities without navigation, never as a new authorization anchor. Ordinary
history and exports cannot use a historical link to gain unrelated record access.

## Durable activity CSV

Use **Export activity CSV** to review filters and the All Records or Assigned
Jobs scope. Generation includes all matching rows, not only the visible page.
The confirmed scope is stored: gaining broader grants later does not widen an
Assigned Jobs request. `activity.export` is independent of browsing and requires
compatible Views. No migration grants it to an existing nonprotected Role.

Both scopes use the shared durable export service: Pending → Running → Ready or
Failed. Accepted work and receipts survive navigation/restart. Ready artifacts
are requester-only for 24 hours from Ready, with an as-of consistent snapshot
and audited generation/download. Failed preparation publishes no partial CSV.
Every status/download rechecks explicit selected records and all included
rows/facets; loss of any required authority blocks the whole artifact, including
after reversion or archive. It never silently returns a subset. Expired/blocked
artifacts require regeneration. Copies already downloaded are outside subsequent
access control.

The fixed columns are:

| Column | Meaning |
| --- | --- |
| activity_id | Stable event identity |
| accepted_at | Recorded acceptance instant, RFC3339 UTC |
| occurred_at | Recorded occurrence instant, or blank if unknown |
| actor_id | Linked event-author ID; zero when unavailable |
| actor | Authorized participant identity or explicit historical/unknown label |
| record_type, record_id | Current record or labelled provenance identity |
| record_name | Authorized current name or nonnavigable provenance label |
| action | Recorded action identifier |
| before_after | JSON array of authorized structured facts and known-value flags |

CSV quotes preserve commas, quotes, line breaks and Unicode. Spreadsheet-formula
leading text (including behind whitespace/control characters) and text starting
with an apostrophe receive one leading apostrophe. Removing that added apostrophe
recovers the original value. Ordering is acceptance time/ID descending.

## Field API

Bearer-authenticated GET `/api/v1/jobs/{id}/activity`,
`/api/v1/estimates/{id}/activity`, and `/api/v1/invoices/{id}/activity` return
`data.records` and an optional `data.next_cursor`. Documents must be Job-bound;
archived Jobs and Customer-only documents do not enter this field surface.
Exports, master-only history and administrative history remain browser surfaces.

Filters: `actor`, `action`, `from`, `until`, `limit`, `cursor`. API date filters
are RFC3339 offset instants; limit is 50 by default, 1–200 allowed. Unknown,
repeated or invalid filters and invalid/filter-mismatched cursors return 400.
Invisible targets return 404. Responses are `no-store` and use the same current
row/facet projection as browser history. Permission loss never preserves cached
authority through a cursor.

## Persistence and upgrade

Migration `074_operational_activity.up.sql` preserves existing rows, IDs and
grants and makes operational history append-only. Its single reviewed historic
backfill is `job.fields.edit`: that existing immutable audit source already
contains exact before/after values, author, target and recorded time, and had
no legacy activity writer. Source audit IDs provide unique provenance. It does
not infer historic snapshots for other old edits.

New canonical detail commands capture before/after state inside the existing
authority transaction. The protected audit insertion publishes operational
history atomically; a history/audit failure rolls back the business edit. The
former post-commit duplicate summaries are suppressed for those mapped commands.
Custom Field API replay retains one effect and one corresponding history fact.

Acceptance lives in the production-router, migrated PostgreSQL tests named
`TestActivity*` / `TestOperationalActivity*`, the field replay integration test,
and the persisted browser scripts `operational-activity.cjs` and
`activity-export.cjs`. The repository-local ticket result records exact commands,
full-suite results, failure injections and browser evidence.
