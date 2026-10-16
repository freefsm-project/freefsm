# File metadata in Job synchronization

`GET /api/v1/sync/jobs` includes a `files` collection on newly created snapshots
and deltas. It contains Job-direct Files and independently authorized Files on
active Job-linked Estimates and Invoices. Parent View controls metadata; File
Download, Rename and Remove grants independently control capabilities. The field
API's original-uploader restriction also applies when a caller has office grants.

Each entry contains its existing File ID and exact parent, original uploader,
display name, fixed extension, size, media type, recorded publication identity,
opaque revision, removal actor/time and capabilities. IDs are decimal strings to
preserve installed 64-bit identities in JavaScript. A null publication identity
means unrecorded historical evidence; synchronization does not manufacture one.

`file_parents` lists the independently visible original Job/document parents and
their current Upload capability. This permits explicit publication staging even
on a parent with no published Files. Losing a disclosed parent also invalidates
cached pages; descriptor identity is never permission to follow a moved File.

Verified temporary transfers are not ordinary Files and do not appear here.
Publication and rename/removal produce metadata deltas. Removed Files retain
metadata and lose Download/Preview capability. Image and PDF types alone expose
Preview capability. Capabilities are observations, not authority for a later
request; every content request and mutation still checks current access.

When a previously disclosed File leaves its original visible parent, old pages
and already-materialized delta successors become unsafe. The server returns the
existing `resnapshot_required` / `purge_and_resnapshot` disposition rather than
replaying the old name or capabilities. Clients must discard inaccessible cached
representations. A new snapshot does not authorize retargeting pending operations.

This projection does not add File bytes to synchronization JSON, alter the
website's online-only behavior, or replace the existing transfer and publication
service. See [File publication](files-publication.md),
[transfer integrity](api-file-transfers.md), and the local OpenAPI `SyncFile`
schema for the underlying contracts.
