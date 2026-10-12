# Permission-aware installed navigation

Navigation is a presentation of current effective grants, not a source of
authority. `UserInfo.NavigationGrants` is loaded on every authenticated request.
Main sections, Create actions and independently delegated configuration pages use
their applicable scopes. Closed master Context does not imply collection access.
Workflow-only users can reach all four workflow configuration pages and return to
Dashboard without requiring company/document configuration authority.

## Read destinations

The explicitly registered `collectionRead` GET adapter activates
`authorization.InstallNavigationReadBoundary`. Its SQL predicates run before
counts, pagination, search and projections, intersecting company, A/J/O grants and
active Job assignment. Existing Customer and Item boundaries retain independent
facet protection. All Records document access remains independent of the parent
Job's lifecycle; Job-bound grants require a current, active assignment.

The adapter is not installed on command requests. Existing command-domain
authorization, transactions and audit remain responsible for submitted effects.

## Detail and edit follow-through

Detail `PolicyRead` continues to use `ParentAccess`. The separate `DetailAccess`
presentation snapshot checks `DetailsAccess` for Edit and each independent action
for other controls. A legacy command's existing policy is an additional
installed-route capability check, never a replacement for current grants. Broad
legacy `CanManage` flags no longer disclose unrelated actions on detail pages.

`ParentFacet` intersects read-only pricing and settlement facets with the actual
visible parent. It preserves expressly authorized archived reads and checks the
actual Job for a narrower Assigned pricing grant even when parent View is All
Records. Inaccessible pricing is omitted from both details and existing editors;
it is not rendered as a zero value. Ordinary document edits without pricing
inputs retain captured lines/tax and unchanged context. Changed context or
submitted pricing still follows the existing selected-input checks.

Invoice settlement history has a bounded current-grant read projection. Payment,
Credit, reversal, finalization, PDF and Delivery retry controls remain separately
granted and intersect installed route support. Existing lifecycle eligibility and
blocker messages remain authoritative.

Job-bound document creation uses a typed current-authority precheck for the
specific Invoice or Estimate Create permission and its visible Job parent. All
scope does not depend on the account's legacy discriminator or assignment;
Assigned scope still requires a current active assignment. The two Create grants
remain independent. Invoice preparation is a confirmed non-writing `completed`
action; saving the prepared Invoice and directly creating an Estimate are actual
domain writes with `saved` receipts. Existing `CreateDocument`/`DetailsTransaction`
scope, selected-Customer, numbering and atomic-audit checks remain authoritative
at commit, including grant loss after preparation.

Exact-parent activity works from current parent View. Cross-document conversion
history additionally checks the independent provenance View and bounds events to
that conversion lineage. Untyped historical metadata is withheld from
nonprotected readers rather than treating parent View as authority over every
historical field. Conversion command policy is unchanged.

## Timesheet follow-through

Time Entry detail, correction and GPS decisions use the recorded subject and
current A/J/O scopes through `TimeEntryAccess`. Assigned access requires an active
assigned Job; Own access never expands to another subject, including for an
account with the legacy Dispatcher discriminator. The subject's name is a
purpose-limited projection of that visible Time Entry, not directory access.

Timesheet dates link to their detail pages. Correction is independently granted
and usable for an authorized other subject. Its bounded adapter retains the
existing correction fields, locks current authority and the entry, rechecks scope
after association changes, and commits the effect with protected audit. It does
not introduce a new time lifecycle. Existing deletion behavior remains separate
from correction; its UI also requires the independent grant and installed route
capability.

Job links and GPS coordinates are independently checked. If an editor cannot
read the existing Job, the form omits that association input and a normal save
preserves it. Neither subject identity nor GPS is changed by correction.

## Verification

`internal/handlers/navigation_integration_test.go` covers configuration-only and
workflow-only access, equal grants/renames, collection scope predicates, counts,
company isolation, empty grants, assignment loss, dashboard visibility, and
persisted-browser history refresh/permission loss.

`internal/handlers/navigation_details_integration_test.go` exercises production
collection/detail/edit destinations for nonprotected users, View-only Dispatcher
denials, independent pricing/settlement disclosure, narrower Assigned pricing,
and authorized archived details. Its persisted browser follows all five parent
record types into granted edit forms, reloads and returns, and saves an Estimate
title without pricing access; later authorized reads verify the captured price
was preserved.

`navigation_time_integration_test.go` extends this to another subject's Time
Entry: A/J/O scope and company denials, independent GPS, read-only action denial,
actual correction, assignment escape rejection, audit rollback and a persisted
row → detail → edit → save → return browser journey. The two older HTTP boundary
regressions now assert current granted discovery and actual nondisclosure;
`TestHTTPAuthorizationBoundaries` uses the fully migrated production fixture.

Run these with a migrated disposable `FREEFSM_TEST_DATABASE_URL`. Enable
`BROWSER_TESTS=1`, configure `NODE_PATH` for the project's Playwright dependency,
and set `BROWSER_EXECUTABLE` for the persisted browser journeys.
