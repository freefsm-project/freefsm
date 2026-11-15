# Durable operation status

The initiation owner stores a versioned status projection in `backup-admissions`,
outside disposable job directories. Existing initiation receipts still own dispatch
deduplication; this projection cannot dispatch, retry, restore, clean up, or open
admission. Existing raw legacy initiation receipt representations are preserved.
New status paths and records contain only the domain-separated operation digest,
actor ID, destination/upload-root hashes, bounded fixed-vocabulary diagnostic
references and the existing resolution binding. No new bearer secret is persisted.

`newJob` persists the actor/capability binding before dispatch. Restore acceptance
records queued restore intent before changing the process-local upload job.
`selectedResolution` links the original journal decision and maintenance transition
before cleanup. `run` persists terminal/unresolved disposition and diagnostic history;
write failures make its process-local phase unresolved. Preflight failure with
pending activity retains a failed outcome and separate `ActivityPending` fact.
The earliest diagnostic remains the primary failure even after reconciliation.

`Status` checks the original actor as well as the capability; `StatusCapability`
needs only the existing secret, without querying an unavailable authentication DB.
Digest knowledge alone does not authenticate. Missing/corrupt/unreadable evidence
returns the same nonsensitive not-found error. Reconstructed results omit source,
archive metadata, actor, destination, paths and DSNs. They explicitly identify
themselves as reconstructed and never recreate downloadable/staged jobs.

For a selected restore outcome, terminal completion requires the exact original
maintenance transition and resolution binding in the current verified-open record.
`Control.ConfirmResolution` holds shared intent then shared admission through file,
summary and generation verification. It neither admits work nor invokes recovery.
Selected/resolved/ready evidence alone remains unresolved. Later closure, generation
change, or reopening in a different transition cannot acknowledge the old release.
Read-side synchronization may reconcile a previously ambiguous write; no success
cache is retained. A verified rollback/no-replacement projects failed, not complete.

Writers use the existing operation reservation, atomic replacement and file/directory
sync. History is append-only and bounded at 256 references; exhaustion stops writes.
Legacy journals without status evidence remain recoverable but cannot invent actor
bindings or status. No automatic pruning or replay authority is added.

## Routing and activity reconciliation

Initiation lookup keeps legacy dispatch receipts but projects current durable status
when available, including upload-to-restore changes. `available` continues to describe
live artifacts; `status_available` permits querying reconstructed status without
recreating those artifacts. Missing legacy status does not invent a binding.

The HTTP capability route strips source/build/archive review metadata even for live
jobs. The protected actor-bound review route requires current admission and authority.
The status-only shell renders reconstructed provenance, selected outcome, release,
pending activity and retained diagnostics; it cannot restore or download. Only exact
verified-open release offers restored-account login, even with retained diagnostics.

New pending ordinary activity records link the operation digest, never its bearer.
After successful idempotent publication, replay durably clears that operation's pending
flag before removing replay evidence. A failed status write retains replay evidence;
legacy events cannot invent a status binding. Diagnostic history and known failure
remain unchanged. Synchronous upload failures also retain their known durable outcome.

## Remaining qualification

Actual whole-engine crash/recovery qualification, D2/email,
native and original full-scope acceptance remain separate mandatory jobs. Filesystem
tests with test-local verification entitlement are not installed-engine proof.
