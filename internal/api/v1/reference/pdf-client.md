# Protected PDF reference client

`PDFClient` in `pdf-client.cjs` extends `CommercialClient` and uses the same
`ProtectedStore`, document identities, offline lease, synchronization and restore
lifecycle. Use one document kind per store. The native store requires a private
0700 directory and a working desktop Secret Service (`secret-tool`); browser
localStorage is not equivalent key protection.

```js
const {PDFClient, ProtectedStore} = require('./pdf-client.cjs');
const pdf = new PDFClient(new ProtectedStore(directory, storeID), transport);
await pdf.initialize();
await pdf.authenticate(token);
await pdf.synchronize('invoice');
const review = await pdf.reviewSource(documentUUID);
// These actions work offline within the current authorization lease.
const operation = await pdf.stagePDF(documentUUID, review.source_token, []);
const pending = await pdf.view();
// Explicit online action; persists unknown before POST.
await pdf.submit(operation);
```

The transport signature is `(path, method, body, auth)`. Send `auth.token` as a
bearer, `auth.epoch` as `X-FreeFSM-Session-Epoch`, and `auth.operation` as
`Operation-ID` when present. Return `{status, data, error}` from JSON envelopes,
or `{status, bytes}` for successful PDF downloads. Throw on uncertain transport
outcomes. Never strip typed recovery errors. The client does not render or cache
PDF bytes.

## Review and reconciliation

- `reviewSource(target)` fetches and encrypts the authorized preview and token.
  `view().sources` exposes these only while synchronized and within the lease.
- `stagePDF(target, token, dependencies)` creates a new immutable original-parent
  intent and operation UUID, with no network traffic or accepted-PDF claim.
  Synchronization preserves supported PDF intent, rechecks its own Generate
  authority independently of commercial Pricing, and never refreshes its token.
- `submit(id)` accepts only pending intent. A failed unknown-state checkpoint
  prevents transport and quarantines the store. Lost responses remain unknown.
- After restart, call `reconcile(id)` first. Receipt lookup needs an authenticated
  session but neither document visibility nor a live offline lease. It returns
  safe effect metadata and withholds cached observations pending synchronization.
- An ordinary missing receipt produces `unreceived`, never an automatic POST.
  `compare(id)` refreshes current authority and compares the original source.
  `retryUnreceived(id, {confirm:true})` rechecks the receipt, synchronizes, compares
  the exact original source again, and only then sends the unchanged intent.
  Throttling/transient failure retry deadlines remain enforced.
- Terminal failure requires `compare(id)` followed by
  `repair(id, current.source_token, reviewedDependencies)`, yielding a new UUID.
  Dependencies are explicitly reviewed anew; no parent or dependency is retargeted.
- Restore locks recoverable intent. Fresh authentication and synchronization
  permit receipt lookup. A missing restored receipt remains `restore_uncertain`;
  after synchronization, explicitly `acknowledgeUncertain(id)`, compare and repair
  with a new reviewed ID. Neither authentication nor restore recovery replays it.
- Ordinary scope loss purges sensitive intent. Conversion/reversion receipts
  immediately purge old-parent PDF reviews and intent through the shared
  commercial lifecycle. Lease expiry locks access; `logout()` destroys keys/data.

## Exact versions and Download

`versions(target)` supplies the server's newest nonstale default. Once explicitly
chosen with `selectVersion(target, versionID)`, the exact ID survives authorized
synchronization and later generation; it never falls back to a newer version.
`download(target)` returns bytes directly or a `stale_pdf` warning naming that
exact version. After displaying the warning, call
`download(target, {confirmVersion: versionID})` to send the audited stale
confirmation. A different ID or confirmation without a warning is rejected.

Download is independent of Generate. For online retained/archived versions,
`retainedVersions(kind, documentID)` and
`downloadRetained(kind, documentID, versionID, {confirmVersion})` work without an
offline lease or synchronized document. Every request checks current server
authority, including archived visibility. These methods retain no document data
or bytes, only the safe exact-version warning needed for explicit confirmation.

Failed `versions`/`download` reads durably withhold cached observations before
returning an error. A well-formed API `not_found` triggers a read of the original
document: confirmed parent loss purges that target's private pending payloads and
selection metadata, retaining only safe operation/receipt metadata. A missing
version, malformed response or transport failure alone never proves parent loss;
intent stays locked for fresh authorized synchronization. A failed withholding or
purge checkpoint quarantines the store, including after process restart.

`acceptedVersion(operationID)` hands off only the accepted exact `version_id` to
an authorized current-document caller. Full Delivery staging and reconciliation
belong to #92; there is no implicit Generate or substitute-version Delivery path.

`PDFReference` remains exported for legacy synchronous in-memory conformance
harnesses. It shares receipt/source matching rules but cannot provide durable
protected persistence. New integrations use `PDFClient` and its single inherited
protected receipt-reconciliation lifecycle.

## Focused verification

`TestPDFProtectedReferenceHTTP` executes the JS harness against the real routed
API and migrated PostgreSQL: encrypted offline review, shared commercial sync,
process restart after lost response, publication/checkpoint faults, explicit
retry, source drift/new-ID repair, restore, scope/session loss, expiry,
conversion purge, independent Download, archived access and exact stale choices.
Legacy PDF harness tests retain existing compatibility coverage.
