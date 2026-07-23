# Estimates, Invoices, and Settlement

This context describes how Estimates become Invoices, how that change may be reversed, and how customer value settles invoices while preserving the history of every document and financial event.

## Language

**Deployment Operator**:
The person responsible for the host, dependencies, secrets, upgrades, backup, restore, and recovery of a FreeFSM instance. This authority does not imply access as a Product User.
_Avoid_: Administrator, system user

**Product User**:
An authenticated person who has exactly one Role.
_Avoid_: Deployment Operator, Customer

**Role**:
A named set of Permissions assigned to a Product User. Administrator is the protected full-authority Role; Dispatcher and Technician are configurable default Roles and actor personas, not authorization conditions.
_Avoid_: Actor, job title

**Permission**:
A product-defined action and, where applicable, one of the fixed scopes All Records, Assigned Jobs, Assigned Job Context, or Own Records, granted through a Role. Authorization depends on Permissions rather than Role names.
_Avoid_: Role, arbitrary policy rule

**All Records**:
All records in the single FreeFSM instance that the permitted action can target.
_Avoid_: Cross-instance access, unrestricted action

**Assigned Jobs**:
Jobs assigned to the Product User and the Job-bound actions permitted for those Jobs.
_Avoid_: Customer assignment, general Job access

**Assigned Job Context**:
A closed, read-only first-link projection from a Job assigned to the Product User. It contains no sibling or unrelated records and does not expand relationships further, except for purpose-limited service-history summaries for linked Assets.
_Avoid_: Customer assignment, another assignment anchor, recursive relationship expansion

**Own Records**:
Records selected by type-specific stable authorization anchors: Time Entry subject, Comment original author, File original uploader, and client session authenticated Product User. Later edits, reassignment, conversion or reversion, archive or restore, and Role changes do not transfer the own-scope anchor.
_Avoid_: Current editor, current parent, Role-derived ownership

**Offline Authorization Lease**:
A finite grant allowing a conforming client to expose synchronized Assigned Job Context and stage permitted actions while disconnected. Server acceptance still depends on current authorization when the actions synchronize.
_Avoid_: Offline session, cached Permission

**Customer**:
The person or organization responsible for invoices and the owner of any credit created by overpayment.
_Avoid_: Account, client

**Invoice**:
A request for payment belonging to exactly one Customer.
_Avoid_: Bill, payment

**Estimate**:
A proposed document. Any active Estimate may be converted into an independently identified Invoice.
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
The effective state presented for an Invoice. Partially Paid and Paid reflect settlement; otherwise the Invoice's preserved Manual Invoice Status applies.
_Avoid_: Payment status, monetary status

**Status Category**:
A fixed semantic stage in an object's lifecycle. Categories provide stable meaning for behavior and reporting, while their labels and colors may be customized. Job categories are New, Travel Time, In Progress, Pending, Completed, and Canceled. Project categories are New, In Progress, Pending, Completed, and Canceled. Estimate categories are Draft, Estimate, Sent, Accepted, Rejected, and Completed.
_Avoid_: Status label, workflow column

**Status Label**:
The customizable, unique display name of a Status Category within one object type.
_Avoid_: Status Category, state

**Default Status**:
The Status selected by default for a Status Category when an object enters that category.
_Avoid_: Initial category, global default

**Manual Invoice Status**:
The preserved user-selected Invoice workflow slot: Draft, Invoiced, Sent, or Void. It remains unchanged while settlement determines the effective Invoice Status.
_Avoid_: Settlement State, effective Invoice Status

**Effective Invoice Status**:
The Invoice Status presented for behavior and reporting. Paid takes precedence over Partially Paid, which takes precedence over the Manual Invoice Status.
_Avoid_: Stored status, payment history

**Payment**:
An immutable positive receipt against one Invoice.
_Avoid_: Credit, transaction

**Settlement State**:
The monetary state of an Invoice: Unpaid, Partially Paid, or Paid. It is separate from Invoice Status.
_Avoid_: Invoice Status, payment status

**Effective Invoice Status**:
The displayed status derived with strict precedence: manual Void; manual Draft when no active settlement exists; Partially Paid for active partial settlement; Paid for paid active settlement or a manually non-Draft Invoice projected Paid; otherwise the preserved Manual Invoice Status.

**Document Delivery**:
One requested transmission of an immutable document snapshot and its PDF by email. Its lifecycle is Queued, Sending, Accepted, Delivered, Bounced, or Failed.
_Avoid_: Status email, notification

**Accepted Delivery**:
A Document Delivery accepted by the receiving mail system for onward delivery. Acceptance does not establish that the message reached the recipient.
_Avoid_: Delivered, read

**Delivered Delivery**:
A Document Delivery for which the email provider supplies evidence of delivery to the recipient's mail system.
_Avoid_: Accepted, opened

**Delivery Evidence**:
Provider-supplied information about acceptance, delivery, bounce, failure, or an enabled recipient open signal.
_Avoid_: Proof of reading, generic SMTP response

**Open Signal**:
An optional, imperfect indication that a recipient may have opened a delivered message, represented only by the first and most recent observed times and an observation count. Open tracking is disabled by default.
_Avoid_: Read receipt, proof of viewing

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
- Any active Estimate may be converted, regardless of its Estimate Status.
- Conversion transfers the Estimate's current files, tags, and comments to the resulting Invoice.
- Revert to Estimate restores the original Estimate identity as Draft, using the current Invoice document values. It combines the original estimate-only custom fields with the current values of Shared Custom Fields and transfers all current files, tags, and comments back to the Estimate.
- Reversion never permits an Invoice number to be reused. A later conversion creates a new Conversion Cycle and a newly identified Invoice.
- Conversion and reversion are atomic, idempotent, and fully audited.
- Reversion requires every active settlement effect and every overpayment credit effect of the Invoice to be fully unwound.
- An archived Invoice must be restored before it can be reverted to an Estimate.
- Product-defined Permissions authorize document creation, conversion, and reversion by scope: Assigned Jobs authority applies only to documents for assigned Jobs; All Records authority is unrestricted and is required for Customer-only documents.
