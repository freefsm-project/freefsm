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

**Time Entry**:
A time interval recorded for exactly one Product User, optionally linked to one Job. An active Time Entry has no end time; a Product User may have at most one active Time Entry across the instance, and accepted non-void entries for the same subject may not overlap. Assignment loss or Job archive blocks new Job clock-ins but does not prevent the subject from clocking out an existing active entry, which retains its Job link. Responsive web supports live clocking and manual creation, correction, and void; the public field API and offline synchronization support the acting user's standalone and assigned-Job clock-in/out. Duration is exact elapsed time derived from its instants; display, billing, or payroll rounding never changes the entry.
_Avoid_: Timesheet, Job duration, schedule

**Manual Time Entry**:
A closed historical Time Entry created with explicit start and end times, both no later than the time of creation and with the end after the start. Manual creation cannot create an active Time Entry.
_Avoid_: Clock adjustment, scheduled time, open manual entry

**Time Entry Correction**:
An immutable before-and-after revision that preserves the Time Entry identity and subject while changing its start, end, Job link, or notes. It records the actor, acceptance time, and required reason and updates the current projection. Corrected times cannot be future times; closing requires a valid end, while reopening removes the end and must preserve sole-active-entry and non-overlap invariants. Own Records retains correction authority after Job access is lost; only a retained Job identity and title snapshot remain visible, and the link may be kept or cleared but may be changed only to a currently authorized, non-archived Job.
_Avoid_: Overwrite, replacement Time Entry, subject transfer

**Time Entry Void**:
A terminal immutable event that records actor, time, and required reason, marks a Time Entry void without deleting it, and ends any active effect without supplying a missing end time. Voided entries are excluded from ordinary totals and exports unless explicitly requested; correction uses a replacement Manual Time Entry rather than unvoiding.
_Avoid_: Delete, correction, reversible status

**Timesheets**:
The authorized reporting projection of current Time Entry states, not a separate record type. It supports date-range, subject, Job, lifecycle, and manual-or-clocked filters; active durations are provisional as of query time, voids are opt-in and excluded from totals, and exact cross-midnight duration is split at company-timezone midnight for grouping by Product User or Job.
_Avoid_: Time Entry, payroll ledger, export permission

**Clock Event**:
An idempotent clock-in or clock-out fact with separate occurrence and server-acceptance times. Online web events use server time for both; offline events use the client-captured occurrence time, subject to lease, ordering, and Time Entry invariants when accepted. The Clock Out action automatically targets the subject's sole active Time Entry while carrying its stable identity for race-safe acceptance.
_Avoid_: Time Entry correction, schedule event, receipt time

**GPS Observation**:
An optional immutable, client-reported location observation attached to a clock-in or clock-out event. It contains latitude and longitude as a complete pair within valid geographic ranges, capture time no later than acceptance, and nonnegative accuracy in meters when supplied by the client. Offline capture must fall within its authorization lease. Invalid or unavailable evidence may be explicitly omitted without blocking clocking; correction or void retains accepted observations, and v1 performs no continuous or background tracking.
_Avoid_: Verified presence, route history, required clock location

**Offline Authorization Lease**:
A finite grant allowing a conforming client to expose synchronized Assigned Job Context and stage permitted actions while disconnected. Server acceptance still depends on current authorization when the actions synchronize.
_Avoid_: Offline session, cached Permission

**Customer**:
An Individual or Organization represented by a service-and-billing master record, responsible for invoices and the owner of any credit created by overpayment. Customer Kind is descriptive and editable; v1 has no lead, opportunity, pipeline, or other CRM lifecycle.
_Avoid_: Account, client, sales lead

**Contact**:
An independently identified, reusable person belonging immutably to exactly one Customer and available to that Customer's work records.
_Avoid_: Product User, Job-only contact

**Location**:
An independently identified, reusable service place belonging immutably to exactly one Customer and available to that Customer's work records.
_Avoid_: Job-only address, shared Customer location

**Primary Customer Record**:
The optional single Primary Contact or Primary Location used as an office form default for one Customer. Primary designation never creates a Job link or grants Assigned Job Context.
_Avoid_: Automatic Job relationship, authorization anchor

**Asset**:
An independently identified, reusable piece of equipment with a required immutable tenant-unique Asset Number and one immutable Asset Ownership Class. Its identity persists across Customer ownership transfers, placements, returns, and later placements.
_Avoid_: Item, Job-only equipment, separate leased-asset type

**Asset Ownership Class**:
The immutable classification of an Asset as Customer-owned or Company-owned. A Customer-owned Asset has exactly one current Customer owner; a Company-owned Asset belongs to the operating organization and has zero or one current Customer placement.
_Avoid_: Asset status, Customer placement

**Asset Placement**:
The current assignment of a Company-owned Asset to a Customer. It begins through explicit placement and ends only through explicit return or transfer; Lease expiration does not move the Asset or end its placement.
_Avoid_: Customer ownership, Lease term, Job link

**Asset Association Event**:
An audited, reasoned, effective-dated transfer, placement, return, or correction that preserves an Asset's complete Customer association timeline. Events cannot take effect in the future; audited backdating is allowed only when derived association periods remain non-overlapping and affected relationships remain valid.
_Avoid_: Current Customer overwrite, Job reassignment, Lease expiration

**Historical Job-Asset Link**:
A retained provenance relationship after an Asset leaves the Customer associated with a Job. It preserves prior service history but is no longer an operational Job link and grants no current Asset or new-Customer context.
_Avoid_: Operational Job relationship, current Asset authorization, deleted link

**Asset Service Entry**:
A revisioned service record for exactly one Asset serviced through one Job, containing its service date and summary and one or more responsible Product Users. A Job-Asset relationship has zero or more entries; they never gate Job completion or turn shared Job notes into Asset history.
_Avoid_: Field Notes, linked Job summary, Asset internal notes

**Asset Service History**:
The history of Asset Service Entries. All Records Asset view may show the complete lifetime history, while Customer and Assigned Job Context projections show only entries from association periods involving that Customer.
_Avoid_: Full linked Job history, cross-Customer context, activity feed

**Lease**:
An independently identified agreement with one Customer covering one or more Company-owned Assets. Its term and lifecycle are distinct from Asset Placement, so it may expire while its Assets remain with the Customer.
_Avoid_: Asset Placement, Customer-owned Asset, automatic return

**Project**:
An independently identified, reusable grouping of zero or more Jobs belonging immutably to exactly one Customer. It may link one Location belonging to that Customer; its Jobs retain their own independent Location links.
_Avoid_: Job, shared Customer project

**Project Progress**:
The manually maintained percentage from 0 through 100 describing a Project's progress. Entering a Completed Project Status presents 100 while retaining the last non-completed value, which is restored when the Project leaves Completed.
_Avoid_: Completed Job ratio, Project Status, weighted milestone progress

**Service Notes**:
Office-maintained operational instructions on a Customer, Contact, Location, Asset, or Project that are deliberately included in Assigned Job Context when that record is linked. They are distinct from internal notes, custom fields, Comments, and activity feeds.
_Avoid_: Internal notes, Field Notes, unrestricted custom field

**Item**:
An independently identified, tenant-wide Service or Product catalog record with separately authorized base, Pricebook, and Item Cost facets. Names may repeat; a nonblank SKU is unique across active and archived Items and is never silently reused.
_Avoid_: Document line, Customer-owned item, Pricebook entry

**Pricebook Facet**:
The commercial fields of an Item used during work or document preparation, including SKU, nonnegative sale price, and tax treatment. A Job, Estimate, or Invoice line receives an editable snapshot with Item provenance, so later Item edits or archive never rewrite the line.
_Avoid_: Separate price record, live document pricing, cost ledger

**Item Cost Facet**:
The sensitive internal unit-cost fields of an Item, protected by a Permission separate from Item base and Pricebook commercial view. It is not included in the Technician default or copied into customer-facing document lines.
_Avoid_: Sale price, Pricebook Facet, document cost

**Customer Related Work**:
The permission-filtered, non-recursive union of Projects, Jobs, Estimates, and Invoices belonging directly to one Customer. Each record appears once; Contacts, Locations, Assets, and Leases use separate Customer sections.
_Avoid_: Recursive relationship graph, Customer master records, unfiltered totals

**Customer Receivables**:
The positive outstanding value of visible, active, non-Draft, non-Void Invoices for one Customer. Unapplied Customer Credit remains separate; aging uses Current, 1-30, 31-60, 61-90, 91+, and No Due Date buckets.
_Avoid_: Draft forecast, automatically netted credit, hidden Invoice total

**Job**:
A work order belonging to exactly one Customer from creation onward. A new Job requires only its Customer and Job Title, enters the configured default New Status, and may otherwise remain unplanned. Customer ownership anchors relationship consistency but does not grant authorization.
_Avoid_: Visit, Project, Customer assignment

**Job Title**:
The required free-text summary identifying the work requested. It is not a configured classification or type taxonomy.
_Avoid_: Job Type, subtitle

**Job Relationships**:
A Job may operationally link one service Location, one Project, any number of Contacts and Assets, and any number of independently identified Estimates and Invoices. Operational links must currently belong to or be placed with the Job's Customer; an Asset link becomes historical when that association ends, and each Estimate or Invoice belongs to at most one Job.
_Avoid_: Authorization scope, recursive context, document subordination

**Field Notes**:
A shared Job facet for observations recorded during field execution, distinct from planning details and Comments. Its explicit Edit Permission supports Assigned Jobs and All Records scopes and is included in the Technician default; current notes follow Job View, and revisions follow authorized Job activity. Revision-guarded editing is supported through responsive web, the public field API, and offline synchronization.
_Avoid_: Technician Notes, service instructions, unaudited shared text

**Field Execution**:
The explicitly permitted actions a Product User may perform on an assigned Job without general Job-edit authority: Job Status change, Subtask completion or reopening, and Field Notes editing. Comments, Files, Time Entries, GPS Observations, documents, and Payments remain separately authorized resources and actions; every other mutable Job field remains an office planning or service detail.
_Avoid_: Technician Job editing, UI-based authority, implicit planning access

**Job Customer Reassignment**:
The atomic replacement of a Job's Customer. It clears the Job's operational Location, Project, Contact, and Asset links and reassigns its linked Estimates and editable draft Invoices to the new Customer. A linked finalized or settled Invoice blocks reassignment until unlinked; any Asset Service Entry blocks reassignment permanently to preserve service provenance.
_Avoid_: Customer inheritance, implicit document context

**Job Schedule**:
The single optional schedule belonging to a Job, comprising a planned start and end and an optional customer-facing arrival window that contains the planned start. A Job may be unscheduled; an arrival window cannot exist without a planned interval, and v1 has no independently scheduled visits within a Job.
_Avoid_: Visit schedule, recurring occurrence

**Job Due Date**:
The optional local calendar date by which a Job is expected to reach a Completed Status. It is independent of the Job Schedule. After that date, a non-archived Job is overdue unless its Status Category is Completed or Canceled; overdue and scheduling beyond the date warn without changing Status or blocking work.
_Avoid_: Scheduled end, automatic completion, hard deadline

**Operational Time**:
An exact instant displayed and edited in the company timezone by default. Intervals may cross midnight and require the end after the start; ambiguous daylight-saving times require an explicit offset choice, and nonexistent local times are invalid.
_Avoid_: Naive local timestamp, silently shifted time

**Job Assignment**:
The set of zero or more enabled Product Users assigned to perform a Job. Assignment does not grant authority; incompatible current Permissions warn without blocking assignment. Disabled users cannot receive new assignments, while existing assignments remain visible as unavailable. All assignees share the Job Schedule and have equal Assigned Jobs authority when granted; v1 has no primary assignee, per-assignee schedule, or assignment-role label.
_Avoid_: Customer assignment, primary technician, Visit assignment, lead/helper label

**Dispatch**:
An atomic office planning action that changes a Job Schedule, a Job Assignment, or both. Scheduling and assignment remain independent. Every drag-and-drop move in calendar or dispatch views only proposes a date, time, and target assignee; the same modal presents the proposal, arrival window, and complete assignee set for editing, and no change occurs until confirmation succeeds.
_Avoid_: Status transition, automatic assignment

**Schedule Conflict**:
An overlap between the Job Schedules of Jobs assigned to the same Product User. It is a visible warning rather than a prohibition; an authorized user may explicitly acknowledge and accept the conflict, and that acknowledgment is audited.
_Avoid_: Validation failure, unassigned schedule overlap

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

**Job Status Transition**:
An explicit move of an active Job to any configured Job Status. V1 has no transition graph; creation alone selects a Status implicitly, and scheduling, assignment, clocking, and Subtask actions never change Status. Completed and Canceled categories do not close the Job, and archive remains the separate read-only lifecycle boundary.
_Avoid_: Job closure, archive, implicit workflow gate

**Subtask**:
A stably identified, ordered unit of work within a Job, with a required title and a completion projection retaining the responsible actor and time. Completion, reopening, reordering, and removal do not change its identity or the Job Status; removal hides it from the active list while retaining audit and synchronization history.
_Avoid_: Array index, Job Status, disposable checklist row

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
