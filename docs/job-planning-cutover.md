# Job planning and explicit context (#33)

## Product boundary

A new Job needs an authorized active Customer and nonblank Job Title only. The existing `job_type` storage/form key now represents that title; identities and recorded titles are not renamed or synthesized. Creation uses the configured New default and the browser supplies no schedule by default. Required Custom Fields do not block minimal creation.

Planning details require `job.edit` (All Records). Relationship changes additionally require `job.relations.edit` (All Records), with independent All Records View of each newly selected master. `/jobs/{id}/links` and `/jobs/{id}/contacts` are relationship-only actions: neither planning Edit nor details-requiredness is implied. The public field API does not expose these actions. Assigned context includes the explicitly linked Contacts through its closed field allowlist, never Customer primaries or unlinked siblings.

Existing archived Project links may remain through planning edits and reopen. New archived Project selection rejects. Project Location never replaces Job Location. Archived Contact, Location and Asset dependencies must be repaired before reopening. Finished active Jobs remain actionable; relationship/planning commands do not change Status.

Customer replacement requires explicit confirmation and clears operational Location, Project, Contact and Asset links atomically. Any linked Estimate or Invoice currently blocks it, including archived/hidden records. The document owning integration must replace this conservative blocker with its full reassignment contract before enabling document-aware replacement.

## Permanent Service Entry integration

Customer replacement is authorized by planning Edit and the new Customer's View. Clearing incompatible links is an inseparable effect under AUTH-02: it does not require or grant discretionary Link/Unlink authority. Its planning audit captures the complete before/after Contact membership as well as the Job row, without claiming use of a relationship grant.

`job_customer_blockers(job_id, service_entry_id)` is an immutable provenance anchor, not a fabricated Service Entry. The Service Entry owning integration must insert its anchor **in the same authority-fenced transaction as entry creation**, before committing. Revisions/removal never delete that anchor. Customer replacement checks it under the same fence and the database repeats the blocker check. No Service Entry is inferred from notes, Asset links, or Job completion.

## Upgrade preflight and explicit review

Migration `074_job_planning.up.sql` stops its transaction before making changes if an existing Job has a missing/blank Title, missing Customer/company ownership, or an ambiguous cross-Customer Project, Location, Contact or Asset link. It does not insert a placeholder Customer, manufacture a title/status/history, choose a primary, or infer authority from Role names.

Before retrying a stopped upgrade:

1. Preserve the pre-upgrade backup and affected original rows.
2. Have an authenticated product Administrator review each affected Job and its recorded Customer/title/links. Host access alone is not product approval.
3. Record the Administrator's explicit decision and the evidence for any correction in the installation's change record. If the facts remain unknown, leave that installation's cutover stopped; do not fill guesses.
4. Apply only those approved source corrections through the prior installation's supported actions or a separately reviewed repair procedure. Retry the migration against a disposable copy first, then follow the normal backup/upgrade procedure.

Conformant Jobs retain every original column verbatim. `job_contacts` copies only the previously recorded singular Contact link, without inventing its historical author/time. Subsequent explicit relationship actions carry immutable actor/Role/permission/scope/channel audit and before/after details in the same transaction as their effects. No nonprotected Role gains permissions during this migration.

The singular Contact column is retained for existing integrations; a trigger mirrors its explicit relationship into `job_contacts`. The multi-Contact action never invents a primary or chooses an arbitrary first Contact. Future integrations should use `job_contacts` for membership, and must use the shared authority fence before mutating relationships or adding permanent dependencies.
