# Bounded field Lease observation

`GET /jobs/{id}/master-context` and the Job-bound Service Entry collection expose
`lease_coverage: {"covered": true, "as_of": "2026-11-04T12:00:00Z"}` only on
authorized operational Assets and authorized history rows. Job keys accept the
nonreused opaque ID returned by `/sync/jobs`, with installed decimal compatibility.
Asset and Service Entry keys remain their own identities. Resolution grants no
authority; current actor, assignment, Customer relationship and coverage are read
under the existing domain authority fence.

The field Service Entry collection requires current Job assignment even with
All Records Job, Asset and Service Entry grants. The separate office history
page retains All Records access without adding the field observation.

The answer is for the Job Customer on the current company date, using inclusive
coverage and read-derived Lease lifecycle. Placement is not coverage. Returning an
Asset preserves its Lease but removes operational field access; it does not yield
a fabricated No. History includes only the Job Customer's association periods.
Lease base/Billing grants never expand these field records or expose Lease routes.
The closed OpenAPI `FieldLeaseCoverage`, `FieldLeaseAsset`,
`FieldLeaseServiceEntry` and `FieldServiceEntryRevision` schemas are executable
allowlists. No Lease identity, Number, terms, notice, rates, Files, other Assets or
Lease history is included. Responsible IDs appear only for independently
authorized Service Entry revision on its exact current relationship.

Malformed, unknown and inaccessible Job keys are nondisclosing. Infrastructure
failure is retryable 503, never a denial or covered=false. Token and restore-epoch
rules run before disclosure. Service Entry create/revise retain original
operation identity, revision conflicts, commit-time authority and atomic audit.

The protected executable reference stores only the boolean and as-of observation
inside its existing encrypted Job cache. Display is **Last synced: Yes/No — as
of …**. Offline start, renewal or termination never recomputes it from local dates.
A refresh durably clears the observation before contacting policy or master
context, so failure of either request displays Unavailable, including after
restart. Failed invalidation persistence locks the protected store rather than
reviving an old observation. Offline display without a refresh retains the last
synced value within its authorization window. Synchronization
purges replaced/removed context after assignment, scope or operational loss;
session expiry/logout/restore retain their existing stronger purge/lock rules.

The reference refresh accepts an exact synchronized Job cache key. Its complete
Job-scoped invalidation removes the direct observation map, both singleton and
collection Asset observations, and their nested Service Entry observations in
one protected transaction before any network prerequisite. Other authorized
facts/history and other Jobs (even for the same Asset) are preserved. The store
writer lock also excludes concurrent views, refreshes and synchronization.

Direct Asset IDs are numeric; synchronized Asset IDs are opaque. The reference
never guesses aliases from position, names or Number. A successful direct refresh
restores observations for its returned numeric Asset IDs; old opaque observations
remain unavailable until a successful snapshot/delta restores them. Conversely,
sync retires direct numeric observations, including inherited unchanged delta
records, and display can use the synchronized opaque Asset key. This prevents
two identity spaces from presenting conflicting saved answers. A failed refresh
leaves neither representation accessible, including through the inherited cache
view after a fresh store/client restart.

Populated snapshots and deltas include authorized `service_entries` on
`SyncAsset`. The closed `SyncServiceEntry` schema uses opaque row IDs and the
same `FieldLeaseCoverage` observation. Direct field responses retain installed
integer row IDs; synchronized opaque IDs must not be used as installed IDs.
Synchronization materializes observations under the current-authority fence and
carries their original as-of timestamps; replacement/removal purges old
observations atomically. No production client or unrestricted Lease endpoint is
introduced here.

Conformance: persisted HTTP schema/forbidden-field cases, actual opaque sync keys,
numeric and opaque writer replay/revision preservation, authority-fence races,
company-date boundaries and a persisted protected Chromium reference journey.
