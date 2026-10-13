# Asset identity and ownership (#34)

Assets have a retained, unique Number and an immutable `customer_owned` or
`company_owned` class. New registrations may specify a Number or leave it blank
for a sequence-generated `AST-…` Number. Serial numbers remain descriptive and
may repeat. Deletion is rejected; archive retains the identity and Number.

Customer-owned equipment requires a Customer. Company-owned equipment can be
unplaced, with no Customer Location, or placed with one Customer. Ordinary Edit
cannot change Number, class or Customer association. Transfer/Place/Return and
effective association history belong to #35; this migration supplies no invented
association start dates. Service Entries and Lease coverage remain their owning
domain slices, rather than synthetic history derived from Asset creation.

Creation and Edit use the existing committing authorization/custom-field adapter.
The effect and immutable audit (including full Asset before/after facts) share a
transaction. Selected Customer/Location authority is independent. Database locks
serialize new master selections against archive and vocabulary retirement.
Retained retired Type/Status selections remain visible and can survive unrelated
edits. Asset Status never controls operational eligibility.

Master Ent reads, including Job form pickers, require All Records Asset View.
`/jobs/{id}/master-context` in web and the field API uses a typed, first-link
allowlist and requires current assignment. It includes only the operational
Asset whose current Customer matches the Job, never sibling Assets, master
Custom Fields, collaboration or association history. The field API has no Asset
master creation/edit endpoint.

Asset Archive/Restore use independent grants rather than legacy Role labels.
Placed Company-owned Assets and unfinished Jobs block Asset archive. Customer
cascade previews include Customer-owned Assets with their independent View and
Archive grants; placed Company-owned equipment blocks Customer archive. Customer
restore does not restore its previously archived Assets.

## Reviewed pre-v1 cutover

Migration `073_asset_identity` is additive. Before applying any migrations in a
full upgrade, the runner inventories existing Assets and requires reviewed
identity resolutions. It checks the snapshot again under locks at cutover.
Empty/new installations need no review. Previously applied migration 073 is
restart-idempotent and does not consume a review file again.

1. Stop application writers and take the normal installation backup.
2. Build `./cmd/freefsm-asset-preflight` and run it with
   `FREEFSM_ASSET_PREFLIGHT_DATABASE_URL` explicitly set. Save its JSON output in
   a restricted file. This command does not run migrations or change data.
3. Review the inventory with the equipment owner. Fill `reviewer`, `reason` and
   exactly one `resolutions` entry per Asset, for example:

   ```json
   {
     "id": 42,
     "number": "PUMP-0042",
     "ownership_class": "company_owned"
   }
   ```

   Keep the exported `snapshot`. Preserve known unique Numbers and known classes.
   Supply explicit decisions for missing/duplicate Numbers and unknown classes;
   a Customer link alone is not evidence of Customer ownership. No default class
   is inferred. Current Customer/Location references and Job links are retained;
   `association_start: null` explicitly records that this inventory does not
   establish a historical start date.
4. Set `FREEFSM_ASSET_REVIEW_FILE` to the reviewed JSON file for normal startup or
   explicit migration. Missing, incomplete, duplicate, unattributed or stale
   reviews reject. The database also validates class/owner/location consistency
   and Number uniqueness. Resolve inconsistent legacy facts explicitly before
   retrying; do not infer historical transfers from current rows.
5. After successful cutover, retain the backup and review artifact. The database
   retains immutable inventory, resolutions, reviewer, reason and acceptance
   time in `asset_identity_migration_evidence`. That time is review acceptance,
   not installation, ownership acquisition or placement time.

Existing IDs, dates, descriptions, archived identities, Type/Status references,
Job links and other foreign references are preserved. The migration adds no
grants to existing nonprotected Roles.
