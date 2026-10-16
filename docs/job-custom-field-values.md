# Independent Job Custom Field Values (#41)

The API snapshot/batch/encrypted-reference extension is documented in
[Offline Job Custom Field Values](api-job-field-sync.md).

Job View reveals current and retained retired field values. `job.fields.edit`
at Assigned Jobs (`J`) or All Records (`A`) is the sole value-edit permission;
planning Edit and definition management are independent. Fresh Technician and
Dispatcher grants use J and A respectively. The existing authorization migration
seeds nonprotected defaults only on a fresh installation; upgrades and this
ticket's migration add no nonprotected grants.

## Web and API commands

The Job's **Edit Custom Field Values** link opens a dedicated online editor.
It submits only changed controls, each with its originally displayed opaque
revision, and an operation ID. Disjoint field changes coexist; stale same-field
changes (including ABA) reject the entire patch. There is no force or text merge.
The generic online receipt wrapper defers concurrency checks to this field
boundary instead of comparing the whole Job. Main-details forms retain their
planning revision and additionally require original revisions for changed fields.

The online editor and main-details saves validate all currently applicable
required fields. `PATCH /api/v1/jobs/{id}/custom-fields` validates changed values
only and defers unrelated missing required values. This does not create a new
creation/conversion exemption. Existing minimal Job creation and lifecycle
recovery exemptions remain; separate Status, notes and Subtask actions do not
invoke details validation.

Current type, precision and option validation runs inside the committing
transaction. Cosmetic definition changes do not invalidate value revisions.
Retired fields reject changes and remain viewable; retired options cannot be
newly selected but existing selections may remain. Reactivation never retries
a rejected operation or backfills a default.

`GET /api/v1/jobs/{id}/custom-fields` returns values and revisions. PATCH requires
`operation_id`, `revisions` and `values`; the unguarded legacy shape is rejected.
Receipt lookup uses the same path with `?operation_id={uuid}`. The web editor
also exposes receipt lookup on `/jobs/{id}/custom-fields?operation_id={uuid}`.
Receipts are bound to the authenticated user/client and exact target/intent.
Same-intent replay never writes values or duplicates attribution. Access loss,
invalid input and revision conflicts remain terminal after repair of the domain.
Audit/storage failure rolls back values and receipt together and remains
retryable with the same identity.

After a conflict, the authorized web page shows current values beside retained
proposed values. **Review and save as new operation** is an explicit new-ID
submission. Discard abandons intent. Receipts retain nonsensitive disposition
and target identity after Job visibility loss; values/comparisons require current
Job View. The authorization fence rechecks session, grants, assignment and archive
state before commit. A no-op changes neither revision nor attribution.

## Upgrade and evidence

Migration `076_job_field_terminal_receipts` removes only the receipt target FK:
a terminal request for a nonexistent Job must be recordable without inventing a
Job relationship. All earlier receipts, identities, values, option identities,
relationships and audit remain intact. Receipt update/delete protections remain.

The shared typed-field preflight continues to stop unsupported stored types,
orphan/ambiguous field identities and missing precision for explicit review,
without coercion, defaults, guessed mappings or revision authors. See
`typed-custom-fields.md` for reviewed metadata preparation. No missing mapping
is inferred from a field label. Historical activity attribution remains recorded;
seeded revision tokens describe current state, not fabricated past authorship.

Executable acceptance:

- `TestJobFieldsRequireReviewedOperationHTTP`, `TestJobFieldsWebRejectsUnguardedSave`
  and `TestJobFieldsPlanningRequiresFieldRevisionHTTP`: no unguarded transport.
- `TestJobFieldsDisjointAndABAWeb`, `TestJobFieldsConcurrentSameAndDisjointPatchesHTTP`:
  field-scoped concurrency and atomic rejection.
- `TestJobFieldsCommitTimeAuthorityRacesHTTP`: real PostgreSQL barriers for grant,
  assignment, archive and session loss; no partial values or successful audit.
- `TestJobFieldsDeniedOperationStaysTerminalAfterAssignmentRepair`: explicit
  new-intent recovery rather than replay on regained authority.
- `TestJobFieldsExecutableReferenceHTTP`: executes the shipped Node reference
  module against production API/DB; atomic validation, replay, ID misuse,
  explicit repair, lost-response reconciliation, immutable receipts/audit/no-op.
- `TestJobFieldsPersistedBrowser`: CSRF-enabled assigned worker, two tabs,
  disjoint changes, retained comparison/repair, independent denials and 320px.
- `TestJobFieldsLegacyUpgradePreservesFactsAndStopsUnreviewedMappings`: genuine
  pre-v1 schema, exact recorded values/option identities/attribution, rejected
  missing mappings and invalid types, protected-only upgrade grants.
- Existing typed/evolution/requiredness/retained-global/audit-rollback tests remain
  part of acceptance, including their persisted browser journeys.

Run mandatory `make lint` and `FREEFSM_TEST_DATABASE_URL=<disposable DB> make test`.
The full Makefile test command retains `-race`. Opt into persisted browser tests
with `BROWSER_TESTS=1`, `BROWSER_EXECUTABLE` and the existing Playwright `NODE_PATH`.
The executable reference uses Node's built-in fetch and needs no npm runtime in
the application.
