# Durable resolution evidence and persistent admission

The existing backup Manager owns verification. `resolve` retains selected identity
in the scoped closed lease before installed-state verification, activity publication
and resolved advancement; only then can recovery/journal cleanup run.
`finishAdmission` reconciles retained identity even without a journal, verifies the
installed destination, replays pending activity, finishes cleanup and pool reset,
and persists release-ready. Only that private path sets `releaseBinding`.
Control's lease invokes `PrepareAdmission` with its exact identity; caller strings
or a syntactically ready record cannot create the Manager's release entitlement.

Ordinary Enter takes shared intent then shared admission, synchronizes the exact
versioned discriminator and invokes Manager.VerifyAdmission to synchronize the
exact referenced summary and current generation. Missing/legacy/unknown/corrupt
or inaccessible state denies. New never opens. No successful-sync cache exists.
Open publication errors after rename are release-unresolved, with no compensating
write. A later closed publication supersedes prior open evidence. ClearMaintenance
is a rejecting compatibility shim, not an authorization path.

BeginStartup owns operation and exclusive maintenance through recovery, migration,
initialization and host recovery. Finish verifies before opening; Close alone keeps
closed. Explicit `-migrate` uses the same startup path. Bootstrap's transition is
retained in the closed discriminator before verification and reused on retry.

Durable actor-bound status is described in `status_contract.md`; routed UI and
whole-engine qualification remain pending. `run` never marks
committed cleanup/release errors complete; ready evidence alone is not terminal
success. Filesystem adapter tests are not whole-engine or native-reboot proof.

## Representation and persistence

`resolutionRecord` version 1 has immutable `resolutionBinding`, monotone `Stage`,
and append-only bounded `resolutionDiagnostic` references. The record is canonical
`encoding/json` struct JSON (optional outer whitespace only); unknown, duplicate,
case-aliased fields, unsupported versions and invalid combinations reject. Maximum
64 KiB and 256 diagnostic references; exhausting the bound stops advancement,
never discards history. Diagnostic messages come from existing fixed categories.

Files are `Manager.root/resolution-<transition digest>.json`, mode0600, directly in
the persistent backup root, not in disposable job/recovery directories. Separate
transitions retain old evidence. No pruning is implemented. The original journal
remains the sole recovery decision authority; these records cannot apply/rollback
DB/files or establish truth by their mere existence.

`journalResolution(j, generation)` derives a domain-separated transition digest
from the original `journal.Recovery`, and status digest from its original 256-bit
operation ID. It checks destination/upload configuration and permits only capturing
→no-replacement, committed→restore, rolled-back→rollback. Replacing is unresolved.
It does **not** verify the supplied generation. Generation must come from verified
installed state and the durably published `schema-generation.json`. The distinct
bootstrap kind has no operation/journal phase and only no-replacement outcome;
its transition must be durably retained by the future maintenance owner before
verification, never regenerated on an ambiguous retry. Bootstrap is not restore.

Neither file names nor contents expose the operation/status bearer, DSN or upload
path. Destination uses the existing Manager destination hash; upload identity is
the hash of its absolute configured root. This intentionally binds configured root
identity as existing journals do, not the replaceable upload-directory inode.

`persistResolution(next)` obtains the directory descriptor before mutation, reads
and synchronizes existing exact evidence, validates immutable identity/stage/history,
then writes a private temporary file, file-fsyncs, atomically renames and fsyncs the
already-open directory. Prepublication faults retain prior bytes; postpublication
errors are typed `resolutionError{Kind:"unresolved"}`. No compensation occurs.
Identical retries synchronize existing bytes without rewriting the inode.

`readResolution(binding, exactStage)` validates all fields, fsyncs the validated
file descriptor and directory, checks current path/inode identities, and rereads
the same descriptor to reject changed content. Every read repeats persistence;
there is no successful-sync cache. Missing/corrupt/unsupported/mismatch/stage/
persistence errors return no usable evidence. `resolutionError.Error` is fixed
vocabulary and never includes identities; raw causes are internal only.

## D3 obligations at actual seams (qualification checklist)

1. **Original identity:** at `Manager.restore` retain the journal identity through
   every `writeJournal` and recovery. `Manager.resolve` uses `journalResolution`
   after the existing journal has selected committed/rolled-back/capturing outcome.
   Do not derive unrelated release IDs. `Recover` must inspect retained resolution
   evidence after journal removal instead of treating absence as verification.
2. **Ordering:** persist `selected` before `resolve` removes any identifying journal
   or recovery material. It records the selected decision, not release permission.
   Persist `resolved` after coherent generation verification and activity publication
   (`publish`/`replayPending`) have succeeded; journal/recovery cleanup is still owed.
   Persist `release-ready` only after `cleanupRecovery`, journal removal and directory
   sync, abandoned cleanup, pending replay, required generation publication and pool
   reset preparation all succeed. Never skip/regress stages. After a partial failure,
   reverify/reconcile actual obligations under the journal/summary identity, then
   retry the same record. Keep the last recovery copy until the journal decision
   permits cleanup. No record causes deletion by itself.
3. **Authority/lease:** writers must hold existing operation → exclusive intent →
   exclusive admission locks for the complete read/modify/write. The future scoped
   maintenance lease replaces `ClearMaintenance`/compensating `KeepClosed`; only
   the Manager verification path may offer a matching release-ready binding. The
   component is not that lease and never exports a string-based release helper.
   The open discriminator should reference this exact binding; it must not duplicate
   an independent generation decision. A record alone cannot mean admission open.
4. **Reader coupling:** fresh readers hold D1 shared intent → shared admission
   through BOTH exact open-discriminator verification and `readResolution(...,
   resolutionReady)`. Later closed transitions supersede old ready records. Matching
   includes transition, operation, destination, upload root, generation and outcome.
   The caller supplies the currently closed/open transition from the lease protocol,
   never searches history for any ready file. All writers honor these locks. Files
   and backup root cannot be renamed/mutated outside that protocol. The inode/content
   checks detect accidental violations but do not replace serialization. The future
   cross-package reader seam must preserve this ownership without importing backup
   into instancecontrol or exposing arbitrary generation minting.
5. **Status and restart:** `Manager.run` must remove committed-error→complete behavior.
   Selected/resolved project cleanup-required; release-ready projects release-pending
   until matching admission publication is durably verified. Release ambiguity stays
   unresolved, never complete. Preserve diagnostic references even after later release.
   Status capability lookup hashes the presented secret with `resolutionHash("status",
   id)` and checks exact operation identity; never return the secret or full binding
   in diagnostics. Ordinary `Status(actor,id)` additionally needs retained authorized
   actor binding from the existing status owner (this component grants no actor access).
   Startup must retain exclusive maintenance through recovery/migration/initialization,
   reconcile journal+summary+installed generation, and only then publish verified-open.

Full D3 integration tests must cover actual restore/rollback with distinct sentinels,
commit followed by cleanup/release errors, crashes after each summary/journal/cleanup
boundary, repeated Recover after journal removal, fresh-process persistent EIO and
repaired exact binding, D1 concurrency, startup/migration, and same-operation status.
D2 atomic email/attempt fencing and all original ticket/native gates remain mandatory.
