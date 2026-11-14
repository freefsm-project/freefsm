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

No synchronization, restart or authentication automatically submits work. Uncertain
financial operation identities cannot be discarded to bypass reconciliation.
There are no Credit, refund or reversal operations in this adapter.
