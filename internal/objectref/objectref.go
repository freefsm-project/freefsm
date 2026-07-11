package objectref

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

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

type Capability uint8

const (
	CapActivity Capability = 1 << iota
	CapFiles
	CapArchive
	CapTags
)

type ExistenceMode uint8

const (
	ExistsAny ExistenceMode = iota
	ExistsActive
)

type Ref struct {
	Type Type
	ID   int64
}

func New(t Type, id int64) Ref {
	return Ref{Type: t, ID: id}
}

func (r Ref) ObjectType() string {
	return string(r.Type)
}

func (r Ref) ObjectID() int64 {
	return r.ID
}

func (r Ref) Valid() bool {
	return Known(r.Type) && r.ID > 0
}

type Descriptor struct {
	Type         Type
	SingularName string
	AdminOnly    bool
	Capabilities Capability
}

type Directory interface {
	Parse(objectType string, objectID int64) (Ref, error)
	Describe(t Type) (Descriptor, bool)
	Supports(t Type, cap Capability) bool
	Exists(ctx context.Context, ref Ref, mode ExistenceMode) (bool, error)
	DisplayName(ctx context.Context, ref Ref) (string, error)
	URL(ref Ref) (string, bool)
}

var (
	ErrUnknownType       = errors.New("unknown object type")
	ErrInvalidID         = errors.New("object id must be positive")
	ErrUnsupportedMode   = errors.New("unsupported existence mode")
	ErrActiveUnsupported = errors.New("active existence requires archive capability")
)

var knownTypes = map[Type]struct{}{
	TypeCustomer:        {},
	TypeJob:             {},
	TypeProject:         {},
	TypeEstimate:        {},
	TypeInvoice:         {},
	TypeAsset:           {},
	TypeItem:            {},
	TypeTimeEntry:       {},
	TypeAssetType:       {},
	TypeAssetStatus:     {},
	TypeCompanySettings: {},
	TypeCustomField:     {},
	TypeJobStatus:       {},
	TypeTag:             {},
	TypeUser:            {},
}

var descriptors = map[Type]Descriptor{
	TypeCustomer:        {Type: TypeCustomer, SingularName: "customer", Capabilities: CapActivity | CapFiles | CapArchive | CapTags},
	TypeJob:             {Type: TypeJob, SingularName: "job", Capabilities: CapActivity | CapFiles | CapArchive | CapTags},
	TypeProject:         {Type: TypeProject, SingularName: "project", Capabilities: CapActivity | CapFiles | CapArchive | CapTags},
	TypeEstimate:        {Type: TypeEstimate, SingularName: "estimate", Capabilities: CapActivity | CapFiles | CapArchive | CapTags},
	TypeInvoice:         {Type: TypeInvoice, SingularName: "invoice", Capabilities: CapActivity | CapFiles | CapArchive | CapTags},
	TypeAsset:           {Type: TypeAsset, SingularName: "asset", Capabilities: CapActivity | CapFiles | CapArchive | CapTags},
	TypeItem:            {Type: TypeItem, SingularName: "item", Capabilities: CapActivity | CapArchive},
	TypeTimeEntry:       {Type: TypeTimeEntry, SingularName: "time entry", Capabilities: CapActivity},
	TypeAssetType:       {Type: TypeAssetType, SingularName: "asset type", AdminOnly: true, Capabilities: CapActivity},
	TypeAssetStatus:     {Type: TypeAssetStatus, SingularName: "asset status", AdminOnly: true, Capabilities: CapActivity},
	TypeCompanySettings: {Type: TypeCompanySettings, SingularName: "company settings", AdminOnly: true, Capabilities: CapActivity},
	TypeCustomField:     {Type: TypeCustomField, SingularName: "custom field", AdminOnly: true, Capabilities: CapActivity},
	TypeJobStatus:       {Type: TypeJobStatus, SingularName: "job status", AdminOnly: true, Capabilities: CapActivity},
	TypeTag:             {Type: TypeTag, SingularName: "tag", AdminOnly: true, Capabilities: CapActivity},
	TypeUser:            {Type: TypeUser, SingularName: "user", AdminOnly: true, Capabilities: CapActivity},
}

func Parse(typ string, id int64) (Ref, error) {
	t := Type(typ)
	if !Known(t) {
		return Ref{}, fmt.Errorf("%w: %s", ErrUnknownType, typ)
	}
	if id <= 0 {
		return Ref{}, fmt.Errorf("%w: %d", ErrInvalidID, id)
	}
	return Ref{Type: t, ID: id}, nil
}

func Describe(t Type) (Descriptor, bool) {
	d, ok := descriptors[t]
	return d, ok
}

func AdminOnlyTypes() []Type {
	types := make([]Type, 0)
	for _, typ := range AllTypes() {
		if typ.AdminOnly() {
			types = append(types, typ)
		}
	}
	return types
}

func AllTypes() []Type {
	return []Type{
		TypeCustomer,
		TypeJob,
		TypeProject,
		TypeEstimate,
		TypeInvoice,
		TypeAsset,
		TypeItem,
		TypeTimeEntry,
		TypeAssetType,
		TypeAssetStatus,
		TypeCompanySettings,
		TypeCustomField,
		TypeJobStatus,
		TypeTag,
		TypeUser,
	}
}

func Known(t Type) bool {
	_, ok := knownTypes[t]
	return ok
}

func (t Type) Has(cap Capability) bool {
	return t.Capabilities()&cap != 0
}

func (t Type) Capabilities() Capability {
	d, ok := Describe(t)
	if !ok {
		return 0
	}
	return d.Capabilities
}

func (t Type) AdminOnly() bool {
	d, ok := Describe(t)
	return ok && d.AdminOnly
}

func URL(ref Ref) (string, bool) {
	if !Known(ref.Type) || ref.ID <= 0 {
		return "", false
	}
	id := strconv.FormatInt(ref.ID, 10)
	switch ref.Type {
	case TypeCustomer:
		return "/customers/" + id, true
	case TypeJob:
		return "/jobs/" + id, true
	case TypeProject:
		return "/projects/" + id, true
	case TypeEstimate:
		return "/estimates/" + id, true
	case TypeInvoice:
		return "/invoices/" + id, true
	case TypeAsset:
		return "/assets/" + id, true
	case TypeItem:
		return "/items/" + id, true
	case TypeTimeEntry:
		return "/time-entries/" + id, true
	case TypeAssetType, TypeAssetStatus:
		return "/settings/assets", true
	case TypeCompanySettings:
		return "/settings", true
	case TypeCustomField:
		return "/settings/custom-fields", true
	case TypeJobStatus:
		return "/settings/job-statuses", true
	case TypeTag:
		return "/tags/" + id, true
	case TypeUser:
		return "/users/" + id, true
	default:
		return "", false
	}
}

func validateExists(ref Ref, mode ExistenceMode) error {
	if !Known(ref.Type) {
		return fmt.Errorf("%w: %s", ErrUnknownType, ref.Type)
	}
	if ref.ID <= 0 {
		return fmt.Errorf("%w: %d", ErrInvalidID, ref.ID)
	}
	if mode != ExistsAny && mode != ExistsActive {
		return fmt.Errorf("%w: %d", ErrUnsupportedMode, mode)
	}
	if mode == ExistsActive && !ref.Type.Has(CapArchive) {
		return fmt.Errorf("%w: %s", ErrActiveUnsupported, ref.Type)
	}
	return nil
}

func fallbackName(ref Ref) string {
	if d, ok := Describe(ref.Type); ok {
		if ref.Type == TypeCompanySettings {
			return "Company Settings"
		}
		return fmt.Sprintf("%s #%d", d.SingularName, ref.ID)
	}

	switch ref.Type {
	case TypeTimeEntry:
		return fmt.Sprintf("time entry #%d", ref.ID)
	case TypeAssetType:
		return fmt.Sprintf("asset type #%d", ref.ID)
	case TypeAssetStatus:
		return fmt.Sprintf("asset status #%d", ref.ID)
	case TypeJobStatus:
		return fmt.Sprintf("job status #%d", ref.ID)
	case TypeCompanySettings:
		return "Company Settings"
	case TypeCustomField:
		return fmt.Sprintf("custom field #%d", ref.ID)
	default:
		return fmt.Sprintf("%s #%d", ref.Type, ref.ID)
	}
}
