# Lease workspace and related records

`/leases` provides Number-ordered, 25/50/100-row discovery. Search covers
Number, title/reference and independently visible Customer, Asset Number and
premises title/address. Archived masters participate in search only with their
archived-view authority. Billing-only discovery retains its Number-only surface.

Queues cover upcoming starts, fixed terms entering Month-to-Month within 30 days,
pending terminations, placement exceptions, monthly billing, unresolved generation
failures and generated-Invoice manual-review exceptions. Counts are Lease counts,
not period counts. The Billing link opens the existing period/history/retry
workflow. Billing queues and filters are authorized before their counts are read;
base-only access cannot query or infer them. No dashboard widget is added.

Lifecycle, Customer/Asset/premises relationship, ending-date, archive, placement
and Billing filters travel in the URL and pagination links. Ending bounds use
the current notice effective date when present, otherwise the fixed-term end.
Lifecycle and notices derive from the authoritative company date and retained
history. The last successful query is retained in an HTTP-only, user-specific
browser cookie; an explicit URL overrides it and Reset clears it. Restored query
state never substitutes for current authorization.

Customer has a separate Leases section outside Related Work. Asset shows retained
coverage intervals independently of its placement history; Location shows premises
relationships. Cards show the first 25 Number-ordered Lease identities and link
to filtered discovery. Archived related references without archived Lease View
are bounded to Number/title/badge, with no details, coverage dates or navigation.
Billing navigation is independently authorized. File and generated-Invoice access
remain on their existing independently authorized surfaces.

The workspace adds no migration, permission, financial ledger or mutation domain.
Archive/restore, master dependency serialization, coverage, placement, billing,
immutable audit and replay continue through the existing services and shared
transaction fence. Installed identities, snapshots and grants are unchanged.

Owning HTTP/PostgreSQL/Chromium verification is
`TestLeaseWorkspaceRelationshipSearchHTTP` and
`TestLeaseWorkspaceTerminatedArchiveBannerHTTP`. Focused qualification also retains
all 134 accepted predecessor roots, including archive/restore commit orders,
authority loss, audit rollback, populated upgrades and encrypted history recovery.
Full-suite qualification and independent acceptance remain parent-owned gates.
