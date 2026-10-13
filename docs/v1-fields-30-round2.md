# #30 round-two corrective evidence

Base: integration `6fb21df`. Findings: the two #30 root gaps in
`docs/v1-integration-21-46-round2.md`, AC4/5/7/9/10/11.

## Retained-global definitions and historical cycles

The shared SQL predicate `custom_field_applies_to_company` expresses the existing
owned-or-retained-global visibility rule. Conversion pairing/capture, historical
review and destructive-change guards now use that scope. A global definition is
not inferred to belong to a company and no ownership is backfilled.

Migration 073 validates overlapping key scopes before installing a replacement
pairing guard. Global/global and global/owned duplicate same-side keys reject;
opposite-side pairs require identical type, precision and option order. Owned
definitions in different companies remain independent. The existing statement
fence serializes concurrent global edits and owned insertions, so exactly one
overlapping claim can commit.

Original legacy cycles and earlier incomplete #30 captures stay immutable.
The effective snapshot reader flags missing capture evidence for explicit review;
it never reconstructs a pair from labels or synthesizes values. The existing
append-only review workflow accepts retained-global identities while continuing
to reject foreign owned identities. Current shared values recover to the exact
source field under the reviewed/captured pair after later key changes.

Tests seed definitions and a hidden historical cycle after released migration 042
(through 057) and run the real forward migrations. They retain NULL owners,
source bytes and identities through denial, review and recovery. Additional cases
exercise mixed ownership, numeric precision, multiselect order, overlapping-key
races, and an ambiguous upgrade that fails without rewriting definitions or
recording a successful migration.

## Company-safe revision responses

`fieldState` receives the authorized Principal's company after parent access is
checked. Its definition set is company-owned plus retained global for all callers,
including GET, successful reviewed patches, replays and terminal conflict
comparisons. Foreign-company definitions no longer reveal IDs or zero-valued
synthetic revisions. An authenticated two-company test also verifies that foreign
and nonexistent submitted definition identities have identical error responses.

## Verification environment

Own disposable PostgreSQL cluster: `127.0.0.1:55430`, UTF-8 data directory
`/var/tmp/freefsm-v1-30-round2/pgdata-utf8`.
Database: `freefsm_v1_ticket_30`.
Go temporary files, test temporary files and build cache are under
`/var/tmp/freefsm-v1-30-round2`; the existing worktree stays on `implement/v1-30`.
The shared cluster was not stopped or modified.

Red → green observations: foreign field `8001` appeared in the revision response;
retained-global conversion omitted the source selection; deletion incorrectly
accepted an unreviewed legacy global reference; an incomplete earlier capture
incorrectly allowed reversion. Each now has a passing HTTP/API regression.

The retained-global persisted Chromium journey exercises the actual forms and
CSRF: preview → convert → edit Invoice value → change pairing key → revert →
reload. The original typed-field retirement browser journey also passes.

Final command results, commit and integration ancestry are recorded in the
coordinator's `ticket-030-result.md`. Full general API cutover remains #41's scope,
as specified in the round-two review; immediate online compatibility is not
credited with reviewed/offline guarantees.
