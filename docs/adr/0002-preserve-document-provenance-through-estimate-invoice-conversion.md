---
status: accepted
---

# Preserve document provenance through estimate and invoice conversion

Estimate conversion will create an independently identified Invoice and remove the source Estimate from normal user-visible behavior. The Estimate will not be hard-deleted: it will remain as a hidden immutable tombstone so its identity and historical state remain available for provenance and audit. There will be no setting to retain the Estimate in normal behavior after conversion.

Each conversion will create an immutable Conversion Cycle linking the source Estimate identity and snapshot, the resulting Invoice, the responsible actor, timestamps, and the convert and revert events. An Estimate may have only one active Invoice. Reverting and later converting again will create a new Conversion Cycle and a new Invoice; Invoice identities and numbers will never be reused.

Conversion will transfer the Estimate's current files, tags, and comments to the Invoice. Custom fields will cross the document boundary only when an administrator has explicitly paired the Estimate and Invoice definitions with a shared conversion key. Field names will not establish equivalence.

Revert to Estimate will hide the Invoice as an immutable tombstone and restore the original Estimate identity as Draft. The restored Estimate will use the Invoice's current document values, combine the original estimate-only custom fields with the current shared custom-field values, and receive all current files, tags, and comments from the Invoice. Reversion therefore preserves the latest working document while retaining both sides of the historical transition.

Convert and revert operations will be atomic, idempotent, and fully audited. Reversion is allowed only after all active settlement and overpayment credit effects have been fully unwound, and an archived Invoice must first be restored. Convertibility is an explicit capability of an Estimate status rather than an inference from its name. Technicians may create, convert, and revert only for assigned Jobs; dispatchers and administrators are unrestricted, while customer-only documents remain office controlled.

## Considered Options

- Hard-delete the source document after conversion or reversion. Rejected because deletion would destroy identity continuity, weaken auditability, and make reliable reversibility impossible.
- Reuse one document identity and change its type or status. Rejected because Estimates and Invoices have distinct lifecycles and references, and an Invoice must retain an independent, never-reused identity and number.
- Copy the Estimate and leave both documents active. Rejected because two normal documents would create ambiguous ownership of relations and permit the Estimate and Invoice to diverge after conversion.
- Keep only a direct Estimate-to-Invoice reference. Rejected because a direct link cannot faithfully represent repeated convert/revert cycles, source snapshots, event actors, or event timestamps.
- Infer shared custom fields from names or reuse one definition across document types. Rejected because renaming and type-specific administration would make conversion behavior implicit and fragile.
- Copy files, tags, and comments while retaining them on both documents. Rejected because duplicated relations would diverge and obscure which active document owns the current collaboration history.
- Reconstruct the original Estimate exactly as it was at conversion time. Rejected because users expect reversion to continue from the Invoice's current document values while recovering Estimate-specific information.
- Permit reversion while settlement effects remain active. Rejected because hiding an Invoice with unresolved financial effects would break financial traceability and Customer Credit integrity.

## Consequences

- Hidden tombstones remain addressable for provenance and audit but must be excluded from normal document behavior and mutation.
- Conversion history is append-only: reconversion adds a new Conversion Cycle and Invoice rather than reopening or replacing an earlier cycle.
- Relations have one active document owner at a time and move with the active document during conversion and reversion.
- Reversion requires a deterministic merge: current Invoice document values and shared custom fields take effect, while original estimate-only custom fields are restored.
- Explicit custom-field pairing adds administrator configuration but prevents accidental or ambiguous field mapping.
- Financial unwind and archive restoration are prerequisites rather than side effects of reversion.
- Authorization depends on Job assignment and document ownership context in addition to role.
- Atomicity and idempotency prevent partial relation transfer and duplicate results, while the audit history records both successful conversion directions.
