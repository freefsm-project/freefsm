# Estimates, Invoices, and Settlement

This context describes how Estimates become Invoices, how that change may be reversed, and how customer value settles invoices while preserving the history of every document and financial event.

## Language

**Customer**:
The person or organization responsible for invoices and the owner of any credit created by overpayment.
_Avoid_: Account, client

**Invoice**:
A request for payment belonging to exactly one Customer.
_Avoid_: Bill, payment

**Estimate**:
A proposed document that may become an independently identified Invoice when its status permits conversion.
_Avoid_: Quote, draft Invoice

**Conversion**:
The transformation of an Estimate into an independently identified Invoice. The Estimate leaves normal user-visible behavior but remains as an immutable hidden record.
_Avoid_: Copy, rename, status change

**Conversion Cycle**:
Immutable provenance linking the source Estimate identity and snapshot, the resulting Invoice, the responsible actor, timestamps, and the convert and revert events. An Estimate has at most one active Invoice; conversion after a revert begins a new Conversion Cycle and creates a new Invoice.
_Avoid_: Version, document link

**Revert to Estimate**:
The reversal of an active Conversion Cycle. The Invoice becomes an immutable hidden record and the original Estimate identity returns as Draft with the Invoice's current document values.
_Avoid_: Delete Invoice, undo status

**Hidden Tombstone**:
An immutable Estimate or Invoice retained for identity, provenance, and audit purposes after it leaves normal user-visible behavior.
_Avoid_: Archived document, deleted document

**Shared Custom Field**:
An Estimate custom-field definition and an Invoice custom-field definition explicitly paired for conversion by an administrator using a conversion key.
_Avoid_: Same-name field, inferred field match

**Invoice Status**:
The workflow state of an Invoice, independent of whether its value has been settled.
_Avoid_: Payment status, monetary status

**Payment**:
An immutable positive receipt against one Invoice.
_Avoid_: Credit, transaction

**Settlement State**:
The monetary state of an Invoice: Unpaid, Partially Paid, or Paid. It is separate from Invoice Status.
_Avoid_: Invoice Status, payment status

**Customer Credit**:
Unused value from an overpayment, owned by one Customer and linked to the originating Payment. Credit is unresolved while its available value is greater than zero.
_Avoid_: Balance, refund, payment

**Credit Application**:
An explicit amount taken from one selected Customer Credit source and applied to one Invoice.
_Avoid_: Payment, automatic credit

**Credit Refund**:
An outbound return of available Customer Credit to its Customer.
_Avoid_: Reversal, payment refund

**Reversal**:
An immutable compensating financial record that corrects an original record without editing or deleting it.
_Avoid_: Edit, deletion, cancellation

## Conversion Rules

- Conversion always removes the Estimate from normal user-visible behavior. There is no option to retain it alongside the resulting Invoice.
- Conversion preserves immutable provenance while giving the resulting Invoice an identity independent of the source Estimate.
- An Estimate may be converted only when its Estimate Status explicitly grants the conversion capability; status names do not imply convertibility.
- Conversion transfers the Estimate's current files, tags, and comments to the resulting Invoice.
- Revert to Estimate restores the original Estimate identity as Draft, using the current Invoice document values. It combines the original estimate-only custom fields with the current values of Shared Custom Fields and transfers all current files, tags, and comments back to the Estimate.
- Reversion never permits an Invoice number to be reused. A later conversion creates a new Conversion Cycle and a newly identified Invoice.
- Conversion and reversion are atomic, idempotent, and fully audited.
- Reversion requires every active settlement effect and every overpayment credit effect of the Invoice to be fully unwound.
- An archived Invoice must be restored before it can be reverted to an Estimate.
- Technicians may create, convert, or revert documents only for Jobs assigned to them. Dispatchers and administrators are unrestricted. Documents belonging only to a Customer are controlled by office roles.
