# Ticket 97: Contact, Location and Project CSV adapters

These adapters implement the Contact/Location/Project slice of #97 and parent
#18 (MAS-01, MAS-05, PER-05/10/12 and DISC-05). They implement `exports.Definition`
and use the existing durable, requester-only CSV lifecycle. Registry, HTTP and
navigation wiring are owned by the integration coordinator.

## Selection and authority

| Adapter | Kind | Required A grants | Suggested route |
|---|---|---|---|
| `Contact{}` | `contact-operational-v1` | `contact.export`, `contact.view` | `/contacts/export` |
| `Location{}` | `location-operational-v1` | `location.export`, `location.view` | `/locations/export` |
| `Project{}` | `project-operational-v1` | `project.export`, `project.view` | `/projects/export` |

All accept `MasterRecordOptions`: `search` (optional literal case-insensitive
substring), `sort` (required: `name`, `name_desc`, `updated`), and `archived`
(default false; true includes active and archived records and requires the
resource's A `archived.view`). Contact and Location additionally accept optional
`customer_id` (`CustomerID`); Project accepts optional `status_id` (`StatusID`).
Zero means no selection; negative IDs and filters for the wrong resource are
rejected. Selected Customers require A Customer View and current company/archive
visibility; selecting an archived Customer also requires the explicit archived
option. Selected Status must belong to a Project workflow and the current
company or retained-global scope. These selections are reauthorized even for
empty results at acceptance, generation, status and download. Unknown keys,
pagination and invalid sorts are rejected. Name sorting uses Contact first/last name,
Location title or Project name; the ID is the stable tie-breaker. Descending name
and updated sorts use descending IDs. Updated means newest first.

Search matches the production collection fields: Contact name/email/phone,
Location title/address, and either Project name or description. IDs do not
participate, and Project queries do not match across the name/description
boundary. Child collections preserve their joined-field search semantics.
Related Customer or Location content cannot influence search or ordering.
Duplicate identifying values remain separate rows. Customer Locations only are
included; other legacy polymorphic Location types are not reclassified.

Customer View and Job View are not prerequisites. Each relationship has an
explicit visibility column: `visible`, `archived`, `restricted`; the optional
Project Location additionally uses `none`. Restricted relationships have **both
ID and label empty**. A related archived record is disclosed only with its own
A View and archived View. Customer and Location disclosures are independent.

Stored authority references contain the resource identity/archive flag and
only relationships (including explicit Job links) actually disclosed. Generation/status/download recheck
current resource and disclosed relationship existence, company, View and archive
authority; the lifecycle blocks the whole export on loss. A previously archived
disclosure still needs archived authority even if subsequently restored.
Undisclosed relationships create no revocation dependency. Relationship changes
do not rewrite the historical snapshot or its authority references.

## Fixed columns, in order

### Contact

```text
contact_id,first_name,last_name,role_title,email,phone,service_notes,notes,primary_customer_default,customer_id,customer_label,customer_visibility,job_links_json,job_links_visibility,archived
```

### Location

```text
location_id,title,address_1,address_2,city,state,zip_code,latitude,longitude,access_notes,service_notes,notes,primary_customer_default,customer_id,customer_label,customer_visibility,job_links_json,job_links_visibility,archived
```

Contact Customer and Location Customer ownership are immutable. The
`primary_customer_default` boolean describes only a Customer default, never a
Job link, assignment or access grant. `job_links_json` separately lists only
explicitly linked Jobs as objects with `id`, `title` and `archived`, ordered by
Job ID. The legacy single Contact link and many-Contact links are deduplicated;
Location uses the Job's explicit Location link. No Customer expansion or
recursive work is included. The list is subject to the same 1 MiB cell limit
as retained Custom Fields: overflow fails generation, never truncates links.

`job_links_visibility` is `restricted` without A `job.view`, with `[]` and no
stored Job authority dependencies regardless of whether hidden links exist.
With A Job View it is `active_only`, unless both the export's explicit archived
option and A `job.archived.view` permit archived Jobs, in which case it is
`visible`. These markers describe selection authority, never hidden-row counts.
Each disclosed Job is stored as a revocation reference and rechecked; empty
collections create no Job revocation dependency. IDs and titles are suppressed
together. Primary defaults alone always leave the Job list empty.

Neither table currently has a Custom Fields column in the SQL migrations;
the adapters do not invent one or copy Customer fields. Coordinate nulls are
empty; zero coordinates remain zero. Legacy notes and Service/Access Notes are
separate columns.

### Project

```text
project_id,name,description,status_id,status_label,status_category,displayed_progress_percent,retained_manual_progress_percent,planned_start_date,planned_end_date,service_notes,notes,legacy_review_reasons_json,legacy_captured_timezone,custom_fields_json,customer_id,customer_label,customer_visibility,location_id,location_label,location_visibility,archived
```

Status ID/label and semantic category are distinct. Only category
`project:completed` presents `displayed_progress_percent=100`; the retained
manual value remains the database's numeric value (including legacy fractional
values). Other categories present that retained value. Neither is computed
from Jobs. Dates are ISO calendar dates, not timezone-converted timestamps.
Missing Status/date values are empty; no status or historical progress is
inferred. No Jobs, documents or related work are recursively expanded.

`legacy_review_reasons_json` preserves the recorded `project_legacy_review`
reason list, including unknown prior progress on legacy Completed Projects;
`legacy_captured_timezone` preserves the recorded date-conversion context.
The review's full historical row is not exported, preventing hidden relationship
IDs from leaking through legacy evidence. Empty reasons are `[]`.

`custom_fields_json` is an object with `values` (the retained JSON verbatim in
meaning) and `definitions` (referenced Project definitions ordered by ID,
including owned and retained-global definitions). Definition metadata includes
ID, name, field type, precision, retired flag, options and retired options.
Retired values/options remain present. Missing definitions do not erase retained
values or cause fabricated metadata. Legacy non-array values remain intact in
`values`, with no invented definition mapping. Every cell is limited to 1 MiB UTF-8 bytes;
overflow fails the entire generation rather than truncating JSON. Fixed headers
do not expand when definitions change.

## Snapshot and checks

Selection uses a PostgreSQL cursor, fetching 128 rows at a time. Each batch is
closed before emitting rows so the durable writer can use the same transaction.
The shared lifecycle supplies the repeatable snapshot, authority fence, atomic
publication/audit, CSV formula escaping, all-row recheck and 24-hour availability.

Focused tests in `internal/exports/master_records_test.go` pin fixed contracts,
strict/resource-specific options, pre-query denial and the retained-cell size
boundary. `internal/handlers/master_records_exports_integration_test.go` uses
the migrated PostgreSQL permission fixture and actual durable export service to
exercise duplicate identities, primary defaults versus deduplicated explicit
Job links, restricted IDs/labels, disclosure-only references and revocation,
explicit archived Job selection and archive-grant loss, selected Customer
authorization even with empty results, Project Status selection/type rejection,
Completed/restored manual progress, independent Customer/Location disclosure
revocation, SQL-only dates, legacy uncertainty and non-array retained JSON,
retired Custom Fields and atomic failure on oversized JSON. A 260-row test for
each adapter crosses two 128-row cursor boundaries and checks completeness,
duplicate identities and descending name/ID ordering.

`TestMasterRecordOwnArchiveAndCollectionSearchPostgres` additionally covers own
Contact/Location/Project archive exclusion and explicit inclusion, post-Ready
whole-artifact denial, restore retaining historical archive authority, and
collection search matching. This test was added after the initial focused
PostgreSQL run; its database execution is reserved for the coordinator's final
reconciled run following the PostgreSQL lock-capacity restart. Compilation,
focused unit tests and vet pass with this addition.

Executed successfully against the coordinator's disposable PostgreSQL:

```sh
source /tmp/opencode/issue97-evidence-20261122/env.sh
go test ./internal/handlers -run '^TestMasterRecord.*Postgres$' -count=1 -v
```

HTTP/browser and shared lifecycle concurrency coverage remain coordinated in
the ticket's separate master export tests. These focused database tests do not
claim browser coverage or whole-ticket acceptance. Full ticket acceptance also
requires `make lint` and `FREEFSM_TEST_DATABASE_URL=<disposable DB> make test`.
