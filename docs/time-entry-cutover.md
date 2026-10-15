# Time Entry correction and Void cutover

Migration `085_time_entry_history` is additive. It preserves identifiers, subjects,
intervals, notes, Job links, creation source, existing audit and all GPS. It grants
no permissions. Existing nonprotected Roles retain exactly their current grants.

The accepted Service Entry migration is `085_asset_service_entries`; this
migration is `085_time_entry_history`. The runner sorts complete filenames and
keys `schema_migrations.name` by the complete filename without `.up.sql`, not by
the numeric prefix. Both identities are retained: fresh installation runs Asset
Service Entries first, while an installation that already applied either one
adds only the missing migration. Their domain tables and history are independent;
renaming a previously applied identity would incorrectly attempt it again.

Every installed entry receives one immutable `retained_at_upgrade` snapshot at
the actual migration time, with no invented historical actor. This records the
projection known at cutover, **not** an assertion about its original creation or
an earlier revision. Existing clock observations remain independent evidence.
The Job title snapshot is the title known at cutover; new links capture their
then-current title. A later Job rename never rewrites that retained identity.

The Product Administrator reviews retained interval warnings against source
records before relying on the affected time. `time_entry_review_required` exposes
invalid/future intervals and overlapping nonvoid entries without altering them.
Authorized entry details and list rows display a review warning without exposing
the other conflicting entry. Resolve each conflict with an independently granted,
reasoned correction, or terminal Void and a replacement closed manual entry.
Before/after facts, actor identity and acceptance time are retained atomically.
New writers continue to reject overlap while review is pending; they never trim,
split, backfill GPS, fabricate an end, or convert Job time to standalone time.

The previously installed053 sole-active migration still stops upgrades with
ambiguous multiple active entries. Do not bypass it or restore the old inferred
clock-out behavior. Preserve the database and obtain explicit source-record and
Product Administrator review of that earlier cutover before proceeding.

Void ends the active effect without filling a missing clock-out. It is terminal,
retains recorded GPS, and is excluded from active clocks and ordinary lists.
`/time-entries?include_void=true` explicitly includes retained Void records; their
duration is labelled excluded rather than contributing time. The field API
continues to permit clocks only. Responsive web owns manual Create, Correct and
Void, each under its independent O/J/A grant.
