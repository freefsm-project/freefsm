# Own clock synchronization

The API and encrypted reference use the existing Clock service and immutable
receipt namespace. The website remains online-only. Manual Create, Correct,
Void and clocking another subject are not API operations.

## Snapshots and deltas

`GET /api/v1/sync/clocks` uses the existing materialized snapshot protocol with
its own scope namespace: `scope=own`, default page size50, maximum200. `page`
and `cursor` are mutually exclusive opaque tokens. Unknown, duplicate or empty
parameters reject400. Apply complete record replacements atomically with their
continuation metadata; only promote the completion cursor after every page is
applied. Tokenless reads replace old snapshots. Continuity is at least30days;
unsafe/expired/cross-client tokens return409 `resnapshot_required`, with explicit
purge and resnapshot. Every page checks current Time View and separate GPS View.

Only the authenticated subject's active/clocked entries appear, including retained
correction/Void history. An existing manual entry reopened by the separate web
workflow remains the same active entry and can be closed exactly; its accepted
Clock-out and history stay in synchronization. This adds no manual mutation API.
`id` is the stable opaque snapshot identity; `entry_id`
and `subject_id` are exact decimal strings for existing installed identities.
Clock command endpoints retain their existing integer IDs. Batch commands also
accept exact decimal `entry_ref`/`job_ref`, mutually exclusive with their integer
counterparts; the encrypted client uses these for string identities and refuses
unsafe JavaScript numeric inputs instead of rounding. Receipts additionally expose
the exact decimal `entry_ref`. `job`, when present,
contains only retained ID/title, never Job context or navigation authority.
Assignment loss/archive does not remove that own retained reference or authorize
a new Job clock. GPS is omitted unless independently permitted and is never
copied into corrected instants. Unknown historical facts are not fabricated.

Closed nonvoid intervals expose exact `elapsed_microseconds` as a decimal string;
active, void or invalid retained intervals expose null rather than inventing an
end or rounding a duration. History is the existing recorded history, not a delta
event log. A replayed delta never invents another revision or observation.

## Ordered clock commands

`POST /api/v1/sync/operations` additionally accepts:

```json
{"operations":[{"operation_id":"83000000-0000-4000-8000-000000000001","type":"clock.out","depends_on":[],"clock":{"entry_id":42,"occurred_at":"2026-10-16T12:00:00Z","lease_id":"83000000-0000-4000-8000-000000000002"}}]}
```

`clock.in` permits optional Job/notes/GPS; `clock.out` names the exact original
entry. The nested clock has no operation ID: the outer identity owns this entire
intent. Dependencies are earlier members or known successful same-bound receipts.
No target mapping substitutes an earlier clock-in result: after an unknown in,
reconcile its original receipt and explicitly review the exact entry for out.

Batch bounds, preflight, ordering, per-operation atomicity, typed failures and
reviewed dependency repair follow [ordered batches](api-operation-batches.md).
Clock results use the original Clock receipt (`entry_id`, code, acceptance time),
queried at `/api/v1/time-operations/{operation}`. Same-ID replay recovers before
dependency reevaluation. Reusing a batch identity on the single-command route
cannot evade its dependencies. Terminal failure needs a new reviewed UUID;
transient failure retains the same intent and identity. No batch-wide rollback
removes earlier accepted work, and rejection never invents an end.

The [Clock contract](clock-contract.md) remains normative for staging leases,
occurrence/acceptance, geographic pairs and accuracy, exact-entry same-end no-op,
immutable GPS, overlap/active conflicts and explicit location-free repair.
No new permissions, automatic grants, migrations or historical backfill are added.

New Clock staging leases honor the existing offline policy (default24hours,
maximum72, zero disables). Same-ID issuance always retains its original stored
bounds, including after policy changes. Disabling offline access denies new
offline acceptance at commit; online server-time clocks remain available under
their own grants. No existing lease or stored occurrence is rewritten.

## Encrypted executable workflow

Invoke `node internal/api/v1/reference/offline-notes.cjs` with the same protected
stdin command envelope described in [offline Notes](api-offline-notes.md), adding
`workflow: "clocks"` to every command. Initialize a distinct store identity for
that workflow; the server/workflow binding cannot be changed on an existing store.
It reuses the same Secret Service key protection, authenticated encryption,
atomic persistence, writer quarantine and authorization state machine.

Authenticate online and synchronize first. Synchronization reads current own
clocks plus currently assigned Job clock targets and obtains the existing Clock
staging lease. General offline authorization controls cache/staging; the Clock
lease independently bounds clock occurrence/capture. Neither grants acceptance
authority. A lost issuance reconciles its original same-ID receipt.

* `stage-clock`: `clock_action` (`in` or `out`), reviewed `clock` fields, and
  explicit `depends_on`. Occurrence is captured once when staging. The result is
  pending, not accepted. Denied/unavailable location can be deliberately omitted.
* `submit-batch`: explicit ordered `operation_ids`; persists unknown before
  transport and never retries automatically.
* `reconcile`: the original `operation_id`, querying only the Clock receipt.
* `repair-clock-dependencies`: an `invalid_dependency`/`failed_dependency`
  operation's ID and individually reviewed `depends_on` references. It creates
  a new UUID while retaining the exact original target, occurrence, lease, notes
  and GPS. Descendants are never rewritten automatically; each requires its own
  explicit repair. Interval conflicts and different ends still require web repair.
* `submit-without-location`: terminal GPS rejection's `operation_id` and reviewed
  `depends_on`. It creates a new UUID, removes only GPS, and retains the original
  occurrence, exact entry/Job, notes and lease. Accepted/unknown work cannot use
  this repair. Server authority and interval invariants are checked again.
  A malformed GPS timestamp rejected by whole-envelope preflight has no server
  receipt, but its known pre-effect rejection permits the same explicit repair.
* `view`, `discard`, `acknowledge-uncertain`, and `logout` retain the existing
  explicit protected-store behavior. View exposes sensitive authorized data to
  its caller; do not redirect it to plaintext logs.

Fresh authentication and complete current-authority synchronization are required
after restore. An absent old receipt remains `restore_uncertain`, never retryable;
review the real recorded outcome and use the separate web correction workflow
where needed. Ordinary revocation instead purges sensitive content on contact.
This is a protocol exerciser, not a production mobile application or device
certification. Browser clock/correction workflows remain online-only.

### Additive protocol change

Own Clock sync, Clock batch members, exact decimal references, optional
`clock_job_id` on authorized Job snapshots, and authenticated `clocks` discovery
extend the existing v1 contract. Old single-command integer requests and their
immutable intent digests remain compatible. Existing installed facts/grants and
receipt namespace are preserved; no schema migration is needed.
