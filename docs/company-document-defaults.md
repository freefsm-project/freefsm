# Company and document defaults

## Authority and reviewed changes

`/settings` has two independently authorized sections:

- `configuration.company.view/manage` at U: profile, timezone, date format,
  branding color, footer and logo.
- `configuration.documents.view/manage` at U: numbering, notes, terms, due and
  validity offsets, line-description defaults and PDF description visibility.

Manage requires the corresponding View grant. A delegated Role does not need a
legacy office Role name. Read-only controls and direct submissions use the same
server authority. The view model selects only its allowlisted fields; it never
loads credentials into the settings representation. The separate existing
Administrator mail/maps/security page is `/settings/advanced`; its write model
cannot modify the migrated company/document fields. SMTP passwords are not
rendered and a blank password submission retains the saved credential.

Every migrated management command checks the submitted configuration revision,
rechecks identity/session/grants under the shared authorization transaction lock,
and commits the effect and protected audit together. Ordinary number allocation
advances a cursor, not the management revision: it cannot invalidate an unrelated
company-profile review. Sequence management still locks and checks the current
high-water mark. Concurrent management changes reject stale reviews.

## Initialization, dates and existing records

New record initialization resolves explicit input, then Customer defaults, then
company defaults. Explicit blank content is distinct from omission or inheritance.
Customer defaults are managed at `/customers/{id}/document-defaults` with Customer
View/Edit and the independent financial View facet; the form offers a separate
inheritance checkbox for each value. Document creation uses purpose-limited
default inputs without granting access to the Customer defaults editor.

New Invoice and Estimate forms include the resolved notes, terms, dates and line
descriptions. Customer selection updates only fields the user has not entered.
The form holds a fixed default snapshot; changing configuration in another tab
does not replace entered content. Validation errors retain submitted values.
Direct creation submissions also resolve omitted fields. Job-derived creation
retains explicit source content and resolves the remaining defaults. Conversion
copies recorded Estimate terms and uses current due defaults for the new Invoice.

A blank company due offset means **No Due Date**, distinct from zero days.
Pristine first-user initialization uses No Due Date; upgrades retain recorded
offsets. The company offset is nullable. The existing Invoice model continues to
represent a missing due date with its zero-date value; no historical invoice date
is rewritten by these migrations. Estimate Valid Through is an optional SQL date.

Changing timezone updates company-date calculations and display of stored
instants. SQL calendar dates are formatted without timezone conversion. There is
no special timezone confirmation or separate timezone-history mechanism.
Finalization changes workflow state without reinitializing a saved Draft's dates.
Receivable overdue calculations compare calendar dates with the company day;
No Due Date stays unpaid without being classified as overdue.

## Numbering and pre-v1 review

Invoice and Estimate numbers are allocated under the company row lock. Their
recorded labels and issued-number ledger preserve identities across prefix
changes, archive and conversion. Sequence management cannot move backwards;
gaps remain valid. The ledger rejects both ordinal and full-label reuse, and
issued entries cannot be updated, deleted or truncated. Lease prefix/sequence
settings are prospective inputs for the Lease implementation.

Authenticated Invoice/Estimate creation, including Job-based creation, uses the
shared authority transaction. Current session/grants, Customer/Job lifecycle,
same-Customer links, assignments and selected catalog inputs are checked under
locks. Document, number reservation and immutable creation audit commit together.
Conversion retains its serializable cycle/idempotency transaction and joins the
authority/audit boundary for the newly created Invoice. Internal fixture or
bootstrap calls without an authorization context are not production HTTP entry
points.

Forward migrations retain existing profile/default/branding values and records.
They do not guess historical labels or convert old PDF files into fabricated
version history. Settings displays unresolved legacy identities and invalid
stored configuration values for review. An unknown color remains a text value,
rather than being silently normalized by a browser color input.

Before using legacy numbering, a manager explicitly supplies the verified label,
number and next sequence high-water mark, including consideration of gaps. The
review is revision-guarded and audited. Existing invoice ordinals remain fixed;
Estimate identifiers are not replaced by their reviewed display numbers.
Unreviewed records expose an explicit internal-record locator and review state,
never a label fabricated from today's prefix.

Legacy `terms IS NULL` means terms were not recorded. It is not an instruction to
apply a new company default to an existing document. The document editor explains
the required review; an explicit blank records no terms. New PDF generation waits
for recorded identity and terms. Existing files remain intact and accessible
through their existing file authority.

## PDF history

The browser PDF page lists persisted versions. Generation and download use their
independent document grants and current All/Assigned scope. Generation reads
current presentation, verifies source state again at commit (including a
configuration change-and-revert), and atomically stores immutable bytes, source,
generation time and protected audit. A missing logo or rendering/audit failure
publishes no version. Logo uploads use new durable files, so failed uploads or
stale reviews cannot overwrite the currently referenced logo.

Downloads never generate PDFs. Without an explicit version, only the newest
nonstale version is selected. A selected stale version requires explicit consent,
which is audited, and returns those exact bytes. Presentation/content/settlement
changes participate in staleness; future initialization defaults and collaboration
do not change existing PDF bytes. Previously saved PDF file attachments retain
their original history rather than acquiring invented source snapshots.

Email composition selects a persisted PDF and carries that version ID through
submission. It does not render an attachment. Delivery rechecks authority,
eligibility and staleness inside its own transaction, loads the selected bytes,
and commits its outbox effect plus immutable audit together. Stale sending needs
explicit confirmation; replay preserves the original selection. The shared
renderer also rejects unknown identities and terms for other rendering callers.
Versioned rendering receives explicit presentation/settlement inputs rather than
replacing request context. Default resolution and calendar-offset projection are
shared by the creation forms and Customer-choice snapshots.

## Integration boundaries

The migration range introduced here is 058–067. No new grants are assigned to
nonprotected Roles. Existing mail/security administration and other commercial
workflow cutovers remain in their owning slices; this is not a wholesale migration
of every commercial action. Creation, PDF commands and selected-PDF queueing have
commit-time boundaries. Delivery retains ownership of its immutable message
snapshots, worker and retry lifecycle.

Future producers should resolve initialization explicitly, use the production
Invoice service with its PostgreSQL pool, and allocate identities while holding
the company row lock. A Lease producer must consume the configured Lease cursor
and reserve the identity in the issued-number ledger in its creation transaction.

## Acceptance tests

`internal/handlers/company_configuration_integration_test.go` exercises production
routes over migrated PostgreSQL: independent grants, API exclusion, reviewed
saves, invalid input, actual filesystem and audit failure, concurrent saves and
number allocation, commit-time revocation, pre-057 upgrade preservation and
explicit review, initialization/retention, date preservation, conversion,
independent PDF scopes, immutable downloads and generation ABA rejection.

`TestCompanyConfigurationPersistedBrowser` runs
`internal/handlers/testdata/browser/company-configuration.cjs` with real HTTP,
CSRF, PostgreSQL and Chromium. It persists company/Customer defaults and an
Invoice, changes defaults while a form is open, verifies explicit values, rejects
a stale settings tab, and checks exact selected PDF bytes after presentation drift.
The delivery regression drives the production HTTP composer and outbox worker
through a real local SMTP receiver, verifying exact attachment bytes, stale
confirmation, replay and audit-failure rollback.
