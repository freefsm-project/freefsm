# Asset Service Entries

Service Entries record explicit service facts for one Asset through one Job.
They are independent of shared Field Notes and never gate Job completion.
An entry stores its original Customer, canonical Job–Asset link and association
period. Recording an entry permanently registers the existing Job Customer
blocker. Revisions append content, actor and acceptance time; responsible users
remain stable user identities, including after rename or disablement.

## Current interfaces

Assigned Job Context links to `/jobs/{job}/assets/{asset}/service-entries`.
Create and Revise require their independent Service Entry grants, compatible
View grants, an active Job/Asset and the currently operational original link.
Assigned scope also requires current Job assignment. Office lifetime history is
`/assets/{asset}/service-entries`; Customer-bounded history is
`/customers/{customer}/assets/{asset}/service-entries`. Neither history exposes
other Job facets or current new-Customer details through a historical link.
Export remains independently granted; these read routes are not export endpoints.

The field API provides the same Job-bound collection:

```text
GET  /api/v1/jobs/{job}/assets/{asset}/service-entries
POST /api/v1/jobs/{job}/assets/{asset}/service-entries
POST /api/v1/jobs/{job}/assets/{asset}/service-entries/{entry}
GET  /api/v1/jobs/{job}/assets/{asset}/service-entries/{entry}/revisions
GET  /api/v1/service-entry-operations/{operation}
```

The GET data contains `entries` and the exact `link_id`.

An entry currently revisable through this exact Job/link also includes
`responsible_user_ids`. A fresh client uses those stable identities from its read
when retaining responsibility during a revision; names are neither unique nor
identity keys. These IDs are omitted from read-only, cross-Job and lost-original-
link rows and from the shared Customer/Assigned Context history projection.
Revision forms similarly retain the current entry's responsible users even after
unassignment, rename or disablement. Those retention choices are entry-specific
and do not become eligible choices for new entries or unrelated revisions.

Mutations carry:

```json
{
  "operation_id": "42000000-0000-4000-8000-000000000001",
  "link_id": 123,
  "service_date": "2026-10-14",
  "summary": "Replaced seal and inspected operation",
  "responsible_user_ids": [7]
}
```

The revision-history route exposes retained service content, responsible names,
actor and acceptance time under the same bounded authority. Revision also
requires the originally read opaque `revision` UUID. Optional
`occurred_at` records client occurrence independently from acceptance. A new
operation cannot silently follow a replacement link, even if it connects the
same Job and Asset after unlink/relink. Stale revisions reject, including ABA.
No force, merge, master maintenance or production offline client is introduced.

Domain receipts retain terminal code, stable entry identity and acceptance time
without summaries, responsible names or Job context. An identical retry returns
the original receipt; changed intent under the same ID conflicts. Audit/storage
failure rolls back effect and receipt, permitting unchanged retry. Receipt lookup
still requires current authentication and the original user/client binding;
resource/grant loss does not erase nonsensitive receipt evidence. Terminal repair
requires explicit review and a new operation ID.

Service Entry commands use the composition owner's `authorization.AdmitOperation`
inside the effect/receipt transaction, binding deployment, user, client, operation
family and exact intent. The domain receipt is not a separate shared registry.
Read synchronization remains ticket #77. Acceptance of the composed integration
is recorded separately from this domain's source verification.

## Preserved evidence

Migration `085_asset_service_entries.up.sql` is additive. It does not interpret
legacy linked Job text or shared Field Notes as service, infer service dates or
responsible users, rewrite existing identifiers, or grant nonprotected Roles
permissions. Existing reviewed association cutover still governs legacy
relationships. Unknown legacy narrative stays narrative rather than appearing
as an invented bounded service row. No Lease conclusion or details are supplied;
the purpose-limited coverage indicator belongs to ticket #75.

Association changes validate all recorded service revisions against their pinned
Customer periods even after Job unlink. Transfer and archive share the existing
authority fence with entry mutations. Customer replacement and first creation
also contend on the same Job lock; incompatible Customer outcomes cannot both
commit.

## Validation

`TestServiceEntry*` exercises production browser/HTTP/API, the executable OFF-13
reference, immutable revisions/identity, authority loss, transfer/relink and
Customer races, audit rollback and additive upgrade preservation. Run with a
disposable `FREEFSM_TEST_DATABASE_URL`; enable `BROWSER_TESTS=1` and provide the
project's Chromium/playwright-core environment for the persisted browser case.
Full ticket acceptance additionally requires the common registry composition and
the owning criteria/evidence matrix, not merely these focused tests.
