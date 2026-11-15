# Durable resolution evidence — UNINTEGRATED

This component does not repair runtime admission yet. It is private to the existing
backup owner; no exported constructor grants verified-generation authority.
`Control.New`, `Manager.clear/run/Recover/resolve`, startup and status callers still
need the sequential integration job. Filesystem tests are not real-engine proof.

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

## Required integration at actual seams (all five D3 points)

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
