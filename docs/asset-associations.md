# Asset associations and retained Job links

## Product actions

Asset → **Association history** provides separately granted `asset.transfer`,
`asset.place`, `asset.return`, and `asset.history.correct` actions. Each submission
requires a reason, a company-local effective date, and the reviewed association
revision. Dates cannot be future-effective. Number and ownership class remain
immutable. Return is Company-owned only and clears both Customer and Location.
Inputs are independently authorized; an omitted Location means no Location,
never a Location carried over from the former Customer.

The full affected timeline is validated inside the same authority transaction as
its current projection and immutable audit. Correct revises an existing original
event; its original facts and every revision remain append-only. Real subsequent
moves use new events. Dates order periods; immutable original event IDs order
multiple real changes on the same day. Such changes remain distinct recorded
periods rather than being collapsed into a fabricated ownership interval.

Job → **Manage Asset links** supports multiple explicit Assets under
`job.relations.edit`, independently of planning Edit. New links require active,
authorized, same-Customer equipment. Leaving a Customer preserves prior link
labels and marks them permanently historical and non-authorizing. Returning to
the same Customer never revives those links: a new explicit link is required.
Unlink is a separate authorized repair and retains its original evidence.

Backdated changes and corrections reject if an affected retained Job link no
longer fits its recorded association period. A legacy link with an unknown date
cannot certify a backdated change; explicitly repair its relationship first.
Current-dated departures can preserve it as historical without inventing a date.
Customer pages keep **Current Assets** separate from **Past and current Asset
periods**; the latter includes each revisit and exposes no other Customer's
periods or navigation through historical references.

## Reviewed upgrade

Before an affected cutover, have an existing product Administrator review the
read-only inventory. Operator access alone does not decide historical ownership.
Use the same explicit database URL as the existing Asset identity preflight:

```sh
FREEFSM_ASSET_PREFLIGHT_DATABASE_URL='postgres://…' \
  go run ./cmd/freefsm-asset-preflight -associations > association-review.json
```

Keep the inventory `snapshot` and `evidence`. Record the Administrator's identity
in `reviewer`, their explanation in `reason`, and explicitly approve both
`administrator_reviewed: true` and `retain_observed_unknown_dates: true`. Supply
that restricted file as `FREEFSM_ASSET_ASSOCIATION_REVIEW_FILE` for explicit or
startup migration. Existing identity review remains separately required when
that earlier migration is pending.

Missing, stale, or incomplete review stops before applying pending migrations.
The locked cutover rechecks the inventory and stores immutable review evidence.
It preserves observed owners, placements, original Job pointers and cross-Customer
links. Cross-Customer links become flagged historical references. Unknown dates
and ambiguous earlier transitions stay explicitly unknown. No acquisition or
transfer event is synthesized from creation timestamps, and no nonprotected Role
is auto-granted permissions.

## Integration contracts

- `asset_association_origins` stores the observed association, explicitly not an
  acquisition date. `legacy` distinguishes imported observations.
- `asset_association_events` is append-only. Corrections reference an original
  event and retain its before-fact evidence, actor, acceptance time and reason.
- `asset_association_facts` selects each original event's latest factual revision.
- `asset_association_periods` provides all periods, including revisits, with
  stable `event_id`, Customer, Location, nullable unknown start, and next boundary.
  Equal-date moves are ordered by original event ID. Day-only Job link evidence
  is pinned to `association_event_id`, rather than inferred from today's owner.
- `job_asset_links` is the canonical multi-Asset relationship. `operational` and
  `unlinked_at` must both be checked for current authority, together with active
  Job/Asset and matching Customer. Historical labels are frozen. `jobs.asset_id`
  remains a compatibility slot; its writes validate and mirror into this table,
  and its presence alone grants no authority. Do not join it directly to current
  Asset fields for historical readers.
- Assigned Job master context reads the canonical operational collection under
  the authority fence. Web renders every authorized operational Asset; the API
  adds `assets` while retaining `asset` as a compatibility projection of its first
  member. Each member uses the existing closed field allowlist. Repairing one
  historical link never unlinks a distinct current visit to the same Customer.
- Association Location snapshots remain historical facts. Correcting an earlier
  period does not reset an unrelated current same-Customer Location edited later.
- Service Entry and Lease slices consume these periods and link identities while
  retaining their own authority, coverage, premise and revision rules. Placement
  never implies Lease coverage or changes an agreement. This ticket does not
  implement the Lease constraints, Customer cascade, or the final browser matrix.

The shared authorization fence serializes authority changes, master lifecycle,
association changes and Job linking. Audit failure rolls the entire action back.
Placed Company-owned Assets and operational unfinished Job dependencies block
archive, independently of descriptive Asset Status.
