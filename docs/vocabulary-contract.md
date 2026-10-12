# Tags and Asset vocabulary (#28)

Migration `058_vocabulary_name_key` freezes the shared normalization rules;
`059_vocabulary` adds retirement and normalized-name uniqueness to
Tags, Asset Types and descriptive Asset Statuses. It preserves definition IDs,
stored labels/colors, references and existing activity. Retired names remain in
the unique indexes. Type and Status are separate single-installation lists;
Tag uniqueness is company-wide.

Names use default Unicode case folding and outer Unicode whitespace trimming.
The same rule is used by command validation, read-only preflight and the unique
indexes. `make generate` generates the immutable PostgreSQL function from the
pinned Go/x/text rules. It consumes complete UTF-8 sequences and returns a byte
key, so SQL_ASCII and UTF8 databases enforce the same identity without collation
dependence or byte-set trimming. Internal whitespace, accents and distinct
spellings are preserved. Invalid legacy encoding requires explicit review.
Changing these frozen identity rules requires a reviewed forward migration.

## Authority and transactions

- `configuration.tags.view/manage` and `configuration.assets.view/manage` govern
  their respective configuration pages and changes. Manage requires its View
  through the existing permission catalog. This migration adds no grants.
- `tag.attach` and `tag.detach` are independent parent-bounded actions. All Records
  applies to the existing Customer, Project, Job, Asset, Estimate and Invoice
  parents. Assigned applies to current assigned active Jobs and their authorized
  Job-bound documents. Context never unlocks master collaboration.
- Definition configuration grants neither parent access nor Tag attachment.
  Parent Edit is not a prerequisite for attachment or detachment.
- The browser routes and actor-aware service adapters call
  `authorization.Service.ChangeVocabulary` / `ChangeTagLink`. These acquire the
  shared authorization transaction mutex, reload the principal/session and
  effective grants, lock/recheck domain state, write ordinary activity and shared
  immutable audit, and check the principal/session again before commit.
- Definition audit captures operation and full before/after facts. Definition
  history pages disclose only definition facts under configuration View, with
  keyset pagination. Deletion does not delete the immutable event's target ID.
- No public field API configuration or Tag-mutation routes were added.

## References and lifecycle

Database uniqueness protects concurrent creation and rename. Restrictive foreign
keys include archived Assets and all Tag links. New selection takes a shared
definition-row lock that conflicts with retirement/deletion; attaching to a
parent also locks and checks that parent. Assigned actions lock the actual Job
and assignment row. Hidden converted documents are not ordinary Tag targets.

Retirement prevents new selection, not display or historical references.
Unchanged Asset Type/Status references are exempt from selection validation, so
an unrelated edit can retain a retired required Type. Replacing a reference
requires an active definition; editing an Asset never reactivates a definition.
Status is only a descriptive reference and has no work/ownership/lifecycle
effects. Reactivation changes selection eligibility only.

Current UI selections omit retired Tags and new retired Type/Status choices.
Existing retired Type/Status selections remain available on that Asset's edit
form. Retired Tags remain visible on archived parents. Archived parent Tag
mutations are denied.

## Non-destructive upgrade review

`DB.VocabularyPreflight` takes a repeatable, read-only snapshot and reports IDs
for blank/normalized-colliding definitions and missing/unsupported legacy Tag
targets. It detects both table and column availability, including the original
global Tag namespace before migration 021. `DB.Migrate` calls it before running
*any* pending migration when 059 is included. This prevents historical 046 cleanup from
erasing evidence before review. Collision checks are repeated within 059 before
the new constraints are installed in 059.

If preflight rejects, retain the database and reported records for explicit
operator/domain review. The upgrade does not merge records, guess missing
targets, delete orphans, synthesize historical events or choose renamed labels.
The historical migration files remain unchanged. A clean upgrade can be rerun
without rewriting recorded facts.

## Verification

`internal/handlers/vocabulary_integration_test.go` uses authenticated production
routing and migrated PostgreSQL for grants, normalization, lifecycle, all six
parents, archived dependencies, rollback on either audit sink, revocation while
a writer waits, concurrent definitions/references and the public API exclusion.
The opt-in Chromium test uses real CSRF-protected HTTP and verifies persisted
Tag/Type effects. `internal/database/vocabulary_migration_integration_test.go`
tests preflight and preservation from a genuine historical schema.
