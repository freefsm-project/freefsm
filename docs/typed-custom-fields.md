# Typed Custom Fields (#29)

## Current contract

Definitions support Customer, Project, Job, Asset, Estimate, Invoice and the
future Lease domain. Contact, Location, Item and Time Entry are excluded.
`configuration.fields.view` and `configuration.fields.manage` are independent
configuration grants. They confer no parent access. Existing permission catalog
entries are reused; this migration grants nothing to nonprotected Roles.
The closed parent/type vocabulary is owned by `customfields/catalog.go` and is
shared by configuration, migration review, record adapters and field UI routing.

Parent basic View reveals values. Parent Edit permits value entry, except Job
values require `job.fields.edit` independently of planning Edit. Each existing
parent has `/<parents>/<id>/custom-fields` GET/POST entry, in addition to its
main-details integration. This allows a parent editor to edit values without
requiring unrelated Customer/pricing/configuration access. Assigned-Job grants
are checked against current assignment and active Job state. Closed Customer
Context continues to exclude master Custom Fields.

General record visibility uses the read-only `authorization.ParentAccess`
primitive, which neither acquires the mutation fence nor fetches field values.
The same scope rules are reused with locks by committing mutations. Combined
`job.edit/A` and `job.fields.edit/J` grants are resolved independently and each
effect is audited under its own scope. Changes that remove their own required
assignment/binding before commit reject the whole details save.

The seven types and wire/storage representations are:

| Type | Representation / validation |
| --- | --- |
| `text` | A single-line string |
| `textarea` | A multiline string |
| `number` | An exact decimal string, with an explicit maximum of 0–18 fractional places; no floating-point conversion, rounding or exponent notation |
| `date` | A valid local `YYYY-MM-DD` date |
| `checkbox` | `"true"` / `"false"`; empty is **Not set**, never implicit No |
| `select` | One exact option key, or an empty string |
| `multiselect` | An array of distinct exact option keys, including an explicit empty array |

Existing option text is retained as the stable, field-local option key. Option
keys are not trimmed, case-converted, translated or replaced in stored values.
Normalized names must be unique per parent type, and normalized option names
per definition. Changing/removing a referenced option is rejected. This keeps
pre-v1 option associations and existing conversion pairing lossless; evolution
and separate mutable option labels belong to #30.

Defaults apply only to creation and only when a field is omitted. Explicit
empty, No and empty selections take precedence. Requiredness is checked on the
next online details/value-form save, including when every field input is
omitted. Minimal Job creation and future minimal Draft Lease creation defer
missing required values while validating supplied values. Lifecycle actions,
restore and recovery reversion do not invoke details validation or defaults.
Invoice value edits take the Invoice row lock and reject active settlement;
independent collaboration and settlement actions remain available.

The current field API provides `GET` and `PATCH /api/v1/jobs/{id}/custom-fields`.
PATCH accepts `{"values":[{"definition_id":123,"value":"false"}]}` and returns
the normal API `data` envelope. A multiselect uses a string array. An atomic
changed-value patch checks requiredness for changed fields; missing unrelated
required fields defer to the next online details save. Invalid input returns
422 with `error.code=invalid_custom_field`, the failing definition identity and
field-specific message. Master and definition CRUD are not field API surfaces.
The owning offline/API tickets add their revision/receipt reconciliation
protocols around this shared validation boundary.

## Transactions and integration boundary

`internal/customfields` owns typed validation, creation/details/changed-value
modes, reference safety and legacy inspection. Its validator must run inside the
parent's committing transaction after acquiring the authorization fence and
parent lifecycle/assignment/settlement locks.

`authorization.SaveField`, `SaveCustomer`, `SaveFields`, `PatchFields` and
`DetailsTransaction` share the foundation advisory transaction lock `190019`.
The definition statement trigger also takes this lock, covering insert phantoms
and compatible direct definition writers. Definitions are read after locking.
The current principal/session is rechecked before commit. Parent mutations and
immutable `authorization_audit` entries commit together; audit failure rolls
back values, other details and definition changes. Definition audit captures
actual before/after state; value audit captures actual before/after values.

Existing Ent domain builders run on the same pgx transaction through the
`services/field_transaction.go` adapter. Nested Job transactions become
savepoints, not independent commits. The original domain validation is retained.
Product details handlers call explicit `SaveDetails` actions, which require an
authorization context regardless of whether a field patch is present. A nil
patch only means preserve stored values. Independent subtask completion uses its
own entry point and does not acquire details-requiredness semantics. Definition
handlers fail closed without an actor and have no legacy mutation fallback.

When a field validation error interrupts another domain's details save, the
correction page retains every submitted non-field control verbatim (including
repeated controls and structured JSON), with a fresh CSRF token. Only currently
authorized Custom Fields are editable there. Resubmission returns to the original
domain action and revalidates the entire request. There is no second list of
domain fields or fabricated textarea representation of identifiers/dates/JSON.

Lease has no existing record implementation here. Its owning ticket supplies
the Lease Manage/lifecycle adapter and uses `customfields.Creation` for minimal
Draft creation, `Details` for online main details, and bypasses details validation
for lifecycle recovery. No Lease table or speculative CRUD was introduced.
Retirement/reactivation, independent option-label evolution, and the full
conversion/default-entry workflow belong to the following evolution/domain
slices. They must preserve this transaction and non-backfill contract.

## Upgrade / explicit legacy review

Migration `058_typed_custom_fields` adds nullable precision and a prospective
default, plus normalized name uniqueness and transaction/settlement guards.
It does not rewrite parent values, definition IDs, option keys, archived or
conversion-hidden records, or immutable conversion snapshots.

Before applying schema changes, migrated PostgreSQL preflight inspects all six
existing parent tables and conversion source snapshots. It stops with explicit
record/definition identities for unknown types, missing numeric precision,
incompatible values, orphan/wrong-parent/duplicate identities, malformed values,
ambiguous options or normalized name collisions. Missing historical required
values are not classified as past validation failures. Failed preflight does
not record the migration or change the data.

Legacy numbers never had a recorded precision. An operator must obtain an
explicit product decision for each numeric definition before upgrade. The
review can add the nullable metadata column in the old schema and record that
decision, for example:

```sql
ALTER TABLE custom_field_definitions ADD COLUMN IF NOT EXISTS precision integer;
-- Example only: use the actual reviewed definition ID and chosen precision.
UPDATE custom_field_definitions SET precision = 3 WHERE id = 901;
```

Rerun normal migration. Preflight still rejects every value incompatible with
that decision; it never truncates, rounds, applies defaults or guesses a scale.
For other diagnostics, resolve the identified ambiguity explicitly before
retrying. Do not edit immutable conversion snapshots to conceal an incompatibility.

## Verification

Production-router tests are in `internal/handlers/typed_fields_integration_test.go`;
upgrade tests are in `internal/database/typed_fields_migration_integration_test.go`.
The opt-in persisted Chromium journey is
`TestTypedFieldsPersistedBrowser`, including CSRF, all seven types, correction
without partial persistence, explicit No, multi-option retention, Job values,
and required Asset/Project inline-create retry. Review regressions also cover
legacy document editor grants, mixed Job scopes, omitted field patches, complete
correction payloads, paired hidden-document/snapshot upgrades, and actual HTTP
Job archive versus Job-scoped document-save concurrency.

Details forms use native navigation so 422 validation responses remain visible.
The browser journey also exposed and verifies the shared root-path CSRF cookie
needed for cross-parent inline creation after a deep-link entry.
