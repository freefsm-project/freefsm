# Protected bounded Payment reference

`payment-client.cjs` exports `PaymentClient` and the shared `ProtectedStore`.
It uses the native Secret Service key boundary and authenticated encrypted cache;
the website remains online-only. The older in-memory Payment harness remains a
separate protocol compatibility example.

Authenticate, then `synchronize()` the assigned Invoice projection and independent
Payment-method authority. `stagePayment(target, values)` records the original
Invoice, Customer, Job, exact amount string, stable method identity, date and
optional description/reference/note. Method label changes do not rewrite it.
`submit(id)` explicitly submits once after durably checkpointing unknown status.

On interrupted transport, restart or access loss, call `reconcile(id)` first.
Lookup never posts a Payment. Only allowlisted disposition, effect identity and
acceptance time survive receipt recovery. Receipt contact withholds cached
financial content until current scoped synchronization; ordinary session loss
purges private intent. Lease expiry locks access. Unknown or invalid receipts
cannot authorize a replacement; missing receipts remain `unreceived`.

After an explicit server transient failure, `retryTransient(id, {confirm:true})`
checks the receipt again, refreshes current authority and observes retry timing
before retrying the unchanged original ID. Mere transport loss is not sufficient.
After terminal rejection, refresh and use `repairPayment(id, reviewedValues)` for
a new identity. Restore requires fresh authentication, scoped synchronization,
receipt lookup, explicit `acknowledgeUncertain(id)`, and reviewed new intent. An
absent restored receipt cannot establish that money was never received; the human
review must account for the possibility of lost post-backup financial history.

For completed entries, restore moves the safe former receipt to
`historicalReceipts`; `receipt` denotes only a receipt found in the current server
history. Historical acceptance is not proof that the restored server has the
Payment, and a missing receipt is not proof that money was never received.
Completed entries retain no private financial intent. After lookup, synchronize
again, explicitly acknowledge the uncertainty, then use `stagePayment` with freshly
reviewed values and a currently authorized Invoice. This creates a new identity
without replaying or reconstructing old values. `repairPayment` remains the path
for retained private intent. Every subsequent restore invalidates the lookup and
acknowledgment; safe historical receipts survive access-loss purges. Receipt-only
review does not grant access to the former Invoice or permit staging against it.

Ordinary grant or session loss during restore review also retains a safe
`restoreReview` marker bound to the reviewed server epoch, without target, amount
or other private intent. Purge clears lookup and acknowledgment. Regaining access
requires fresh authentication, synchronization, original-ID lookup, resynchronization
and a new explicit acknowledgment. A later restore resets these gates even when
the prior authority was purged. Ordinary unknown/purged operations without restore
provenance do not acquire this recovery path; changing users removes prior-user work.

Each Payment also retains its observed server `generation` independently of a
resolved review or cached authority. Finding a receipt resolves `restoreReview`
but does not erase this provenance. After ordinary purge, validated same-user
authentication compares that generation before returning: a later epoch archives
the former receipt and renews locked review immediately, even before synchronization
or lookup. Same-epoch login does not manufacture restore uncertainty. A legacy
receipt with no recoverable generation is treated as historical until lookup;
an ordinary unknown operation gains no review path merely from a missing receipt.

Shared adapters use the same Payment recovery lifecycle (`payment-recovery.cjs`),
including purge, restore, authentication and uncertain-discard protection. Before
another adapter operates on a store containing Payment work, its transaction removes
private Payment intent and retains only safe recovery metadata. This handoff resets
lookup/acknowledgment; another domain's synchronization cannot authorize Payment
review. Reopen with `PaymentClient`, synchronize current Invoice/Payment authority,
and reconcile the original IDs. Notes, Clocks, document creation, Commercial, PDF
and Delivery adapters cannot erase the uncertainty or present an old-generation
receipt as current by authenticating on behalf of the shared store.

No synchronization, restart or authentication automatically submits work. Uncertain
financial operation identities cannot be discarded to bypass reconciliation.
There are no Credit, refund or reversal operations in this adapter.
