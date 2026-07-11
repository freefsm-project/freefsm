## Problem

The codebase has an implicit generic-object concept represented by raw `object_type` strings and `object_id` integers. The same object vocabulary appears across activity logs, files, tags, comments, authorization, archive guards, and delete dependency checks, but each caller partially redefines what those strings mean.

- `ActivityService.LookupEntityName` owns a large display-name switch for persisted object types.
- File attachment validation separately knows which object types support files and whether targets exist.
- `archive_guard.go` repeats active/archived existence checks for soft-deletable objects.
- Policy, dependency, tags, and comments also accept raw `(objectType string, objectID int64)` pairs, creating more future migration pressure.

The architectural friction is that adding or changing an object type requires auditing many shallow switches and call sites. Tests also have to exercise object interpretation indirectly through higher-level services instead of testing one boundary for object identity, capabilities, existence, display names, and URLs.

This RFC intentionally scopes the first migration small: introduce the object reference boundary and migrate activity, files, and archive guards first. Policy, dependency checks, tags, and comments should remain follow-up migrations after the boundary proves useful.

## Settled Decisions

- The first migration is limited to Activity / Audit Log display, File Attachment target validation, and Soft-Delete / Archive active-object guards.
- The module lives at `internal/objectref`.
- The module owns persisted object type spelling, object reference validation, capability checks, admin-only classification, pure URL construction, display-name lookup, and existence lookup.
- The module does not own full CRUD, authorization decisions, dependency protection, archive/restore mutation commands, tag/comment/custom-field data operations, or entity-specific business rules.
- Parsing is strict: unknown object types and non-positive IDs are errors.
- The type list includes the full current object vocabulary from day one: `customer`, `job`, `project`, `estimate`, `invoice`, `asset`, `item`, `time_entry`, `asset_type`, `asset_status`, `company_settings`, `custom_field`, `job_status`, `tag`, and `user`.
- `CapFiles` preserves current File Attachment behavior: `customer`, `job`, `project`, `estimate`, `invoice`, and `asset` only.
- `CapArchive` preserves current active-object guard behavior: `customer`, `job`, `project`, `estimate`, `invoice`, `asset`, and `item` only.
- `ExistsActive` returns an error for object types that do not support `CapArchive`.
- URL construction is pure and does not check whether the target row exists.
- Display-name lookup preserves current fallback behavior for missing historical rows.
- `AdminOnly` is object metadata and is used only for Activity / Audit Log filtering in this migration.
- `FileService` consumes `objectref.Directory` only for parse, file capability, and active-target existence. It keeps MIME, size, path, disk, Ent file row, cleanup, rename, delete, and size-formatting behavior.
- `requireActiveObject` remains an HTTP adapter in `internal/handlers`, but consumes `objectref.Directory` instead of `*ent.Client`.
- `FakeDirectory` is data-driven with explicit descriptor, name, active, any-state, URL, and error maps.
- Adapter errors are distinct from clean missing or inactive results. Archive middleware should return `500` for adapter errors and `403` for a clean inactive/missing result.
- Migration order: add `internal/objectref`, add objectref tests, migrate files, migrate archive guards, migrate Activity / Audit Log display and filtering, then collapse duplicated tests only where the new seam covers the behavior.

## Proposed Interface

Introduce a small package such as `internal/objectref` that preserves existing persisted `object_type` strings while making object references explicit.

```go
package objectref

import "context"

type Type string

const (
	TypeCustomer        Type = "customer"
	TypeJob             Type = "job"
	TypeProject         Type = "project"
	TypeEstimate        Type = "estimate"
	TypeInvoice         Type = "invoice"
	TypeAsset           Type = "asset"
	TypeItem            Type = "item"
	TypeTimeEntry       Type = "time_entry"
	TypeAssetType       Type = "asset_type"
	TypeAssetStatus     Type = "asset_status"
	TypeCompanySettings Type = "company_settings"
	TypeCustomField     Type = "custom_field"
	TypeJobStatus       Type = "job_status"
	TypeTag             Type = "tag"
	TypeUser            Type = "user"
)

type Ref struct {
	Type Type
	ID   int64
}

func New(t Type, id int64) Ref { return Ref{Type: t, ID: id} }
func (r Ref) ObjectType() string { return string(r.Type) }
func (r Ref) ObjectID() int64 { return r.ID }
func (r Ref) Valid() bool { return r.Type != "" && r.ID > 0 }

type Capability string

const (
	CapActivity Capability = "activity"
	CapFiles    Capability = "files"
	CapTags     Capability = "tags"
	CapComments Capability = "comments"
	CapArchive  Capability = "archive"
)

type ExistenceMode int

const (
	ExistsAny ExistenceMode = iota
	ExistsActive
)

type Descriptor struct {
	Type         Type
	SingularName string
	AdminOnly    bool
	Capabilities map[Capability]bool
}

type Directory interface {
	Parse(objectType string, objectID int64) (Ref, error)
	Describe(t Type) (Descriptor, bool)
	Supports(t Type, cap Capability) bool
	Exists(ctx context.Context, ref Ref, mode ExistenceMode) (bool, error)
	DisplayName(ctx context.Context, ref Ref) (string, error)
	URL(ref Ref) (string, bool)
}
```

Production should provide an Ent-backed implementation:

```go
type EntDirectory struct {
	client *ent.Client
}

func NewEntDirectory(client *ent.Client) *EntDirectory {
	return &EntDirectory{client: client}
}
```

Tests should provide a fake implementation for callers that only need object semantics:

```go
type FakeDirectory struct {
	Descriptors map[Type]Descriptor
	Names       map[Ref]string
	Active      map[Ref]bool
	Any         map[Ref]bool
	URLs        map[Ref]string
}
```

Example caller migration:

```go
ref, err := objects.Parse(objectType, objectID)
if err != nil || !objects.Supports(ref.Type, objectref.CapFiles) {
	return nil, fmt.Errorf("invalid attachment target")
}

exists, err := objects.Exists(ctx, ref, objectref.ExistsActive)
if err != nil || !exists {
	return nil, fmt.Errorf("target %s %d not found", ref.Type, ref.ID)
}
```

Archive guard migration:

```go
func requireActiveObject(objects objectref.Directory, objectType objectref.Type) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
			ref := objectref.New(objectType, id)

			if err != nil || !ref.Valid() {
				http.NotFound(w, r)
				return
			}

			ok, err := objects.Exists(r.Context(), ref, objectref.ExistsActive)
			if err != nil || !ok {
				http.Error(w, "archived records are read-only", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
```

The interface hides object-type spelling, valid capabilities, display-name lookup, route URL construction, active-vs-any existence checks, and admin-only classification. It does not hide full CRUD, authorization policy, or delete dependency behavior.

## Dependency Strategy

- **In-process**: `Ref`, `Type`, `Capability`, `Descriptor`, capability tables, fallback formatting, URL rules, and admin-only classification. These should be tested without a database.
- **Local-substitutable**: Ent-backed object existence and display-name lookup. Production uses `EntDirectory`; adapter tests use the project’s existing local database test setup.
- **Ports & adapters**: The `Directory` interface is the port. `EntDirectory` is the production adapter and `FakeDirectory` is the test adapter. This keeps callers from depending directly on Ent-specific object switches.
- **Mock**: Not needed for this first migration because there is no true external dependency in this seam.

## Testing Strategy

- **New boundary tests to write**:
  - `objectref.Ref` preserves persisted object strings and rejects invalid refs.
  - `EntDirectory.Supports` returns the expected capabilities for activity, files, and archive guards.
  - `EntDirectory.Exists` distinguishes `ExistsAny` from `ExistsActive` for soft-deletable objects.
  - `EntDirectory.DisplayName` matches current activity display-name behavior for core object types.
  - `FakeDirectory` supports caller tests without Ent.
  - File target validation rejects unsupported object types and archived targets through `Directory`.
  - Archive guard rejects archived targets through `Directory`.
- **Old tests to delete**:
  - Delete or collapse tests that only re-check duplicated valid-object-type or active-target switch behavior once the `Directory` boundary tests cover it.
  - Keep higher-level file/activity/archive tests that assert HTTP or persisted behavior.
- **Test environment needs**:
  - Fast unit tests for in-process `objectref` behavior.
  - Existing Ent/Postgres integration setup for `EntDirectory` adapter tests.
  - Fake directory for handler/service tests that should not need database setup.

## Implementation Recommendations

- The module should own object identity and object metadata, not domain CRUD.
- The module should hide persisted string spelling, capability support, display names, URLs, active/archive existence checks, and admin-only object classification.
- The module should expose `Ref`, typed constants, a small `Directory` interface, `EntDirectory`, and `FakeDirectory`.
- The first migration should update activity display names/URLs, file target validation, and archive guards only.
- Policy, dependency checks, tags, and comments should be follow-up migrations that accept `objectref.Ref` once the first migration is stable.
- Avoid adding `CanAccess`, `CanDelete`, or broad repository methods to `Directory` in the first pass. Those would make the registry too deep too early and risk creating a god module.
- Preserve existing database values exactly; no data migration should be required.
