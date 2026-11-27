# #101 parallel implementation interface contract

Planning handoff only; no Go/schema/template changes in this document. Coordinator accepted two versioned operational/Billing row-type CSVs and GET/POST `/leases/report/export`. This document fixes the cross-owner seams; implementation-private helpers remain with their owner. It narrows the earlier plan where existing date fields satisfy the AC without new workspace features.

## Authoritative exclusive ownership

| Owner/session | Exclusive implementation files |
| --- | --- |
| A — `ses_decb0c5bfffe7W5QOF98X16H3c` | Authorization report projection/reference files, new `internal/authorization/lease_exports.go`, supporting authorization tests; narrow owning billing/coverage helper extraction only where needed. |
| B — `ses_decb0016affeWFnSIOFH1ZPMk3` | `internal/exports/leases.go`, `internal/exports/lease_references.go`, adapter/contract tests under `internal/exports`. |
| C — `ses_decb02b61ffe1vTq5TK5ktQYXX` | Handler/UI implementation, templ and generated templates, `internal/exports/catalog.go`, `internal/exports/recovery.go`, handler registration/recovery wiring, and public export/report docs. No handler test files or browser script. |
| D — `ses_decb306caffet4tBvZs9jPNCPm` | All `internal/handlers/lease_exports_*test.go` and `internal/handlers/testdata/browser/lease-exports.cjs`. |

No confirmation remains outstanding. Consumers use the exact normalization, typed reference and snapshot signatures below. Coordinator distributes this handoff; planning does not contact or spawn agents.

## Scope decisions

- Keep existing `Service.LeaseReport(ctx, Actor, LeaseReportQuery)` behavior and public report types compatible. Extract its transaction-bound selection internally. Export must not call the public method or loop HTTP pages.
- No new general queue API, dashboard, export framework, permissions, migrations, File projection, Invoice mutation or master export.
- Operational starts/monthly transitions/endings are date columns with existing lifecycle/placement filters plus the required ending range. No additional operational queue selector is necessary for this ticket.
- Billing predicates extend existing `review`, `void`, `ungenerated` with `pending`, `failed`. No separate overlapping `queue` option. Existing `ungenerated` remains due-by-as-of; `pending` covers owed ungenerated periods through as-of +30 civil days. Neither implies an Invoice balance or a guarantee that generation will succeed.
- All interfaces below use existing `authorization.Principal`, `Scope`, `Grant`, `pgx.Tx`, `exports.Definition`, `Record`, `RecoverySelection` and existing error conventions. No new domain-to-exports dependency.

## A → B/C: filter and authority API

Add in `internal/authorization/lease_exports.go` (A owns):

```go
type LeaseExportMode string
const (
    LeaseExportOperational LeaseExportMode = "operational"
    LeaseExportBilling     LeaseExportMode = "billing"
)

// No Page/PerPage and no mode field; kind fixes mode permanently.
type LeaseExportFilter struct {
    AsOf          string `json:"as_of"`
    Search        string `json:"search"`
    Lifecycle     string `json:"lifecycle"`
    Customer      int64  `json:"customer"`
    Asset         int64  `json:"asset"`
    Premises      int64  `json:"premises"`
    EndingFrom    string `json:"ending_from"`
    EndingThrough string `json:"ending_through"`
    Archive       string `json:"archive"`
    Placement     string `json:"placement"`
    Billing       string `json:"billing"`
    Sort          string `json:"sort"`
}

func NormalizeLeaseExportFilter(LeaseExportFilter) (LeaseExportFilter, error)
func AuthorizeLeaseExport(Principal, LeaseExportMode, LeaseExportFilter) error
func AuthorizeLeaseExportSelection(context.Context, pgx.Tx, Principal,
    LeaseExportMode, LeaseExportFilter) error
// Fresh request only; never called for Ready status/download/references.
func ValidateLeaseExportFreshSelection(context.Context, pgx.Tx, Principal,
    LeaseExportMode, LeaseExportFilter) error
```

Normalization is pure: valid dates/order, nonnegative IDs, existing lifecycle/archive/placement enums, Billing enum above; default sort `number`, alternate `number_desc`. Empty as-of stays empty until snapshot. Preserve search text (case-insensitive matching as today); no automatic sensitive-filter clearing. `AuthorizeLeaseExport` requires `lease.export` + `lease.view` All Records, Billing View All Records for Billing mode, and rejects Billing predicates in operational mode even for Administrator. Mirror existing report prerequisite rules for filters. Unknown mode is forbidden. Validation errors use `ErrConflict`; denied permissions use `ErrForbidden`; invisible selected resources use `ErrNotFound`.

`AuthorizeLeaseExportSelection` is time-independent authority validation: re-run grants and validate selected Customer/Asset/premises including archived access even for empty results. It MUST NOT compare as-of to today or reclassify queues. `pending` and `ungenerated` require complete Invoice/Pricing/archived-Invoice authority (`leaseReportGenerationComplete`); `failed` uses the same completeness guard because unresolved failure classification depends on absence of a generation. Existing positive review/void matching keeps independently authorized Invoice/Pricing scope, including compatible Assigned access. Historical due-by-date `ungenerated` remains a retained-generation/entitlement predicate, not a worker-status assertion. No silent downgrade of explicit forbidden filters.

`ValidateLeaseExportFreshSelection` additionally validates temporal eligibility for a NEW review/acceptance: resolve company today, reject future as-of, and reject `failed`/`pending` when explicit as-of is earlier than today. Generation performs the same temporal validation using its single captured date/timezone before selecting rows. Empty as-of resolves at generation; explicit current date queued across midnight may fail generation as now-historical queue intent, requiring new review. In contrast, once Ready, either explicit or empty-as-of artifact retains its captured meaning for the full Ready+24h window. Midnight alone cannot block it. Status/download/reference checks only verify current grants, selected-resource authority and captured original/current reference anchors; never today's filter membership, date eligibility, lifecycle or queue classification.

The existing engine invokes optional `AuthorizeSelection` at acceptance, generation, status and download, so temporal validation MUST NOT live in that hook. Add one narrow optional acceptance hook (specified below) invoked only in `Service.Accept` inside its existing fenced transaction, after authority checks and before accepting a NEW job. Same-intent existing job replay returns its existing receipt without fresh temporal reclassification. Generation calls fresh validation inside A's snapshot path, reusing the captured metadata. No new generic lifecycle or policy framework is needed.

Add only `EndingFrom, EndingThrough string` to existing `LeaseReportQuery`; use the same normalized predicates for browser and exports. Ending comparison uses notice termination date if present, else fixed-term end. Draft without an end does not match a nonempty ending bound. C adds parsing/validation/retention for these inputs and new Billing choices. Historical `failed` selection gets an actionable validation message, not an empty report.

## A → B: streaming snapshot and typed data

```go
type LeaseExportMeta struct {
    AsOf      string    // resolved company date, YYYY-MM-DD
    Timezone  string    // company IANA timezone from same snapshot
    SnapshotAt time.Time // transaction_timestamp(), not per-row clock_timestamp()
}
type LeaseExportSummary struct {
    Meta          LeaseExportMeta
    LeaseCount    int64
    RentCents     int64 // Billing in-force selected-date monthly rent only
    RentAvailable bool  // false => unavailable total, not default numeric zero
    Reference     LeaseExportReference // all contributors + selection prerequisites
}
type LeaseExportEntry struct {
    Lease                LeaseReportRow // already authorized existing projection
    FirstMonthlyOn       string         // fixed end + 1, not monthly rent due date
    RentTerms            []LeaseExportRentTerm
    BillingRevisions     []LeaseExportBillingRevision
    Periods              []LeaseExportPeriod
    GeneratedInvoices    []LeaseExportGeneratedInvoice
    FailureHistory       string // "current" or "unavailable"; Billing only
    PositionAvailability  string // "available" or "unavailable"
    Reference            LeaseExportReference
}
type LeaseExportRentTerm struct {
    AssetID     int64 // 0 when identity not independently visible
    RentCents   int64
    TaxRate     string // configured decimal rate, not computed tax amount
    EffectiveOn string // selected as-of date: position, not invented revision date
}
type LeaseExportBillingRevision struct {
    ID, ActorID int64
    EffectiveOn, RecordedAt, Reason string
    Changes []LeaseExportBillingChange
}
type LeaseExportBillingChange struct {
    Ordinal int // 1-based stable position in retained revision After array
    AssetID int64 // independently redacted
    BeforeRentCents, AfterRentCents int64
    BeforeTaxRate, AfterTaxRate string
}
type LeaseExportPeriod struct {
    Kind, DueOn, State, Reason string
    ObligationID int64 // 0 for derived, not yet materialized period
}
type LeaseExportGeneratedInvoice struct {
    Invoice LeaseReportInvoice // current independently authorized identity;
                               // retained generation amount/status/provenance
    ObligationID, ObligationRevisionID int64 // revision 0 = original obligation
    TaxCents int64
    TaxRate string
    TaxExempt bool
    Lines []LeaseExportInvoiceLine
}
type LeaseExportInvoiceLine struct {
    Ordinal int // 1-based captured line position, identity independent of Asset view
    AssetID, BillingRevisionID int64
    Amount string // captured unit_price_decimal, not recalculated current rent
    Taxable bool
}

func LeaseExportSnapshot(ctx context.Context, tx pgx.Tx, p Principal,
    mode LeaseExportMode, f LeaseExportFilter,
    emit func(LeaseExportMeta, LeaseExportEntry) error,
) (LeaseExportSummary, error)

func CheckLeaseExportReference(context.Context, pgx.Tx, Principal,
    LeaseExportMode, LeaseExportReference) error
```

A emits one entry per matching Lease using existing SQL `ORDER BY document_number,id` (reverse both for descending), builds summary after all matching rows, and propagates any query/scan/emit error. This preserves the existing domain order: migration 105 stores document_number as bigint; number_label is display text. Never parse/re-sort display labels or add natural-number parsing. B emits CSV children immediately and the single summary last. Summary metadata exists even for zero matches. A owns contributor accumulation so B cannot accidentally omit summary authorization. Summary is never emitted on projection failure. `LeaseCount` counts distinct Leases, not CSV child rows; engine `row_count` remains CSV record count including summary.

### Mandatory single selected-date agreement position

A must derive one transaction-local agreement position before projecting lifecycle, coverage, rent, periods/queues or ending filters. All consumers use its corrected-history commencement, fixed end, notice/cancellation and inclusive termination boundaries. A may extract a narrow private helper in `lease_coverage.go` and `lease_billing_worker.go` to share owning entitlement; no second independent calendar/termination interpretation is permitted. Preserve the worker's current-date behavior. Coverage, rent and queue calculations cannot disagree with lifecycle at termination/next day or scheduled cancellation.

Current `terminated` storage alone is not evidence of the date of historical termination. For an imported agreement without sufficient retained termination facts, a requested earlier position has `PositionAvailability="unavailable"`, `Lease.Lifecycle="unavailable"`; unknown coverage, derived rent and end-dependent queue facts are not fabricated. Retained independently known generation/revision/settlement facts may still appear. Any lifecycle/ending/coverage/placement or financial predicate whose membership cannot be established must fail selection actionably rather than omit unknown candidates and claim complete totals. With no such predicate, include the Lease with explicit unavailable fields; any aggregate depending on that missing position is unavailable, never zero. Extend `LeaseExportSummary` with `RentAvailable bool`; false produces literal `unavailable` in matching_monthly_rent. Existing `RentCents` is meaningful only when true. Unknown row rent likewise serializes `unavailable`, not the struct's default zero. Initial current-company-date and snapshot instant/timezone are resolved once and supplied to every helper, including validation inside generation; never fetch a fresh clock per Lease.

Reuse existing `LeaseReportRow` for operational fields, adjustments and rent aggregates. In Billing mode, `GeneratedInvoices` supplies the authoritative invoice iteration (with its existing nested settlements); do not also emit `Lease.Invoices`. Similarly `Periods` replaces `Lease.Periods` for CSV and must not be emitted twice. Operational mode leaves all new financial slices nil, `FailureHistory` empty and financial totals zero; B does not serialize financial fields at all.

RentTerms are selected-date monthly terms, including scheduled rates separately labelled by Lease lifecycle, and exclude terminated/Draft active rent. They do not claim past worker knowledge. Billing revisions use the existing retained before/after rows; include only revisions recorded before selected company-day end, even when effective date is future. Preserve currently documented rent projection using corrected effective terms; describe this distinction in review/help text. Future approved aggregate rent schedule already in the browser need not become another export row type: revisions carry the approved rate change history. Do not invent a recording date for effective current rent.

Generated financial facts use `coalesce(generation.obligation_revision.financial, obligation.financial)` exactly as the current report, with corresponding captured line items. `commercial.Read` gives `Result.TaxCents`, `Input.TaxRate`, `Input.TaxExempt`. `LeaseBillingPeriodLine` already defines captured line keys `asset_id`, `unit_price_decimal`, `taxable`, `lease_billing_revision_id`. Use these; do not read current Invoice lines for original generation columns. Lines retain order even when Asset identity is redacted. Existing adjustments/settlements are already filtered but their reference anchors must still be captured.

## Owning queue semantics / retained-history limits

Inspected owning implementations: `lease_billing_worker.go:182–235,257–380`; `lease_billing_history.go:40–53`; `lease_coverage.go:497–564`; migrations 114/118; `lease_report_billing.go:137–193`.

1. Monthly candidate dates are first-of-month strictly after corrected commencement, no earlier than `managed_billing_start`, through selected date +30 inclusive. Initial due comes from retained finalized obligation/finalization; coverage charges come from accepted nonwithdrawn add events with positive optional charge and the same managed cutoff. Union retained obligation/failure keys so repairs do not erase provenance. Do not copy the worker's `LIMIT 24` or outer Lease `LIMIT 100` into a report.
2. Generated periods remain consumed after Void. `lease_invoice_generations` plus recorded-at cutoff determine generation history. Never infer pending from a voided Invoice; successor/replacement generations remain separate invoice rows. Ordinary status, review flags and settlement retain current report semantics/authority.
3. Ungenerated period disposition is derived with `effectiveLeasePeriodTerms(..., due, kind, asOf)`, not merely the mutable disposition table. It distinguishes termination, corrected commencement, no covered Assets/withdrawn charge. Nonempty disposition maps to `State="not_owed"`, preserving returned reason verbatim. No-owed periods never match pending/failed/ungenerated.
4. For nonarchived finalized agreements (including Scheduled initial charges within the window and Terminated retained owed periods), owed ungenerated due <= as-of +30 maps to current `pending`, unless a *current* unresolved `lease_billing_period_failures` entry exists, then `failed`. Shared entitlement/cancellation rules decide what is owed; “Active lifecycle only” is incorrect. Retained owed ungenerated periods beyond as-of +30 use `State="not_yet_eligible"`, never pending/failed, even if a current failure row exists. Current unresolved failure means no generation and no valid current-source disposition, as in the owning history CTE. `lease_billing_attempts.failure` is NOT a failure predicate: materialization deliberately inserts “Generation has not completed” there for ordinary pending work. Existing bounded enumeration stays unchanged; do not generate an indefinite future schedule to populate this state.
5. `billing=pending` matches the union of `pending` and `failed` states in the generation window (work remains); `billing=failed` is the failed subset. Existing `billing=ungenerated` matches those owed ungenerated dates <= as-of, independent of current retry status. Document this overlap, not independent additive counts. Historical effective termination can leave pre-end debt owed; do not relabel every terminated Lease's old owed period as not owed. Archived agreements do not have an actionable pending queue: retained owed ungenerated periods use `State="inactive"`, reason `Archived Lease; generation inactive`, and may match historical due-by-date ungenerated, but never pending/failed. Reuse owning entitlement for termination/cancellation, never broad lifecycle filtering that loses older owed periods.
6. Past-date output cannot claim past retry failures. Failure tables overwrite/delete entries on retry/success (`worker.go:369–380`), and disposition table is a mutable projection with no recording history. For a past as-of, set `FailureHistory="unavailable"`; effective owed absence with complete retained generation authority is `period_state="ungenerated"` inside its generation window, never `pending`, `failed`, or “no failure.” Retained future owed periods outside that selected date's +30 window are `not_yet_eligible`: known temporal eligibility is distinct from unavailable retry history. A future due date within +30 is eligible for generation, not automatically not_yet_eligible. These states say only that no retained generation existed by the selected cutoff and whether its window was open. Reject NEW historical `failed` and `pending` selections actionably because complete worker-queue selection cannot be reconstructed; never apply that rejection to existing Ready bytes. Current-date output uses `FailureHistory="current"`. Only actual retained evidence sufficient to prove historical status could justify a later refinement; missing evidence never implies success/pending. Never backfill or reconstruct old failures from today's table. This is a documented absence of facts, not a query error.
7. No Invoice/Pricing completeness: preserve the existing unavailable-generation behavior; no negative period rows/filters or inferred queue counts. Set a `period` row with `period_state="unavailable"`, blank kind/due/ID and safe reason, rather than outputting zero owed periods as an asserted fact. Positive authorized generated/review/void facts can still appear. `failure_history` does not confer completeness.
8. Pending means “effective owed period not generated within the generation window,” not guaranteed retry eligibility. The actual writer additionally checks active Customer/premises/Assets, timeline invariants and approved source details. Do not report those checks as successful without running their existing read-only predicates; do not invoke writer/locks/mutations during export.

## A → B: private reference contract

```go
type LeaseExportReference struct {
    Version int `json:"version"` // exactly 1
    Mode LeaseExportMode `json:"mode"`
    Summary bool `json:"summary"`
    Filter LeaseExportFilter `json:"filter"` // normalized accepted selection
    Selection LeaseExportSelectionReference `json:"selection"`
    CompleteGeneration bool `json:"complete_generation"`
    Leases []LeaseExportLeaseReference `json:"leases"` // nonnil, sorted unique
}
type LeaseExportSelectionReference struct {
    Customers []LeaseExportRelatedReference `json:"customers"`
    Premises []LeaseExportRelatedReference `json:"premises"`
    Assets []LeaseExportRelatedReference `json:"assets"`
}
type LeaseExportRelatedReference struct {
    ID int64 `json:"id"`
    Archived bool `json:"archived"` // archived visibility used in source snapshot
}
type LeaseExportInvoiceReference struct {
    ID int64 `json:"id"`
    JobID int64 `json:"job_id"` // original scope anchor; 0 for Customer-only
    Archived bool `json:"archived"`
    Settlement bool `json:"settlement"` // any settlement fact/derived status used
}
type LeaseExportLeaseReference struct {
    ID int64 `json:"id"`
    Archived bool `json:"archived"`
    Customers []LeaseExportRelatedReference `json:"customers"`
    Premises []LeaseExportRelatedReference `json:"premises"`
    Assets []LeaseExportRelatedReference `json:"assets"`
    Invoices []LeaseExportInvoiceReference `json:"invoices"`
    Actors []int64 `json:"actors"`
}
```

These are durable private authorization anchors, never CSV JSON fields. Per-entry references have exactly one Lease; summary references union all matching contributors. Each entry's child CSV rows can reuse its entire reference (slightly conservative across that Lease, correct whole-artifact policy). Non-summary empty reference, invalid version/mode, malformed IDs, unknown keys, or malformed JSON is forbidden. Summary empty Lease slice is valid only as an empty result and still rechecks filter authority/selection. B's strict decoder rejects unknown keys/trailing data and missing required keys, including Boolean keys (omission must not silently become false); A validates semantic invariants. All fields shown have no omitempty: required collections are encoded as nonnil arrays, including empty arrays. This typed boundary uses existing permission IDs only: `lease.export`, `lease.view`, `lease.billing.view`, `lease.archived.view`, `customer.view`, `customer.archived.view`, `location.view`, `location.archived.view`, `asset.view`, `asset.archived.view`, `invoice.view`, `invoice.archived.view`, `pricing.view`, `settlement.view`, `directory.view`; use existing `All`/`Assigned` constants and access helpers, never invent Lease financial or related-export grants.

`selection` is a required nonnull object with required lowercase `customers`, `premises`, `assets` JSON arrays; all three must be nonnil, even when empty. Their contents match normalized Filter IDs exactly: Customer=0 requires empty Customers, otherwise exactly one Customers reference with that ID; the same rule applies to Premises and Asset. Missing, null, extra, duplicate or mismatched selection entries are forbidden. These arrays are selected-input anchors, not the matching-Lease contributor arrays, and cannot be derived from output rows.

A captures each selected resource's identity and original archived state within the generation snapshot and includes the same Selection on every entry reference and summary, including a zero-matching-Lease summary. Rechecking performs current selected-resource access AND captured original access: a resource selected while archived still requires its archived View grant after restoration. Current archive state independently requires archive authority too. Merely re-reading the restored resource and seeing active is insufficient. This remains separate from temporal eligibility: no today/date/queue reclassification is introduced. A implements semantic validation and original/current authorization; B strictly decodes required selection/object/arrays before invoking A.

D adds/extends direct-reference and production-route tests: select an authorized archived Customer, premises or Asset yielding zero matches; generate Ready; restore the selected resource; remove that resource's archived View grant; assert whole-artifact status/download denial despite current active visibility. Cover all three selected types, missing/null arrays, missing selection object and IDs mismatching normalized Filter. Direct reference fixtures must populate required nonnil Selection arrays, not rely on zero-value structs. Entry and nonempty-summary paths must carry the same anchors; add assertions that neither can omit them. Use `TestLeaseExportArchivedSelectionRestoredThenAuthorityLost` for the persisted regression, alongside existing empty-sensitive-selection/reference tests.

Every reference rechecks time-independent `AuthorizeLeaseExportSelection`, including grants and IDs on empty results, never `ValidateLeaseExportFreshSelection`. Captured date/queue meaning is immutable: no next-day reclassification or current membership recomputation. `CompleteGeneration` is true if complete-generation knowledge influenced emitted facts or membership, even when filter is empty. Recheck completeness then; do not set it solely because requester happened to possess an unused grant. Billing mode is always a prerequisite even if selected results contain no amounts.

Each Lease must remain currently visible in company with base View; original archived requirement and any current archive state require independent archived View. Originally disclosed Customer/premises/Asset references must themselves remain visible with both original and current archive requirements. Relationship moves do not authorize originally hidden identities or require today's relationship to equal the old one: this is an immutable authorized snapshot.

Every Invoice reference requires current independent Invoice + Pricing access and original scope anchor authority. When All Records is currently granted, Job movement/assignment does not remove authority; otherwise require current and captured original Job access under compatible Assigned scopes. If `Settlement`, recheck Settlement View at both applicable anchors as well. Captured `Archived` and current archived state require archive grants; no tombstone escape. Include Invoices used by adjustments/positive filters, even if their only visible use is row membership. Actors require directory View and company membership as in existing disclosure. No File reference exists because no File information is exported.

Filter/membership-sensitive references: positive Invoice filter matching captures its authorized witness Invoices; Asset/placement/search relations capture every disclosed or matching related identity. A Lease summary uses same entry refs, so losing one contributor blocks the entire artifact, never a recomputed smaller total. Facts absent under original authority need no new reference; gaining authority must not enrich old bytes. Missing source or newly lost permission is unavailable/forbidden, not numeric zero.

## B → C: durable adapter public API

`internal/exports/leases.go` (B owns):

```go
const (
    LeaseOperationalKind = "lease-operational-A-v1"
    LeaseBillingKind     = "lease-billing-A-v1"
)
type Leases struct { Mode authorization.LeaseExportMode }
// Implements existing Definition and optional selectionAuthorizer exactly.
// Kind(), Permission(), Scope(), Columns(), Validate(raw), Authorize(p, raw),
// Snapshot(ctx, tx, p, raw, emit), Recheck(ctx, tx, p, reference).
func (Leases) AuthorizeSelection(context.Context, pgx.Tx,
    authorization.Principal, json.RawMessage) error
// Optional engine hook, invoked only for NEW acceptance under its existing fence.
func (Leases) ValidateAcceptance(context.Context, pgx.Tx,
    authorization.Principal, json.RawMessage) error

func DecodeLeaseExportFilter(json.RawMessage) (authorization.LeaseExportFilter, error)
```

`Permission()` is `lease.export`, `Scope()` is `authorization.All`. `DecodeLeaseExportFilter` strictly decodes a JSON object, rejects null/unknown keys/trailing tokens, delegates pure normalization; used for adapter validation and authorized recovery. C marshals the normalized typed filter. `Mode` must be explicit; no invalid-mode fallback to operational. Registry registers exactly the two valid values in `exports.Product`. B uses the existing engine for encoding/escaping/storage/hash/audit/fences/Ready publication; it does not implement another worker or download handler.

`Snapshot` delegates to A; emits fixed records with JSON-marshaled reference; emits summary last. Errors return to engine (all emitted rows roll back). `Recheck` strict-decodes and calls A; it never filters bytes or recomputes values. Each kind's columns are immutable fresh/copied slices so callers cannot mutate shared column state.

B implements `ValidateAcceptance` by decoding options and calling A's `ValidateLeaseExportFreshSelection`. C owns the minimal optional-interface invocation in `internal/exports/exports.go`'s `Accept` only, after existing same-intent receipt lookup and before new insertion; retain atomicity/audit/idempotency and all other adapters' behavior. The optional interface has exactly the method signature above. C must not put this invocation into shared `authorizeDefinition`, which is used for Ready artifacts. This is the sole additional shared-engine wiring file beyond C's existing registry/recovery ownership.

## Exact ordered CSV columns

Definitions below are ordered arrays, not illustrative field lists. Names/row semantics are frozen for these v1 kinds. Money uses exact signed two-decimal strings via `commercial.FormatCents`, never floats or currency symbols. Rates retain validated decimal rate text. Booleans are `true`/`false` only for applicable fields. IDs are base-10 (settlement IDs remain UUID strings); zero/unavailable identities are blank. Dates ISO; instants normalized RFC3339Nano. Blank means inapplicable/not independently visible; explicit `unavailable` fields distinguish unavailable aggregate/status knowledge from zero. No HTTP URLs, attachment identities, raw financial JSON or unrestricted revision JSON.

### Operational (27 columns)

```text
row_type,as_of,timezone,snapshot_at,lease_id,lease_number,title,lifecycle,archived,customer_id,customer,premises_id,premises,starts_on,fixed_ends_on,termination_on,first_monthly_on,renewal_starts_on,renewal_ends_on,asset_id,asset_number,asset_name,coverage_starts_on,coverage_ends_on,placement,matching_lease_count,history_status
```

Row types: `lease`, `coverage`, `summary`. Lease row includes fields through renewal dates; coverage row repeats that authorized identity/context and fills Asset/coverage/placement; summary has only row_type, as_of/timezone/snapshot_at and matching_lease_count. `history_status` is `available` for retained ordinary position, `unavailable` for explicitly missing retained facts (e.g. Draft detail snapshot); do not use it to imply missing permission. Existing unknown association period stays `placement=unknown` with history_status unavailable for that coverage row. Unknown position fields are literal `unavailable` where applicable, not blank or zero. No financial data even in title substitutions or summaries beyond the existing authorized base title.

### Billing (59 columns)

```text
row_type,as_of,timezone,snapshot_at,lease_id,lease_number,title,lifecycle,archived,customer_id,customer,premises_id,premises,asset_id,effective_on,recorded_at,actor_id,reason,monthly_rent,scheduled_monthly_rent,tax_rate,revision_id,before_rent,after_rent,before_tax_rate,after_tax_rate,period_kind,due_on,period_state,obligation_id,obligation_revision_id,invoice_id,invoice_number,generated_at,generated_total,generated_tax,tax_exempt,manual_invoice_status,effective_invoice_status,review_required,line_ordinal,line_amount,line_taxable,line_billing_revision_id,settlement_id,settlement_kind,settlement_amount,settlement_effective_on,reversed_on,adjustment_id,adjustment_kind,adjustment_amount,adjustment_starts_on,adjustment_ends_on,matching_lease_count,matching_monthly_rent,failure_history,revision_line_ordinal,history_status
```

Row types and population:

| Row type | Fields beyond common snapshot + authorized Lease identity (columns 1–13) |
| --- | --- |
| `lease` | monthly_rent, scheduled_monthly_rent, failure_history. Effective unavailable status is not inferred here. |
| `rent` | asset_id if visible; effective_on = selected as-of; tax_rate; monthly_rent for in-force, scheduled_monthly_rent for Scheduled. |
| `billing_revision` | one row/change: asset_id, effective_on, recorded_at, actor_id if visible, reason, revision_id, revision_line_ordinal, before/after rent/rate. Stable line key is revision_id + revision_line_ordinal, independent of Asset visibility. |
| `period` | period_kind, due_on, period_state, obligation_id when retained, reason. No invented Invoice ID or balance. Unavailable negative-generation placeholder follows rules above. |
| `invoice` | period_kind, due_on, obligation_id/revision_id, invoice_id/number, generated_at, generated_total/tax, tax_rate/exempt, manual/effective status, review_required. Effective status literal `unavailable` when Settlement absent. |
| `invoice_asset` | captured line ordinal/amount/taxable/revision, asset_id if visible, invoice_id/number and obligation ID/revision; do not repeat generated totals. |
| `settlement` | invoice_id/number; settlement ID/kind/amount/effective/reversed dates. Do not repeat Invoice amount or infer unapplied Credit. |
| `adjustment` | adjustment ID/kind/amount/start/end; invoice_id, asset_id and actor_id as authorized, recorded_at and reason. Invoice Number blank unless already loaded/authorized; no secondary pool lookup. |
| `summary` | snapshot fields, matching_lease_count, matching_monthly_rent and history_status; unique-Lease in-force rent, no Invoice/settlement total. Unavailable position makes dependent total unavailable. |

Order: Leases by Number/ID, then lease row; rent by Asset ID before redaction; revisions by effective date/ID then stored change order; periods by due/kind; generated Invoices by due/ID (each followed by captured lines then settlements in existing effective-date/ID order); adjustments by ID. Summary last. Operational children use existing Asset Number/ID order. Revision-line ordinal is retained array position, never an ordinal newly assigned by private Asset sort. Generated line key is invoice_id + line_ordinal. Invoice ID identifies its retained generation, linked to obligation_id + obligation_revision_id; original obligation revision is explicitly `0` in this applicable column, not a blank missing fact. No additional invented generation ID is needed. Operational counts/order do not depend on Billing child ordering or fields. Monthly rent repeated on `lease` and `rent` rows is explicitly different row granularity; only `summary.matching_monthly_rent` is an additive report total. Billing history_status follows position availability on lease/rent/summary rows and retained-fact availability on provenance rows; known numeric zero stays `0.00`, unavailable is the literal `unavailable`, inapplicable/unauthorized remains blank.

## C integration/ownership and D verification

C owns browser route `registerLeaseExports`, GET confirmation/POST UUID acceptance, `leaseExportSelectionURL(RecoverySelection) string`, report parsing/ending controls/export link, new export review template and generated template files. Integrate registration in `internal/handlers/exports.go`, recovery dispatch in `financial_exports.go`, catalog entries in `internal/exports/catalog.go`, and recovery type allowlist in `internal/exports/recovery.go`. B must not edit those four integration files. This supersedes earlier coordinator ownership to allow C integration without overlap; coordinator reviews the combined result.

GET/POST use `mode=operational|billing`; default GET operational, POST missing/invalid mode rejected. Browser page/per_page are dropped from a link to review, not accepted as export options. Review prominently states All Records, every matching Lease, mode, selected date or current-at-generation, retained-history limits, fields and 24-hour availability; lists these exact ordered columns. GET authorization/selection checks use an existing authority-fenced callback; no export job created. POST calls shared `Accept` with kind + normalized filter JSON and `operation_id`. Use existing status/download UI and CSRF. Never put mode in persisted filter JSON. C includes API-channel guard like existing report and must not register public field API routes.

Recovery uses only `Service.RecoverySelection` followed by B's strict filter decoder and kind-to-mode lookup, never raw job-table reads. Return a new GET review URL; new operation ID on review, no replay of previous POST. Recovery of authorized intent is time-independent; the subsequent NEW review applies fresh temporal validation. An old explicit date with pending/failed may require user-selected revised date/new review; do not silently change the old artifact, renew its expiry, or block its still-valid Ready download. C checks current authority before reflecting selected sensitive fields on failed validation/recovery.

Additional D test: `TestLeaseExportReadyAcrossCompanyMidnight`, table-driven for explicit current as-of and empty as-of, and pending/failed. Prepare Ready before company midnight, advance company date while still within Ready+24h, assert status remains available and exact CSV bytes download unchanged; expiry still occurs at Ready+24h and actual authority loss still blocks. Verify new review with old explicit queue date rejects actionably, and empty-as-of new generation resolves the new date. Cover same-intent receipt replay versus new acceptance after midnight. Also test Scheduled initial due and Terminated retained owed periods, plus retained outside-window `not_yet_eligible` in current/historical projections.

D exclusively owns all new `internal/handlers/lease_exports_*test.go` files AND browser script `testdata/browser/lease-exports.cjs`; C supplies production UI/selectors and owns public docs, not test/script files. B owns adapter/CSV contract tests; A owns domain projection/reference tests and changes to existing projection source, including narrow helper extraction in `lease_coverage.go`/`lease_billing_worker.go` if necessary. D reads column lists independently and parses CSV, asserts column counts 27/59, all row types, >100 Leases, unique summary arithmetic, every facet revocation including zero-match filters, immutable snapshots, exact June scenario, unavailable past failure history, no-truncation/error rollback and API denial. DB runtime setup is being handled separately; no test results are asserted here.

Required named acceptance tests (D owns; B/A may add supporting unit tests with distinct suffixes): `TestLeaseExportHistoricalFailureRetryUnavailable`, `TestLeaseExportOperationalPrivileged`, `TestLeaseExportGrantGainPreservesBytes`, `TestLeaseExportEmptySensitiveFiltersAndSummaryReferences`, `TestLeaseExportOriginalAndCurrentAssignedAnchors`, `TestLeaseExportMalformedRequiredReferences`, `TestLeaseExportAgreementPositionCoherence`. The agreement test covers corrected commencement/terms, inclusive termination/next day, scheduled cancellation, historical notice revise/withdraw and imported termination lacking history; compare lifecycle/coverage/rent/ending/periods within a single snapshot. Failure test creates a real failure then successful retry/deletion and proves earlier status stays unavailable, not pending/no failure. Operational privileged test proves financial readers are not invoked (including injected failure in financial-only query path), not merely that columns were hidden. Grant gain test compares exact stored/downloaded bytes before/after new authority. All tests use real PostgreSQL and production-equivalent HTTP where applicable.

## Handoff caveats

- The proposed types are additions, not claims that symbols already exist. Source projection internals are A's choice, but exported signatures/data semantics must not drift without notifying B/C/D.
- Existing imports/unknown terminated history must produce an explicitly unavailable position where facts cannot be derived; no fake dates/backfilled revisions. Malformed retained data/query failure is an error, not the unavailable-history sentinel.
- If normative review rejects the explicit unavailable historical failure queue, obtaining a true point-in-time failure log requires a separately agreed prospective history design; current tables cannot satisfy it by a query tweak. Do not backfill fabricated past failures.
- Changes outside these ownership boundaries or any extension to queue controls beyond the two Billing predicates require coordinator agreement. #94/#97 and generalized test harness remain outside scope.
