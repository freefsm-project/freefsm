# Encrypted field document creation reference

`internal/api/v1/reference/document-creation-client.cjs` exports
`DocumentCreationClient` and the existing `ProtectedStore`. It extends the
protected field client, using its platform Secret Service key, AES-GCM store,
exclusive durable writer marker, offline authorization lease and recovery owner.
It is an executable native protocol reference; the website remains online-only.

## Review and stage

1. Initialize a private owned directory and authenticate through the existing
   lease protocol. `synchronize()` reads complete authorized Job and Item
   snapshots before publishing either cache. `synchronize(true)` applies their
   respective coherent deltas. Item identity and permitted base/Pricebook fields
   are retained; Cost is excluded even when the caller may view it separately.
2. `view()` exposes current authorized inputs. Each Job's `document_creation`
   exposes independent Estimate/Invoice capabilities, exact reviewed Customer
   and Job IDs, company calendar date/timezone, tax, definitions and defaults.
   Items remain a separate catalog, never Assigned Job Context.
3. `selectItem(opaqueItemID, decimalQuantity)` returns an editable customer line
   with retained Item provenance and exact decimal price. `prepareCreation(kind,
   opaqueJobID, values)` resolves a reviewable document. Explicit values win;
   Customer non-null overrides beat company defaults, while null inherits.
   Empty optional dates mean No Due Date/no validity. Invoice date defaults to
   the discovered company date; offsets use calendar days. Line descriptions
   and typed field defaults are resolved before review, including multiselect
   arrays and explicit checkbox false. Retired definitions supply no defaults.
4. After the human reviews those values, `stageCreation(kind, opaqueJobID,
   reviewed)` saves a new operation UUID, offline document UUID, original exact
   binding, editable content and reviewed input/provenance snapshots encrypted.
   It performs no network request. No default, amount, identity or date is
   silently recomputed during submission. Missing title/binding or unresolved
   content rejects staging. Server validation remains authoritative for current
   fields/options, exact commercial arithmetic, assignment and active inputs.

`submitCreation(operationID)` explicitly submits one pending intent. The server
assigns an immutable per-type number and creates Draft only. An Item price delta
does not reprice pending content. Archived/unauthorized selections reject the
whole creation. Creation never finalizes, sends, settles or falls back to a
Customer-only document.

## Reconciliation and authority

The client durably marks an operation unknown before contact. After transport
loss, `reconcile(operationID)` queries the original commercial receipt; it never
creates new intent. Commercial receipts use `outcome`, unlike execution receipts
that use `code` and acceptance timestamps. Accepted receipts contain safe effect
identity and disposition; private content is read separately under current
authority. A missing receipt permits an explicit same-ID retry, not automatic
submission. Current throttling/transient failure never establishes acceptance.

Conflicts withhold cached comparisons and repair access durably before returning.
Reconstruction in a fresh process remains locked. Fresh authorized synchronization
is required before comparisons or repair; missing Item authority hides the old
capture. Lost Job/Customer binding purges that Job's private pending content while
retaining safe receipt identity and unrelated authorized work. A failed authority
write leaves the durable writer marker quarantined: restart cannot expose old
ciphertext. Normal client actions never clear that marker automatically.

After a terminal rejected receipt, `repairCreation(originalID, reviewedValues)`
requires fresh inputs and creates new operation/document UUIDs, keeping the
original Job/Customer binding. Removing or replacing an unavailable Item is an
explicit new reviewed choice. Other pending work is unchanged; no descendants,
dependencies, revisions, amounts or anchors automatically follow the replacement.
A changed Customer requires separately reviewed new creation after the old
binding's private intent has been purged, never a reanchored retry.

Expiry/clock rollback locks display and staging durably. Explicit online renewal
and synchronization preserve eligible pending values without submitting them.
Ordinary session/grant denial purges private cached work; logout destroys key
and ciphertext. Restore is distinct: pending intent remains locked until fresh
authentication and synchronization establish current authority, then becomes
`restore_uncertain`. A missing restored receipt cannot prove the historical
effect never occurred. Explicit reconciliation, `acknowledgeUncertain`, and
reviewed new-ID repair are required; blind replay is forbidden.

## Executable evidence

`TestFieldDocumentProtectedReference` runs both kinds through migrated PostgreSQL,
production HTTP, actual platform-key encryption and fresh Node processes. It
covers catalog deltas, retained prices, offline UUID/reopen, title/empty Draft,
reviewed defaults/dates, all typed defaults, exact COM-03 golden totals,
negative-total rejection, lost responses (also followed by grant loss), Item
archive, binding/assignment/grant drift, required-field repair, lease renewal,
ordinary purge, restore-only review, failed persistence/quarantine and unrelated
work preservation. Requests and responses are validated against the served
OpenAPI; rejected creations are checked for partial persisted effects.

The independent `TestFieldDocumentCreation*HTTP` and commit-order families retain
the complete server validation, tax exemption, typed options/default precedence,
golden arithmetic and real archive-versus-creation race matrices. They are not
replaced by reference transport mocks.
