# Custom Field evolution (#30)

## Definitions and retained history

Settings → Custom Fields edits availability, retired option identities, help text,
and PDF inclusion independently of pairing. Retiring a field retains its values,
removes its ordinary entry controls, suspends requiredness/defaults and rejects
submitted edits, including forms opened before retirement. Reactivation changes
future validation; it neither backfills records nor retries rejected operations.

Options retain their exact stable text identities. `retired_options` is a JSON
array of identities that remain in `options`; their names remain reserved.
An already-stored retired selection may remain, or be removed. New selection is
rejected. Clear a prospective default before retiring the option it selects.

Type, precision, deletion and option validation retain the foundation's archived
and hidden-record checks. Captured pairing references also prevent deletion/type
changes when no value was stored. Definition edits cannot invalidate the current
shared value of an active conversion. Definition/value writes and conversion
share the authorization fence; effects and immutable audit commit together.

## Conversion and recovery

Convert to Invoice opens a required-input preview. Paired source values take
precedence, unpaired defaults initialize the new Invoice, and supplied Invoice
values require Invoice Edit independently of Convert and the existing creation
authority. Requiredness is checked before numbering, relation movement or hiding
the Estimate. The command rechecks the current definitions and permissions.

Each new cycle preserves `field_pairs` and `field_definitions` in its immutable
source snapshot. Pairing is explicit, one Estimate to one Invoice, with identical
type, precision and ordered option text; names/help/PDF choices may differ.
Retired definitions do not establish new-cycle pairs. Existing cycles retain
their captured pair even after current keys change. Invoice writes must remain
representable under the captured source and current source definitions.
Reversion preserves original Estimate-only values, transfers current shared
values using that cycle's pair, and defers newly required values to the next
applicable details save. It never applies creation defaults during recovery.

Pre-v1 cycles are preserved verbatim. Missing historical pairing blocks dangerous
definition edits, field edits and reversion; current names never supply evidence.
The recovery error identifies `/settings/custom-fields/cycles/{uuid}/review`.
The review requires fields configuration authority and All-record Estimate and
Invoice View/provenance grants. Supply independently reviewed evidence and a
reason, for example:

```json
{
  "field_pairs": {"900": 901},
  "field_definitions": [
    {"id": 900, "name": "Original reading", "field_type": "number", "precision": 2, "options": "[]"},
    {"id": 901, "name": "Invoice reading", "field_type": "number", "precision": 2, "options": "[]"}
  ]
}
```

The review validates one-to-one identities, types, precision, option order,
recorded source values and current shared values. An explicitly reviewed empty
pairing is `{ "field_pairs": {}, "field_definitions": [] }`. Review evidence,
reason, actor and time are appended separately and are immutable. The original
snapshot and source values are never replaced. A conflicting/repeated review
rejects. An authorized recovery is a separate subsequent action.

PDF inclusion defaults off, including on upgrade. New PDF Versions capture the
opted-in field labels/values and include them in their source fingerprint. Toggling
inclusion or changing rendered labels/values makes earlier versions stale without
changing their bytes. Historic versions remain selectable under existing grants.

## Reviewed Job-field API reference

`GET /api/v1/jobs/{id}/custom-fields` returns `data.values` plus `data.revisions`,
a map from definition IDs to opaque revision strings. Tokens change only when
that field value changes, including ABA; unrelated fields and cosmetic definition
edits do not invalidate pending intent. Treat the initial all-zero token as opaque.

Offline/reviewed clients submit:

```json
{
  "operation_id": "30666666-6666-4666-8666-666666666666",
  "revisions": {"1": "00000000-0000-0000-0000-000000000000"},
  "values": [{"definition_id": 1, "value": "Reviewed reading"}]
}
```

`PATCH` checks all changed revisions and current validation atomically. Changed
required fields must be supplied; unrelated missing required fields remain
deferred. A successful same-intent replay has one effect. Desired-state no-op
does not change revision or attribution. A changed-intent operation-ID reuse,
stale revision or invalid field returns 409 with an error code,
`recovery: "new_reviewed_operation"`, and currently authorized comparison data.
Terminal failure is durable: reactivating a field cannot turn its rejected
operation into success. Repair needs a new operation ID after review.
Storage/audit failure remains nonterminal and retryable with the original ID.

`GET /api/v1/jobs/{id}/custom-fields?operation_id={uuid}` retrieves that
user/client's nonsensitive immutable receipt, including after parent scope loss.
It does not return intent or field values. Current data still requires current
Job View; mutations independently require Job Custom Field Values Edit.

The earlier immediate-value PATCH shape remains accepted for compatibility; it
does not provide the reviewed/offline reconciliation guarantees. New offline
clients must send both operation identity and per-changed-field revisions.
This is a field-specific reference boundary, not the complete future batch-sync,
lease, cache-purge or public OpenAPI implementation.

## Migrations and verification

Migrations 070–072 add definition metadata, current Job-field revision tokens,
immutable field receipts and retrospective review evidence. They do not change
existing values, definition IDs, source snapshots, relationships or Role grants.
Historical revisions are not invented; seeded tokens identify current state only.

Executable production HTTP/API cases:

- `field_evolution_integration_test.go`: retirement/reactivation, option retention,
  precision/order compatibility, captured pairing and required conversion input.
- `field_offline_integration_test.go`: terminal retirement, stale revisions,
  cosmetic changes, replay, audit rollback and receipt nondisclosure after loss.
- `field_pdf_integration_test.go`: downloaded PDF opt-in and immutable older bytes
  (requires `pdftotext` for integration verification, not application runtime).
- `typed_fields_review_integration_test.go`: actual legacy source/cycle preservation,
  refusal to guess, explicit immutable review and recovery.
- Existing typed-field HTTP concurrency/audit/assignment/settlement tests continue
  to run against the same domain transaction boundaries.
- `TestTypedFieldsPersistedBrowser`: persisted Chromium/CSRF journey, including a
  form opened before retirement and no replay after reactivation.

Run `make lint`, `FREEFSM_TEST_DATABASE_URL=<disposable PostgreSQL> make test`, and
the opt-in browser test with `BROWSER_TESTS=1`, `NODE_PATH` pointing to the existing
Playwright installation, and `BROWSER_EXECUTABLE` pointing to Chromium.
