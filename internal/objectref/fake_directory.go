package objectref

import (
	"context"
	"fmt"
)

type FakeDirectory struct {
	Descriptors map[Type]Descriptor
	Names       map[Ref]string
	Active      map[Ref]bool
	Any         map[Ref]bool
	URLs        map[Ref]string
	Errors      map[Ref]error
}

func (d *FakeDirectory) Parse(objectType string, objectID int64) (Ref, error) {
	return Parse(objectType, objectID)
}

func (d *FakeDirectory) Describe(t Type) (Descriptor, bool) {
	if d != nil && d.Descriptors != nil {
		if desc, ok := d.Descriptors[t]; ok {
			return desc, true
		}
	}
	return Describe(t)
}

func (d *FakeDirectory) Supports(t Type, cap Capability) bool {
	desc, ok := d.Describe(t)
	return ok && desc.Capabilities&cap != 0
}

func (d *FakeDirectory) Exists(ctx context.Context, ref Ref, mode ExistenceMode) (bool, error) {
	_ = ctx
	if err := validateExists(ref, mode); err != nil {
		return false, err
	}
	if err := d.err(ref); err != nil {
		return false, err
	}
	if mode == ExistsActive {
		if d == nil || d.Active == nil {
			return false, nil
		}
		return d.Active[ref], nil
	}
	if d == nil || d.Any == nil {
		return false, nil
	}
	return d.Any[ref], nil
}

func (d *FakeDirectory) DisplayName(ctx context.Context, ref Ref) (string, error) {
	_ = ctx
	if !Known(ref.Type) {
		return "", fmt.Errorf("%w: %s", ErrUnknownType, ref.Type)
	}
	if ref.ID <= 0 {
		return "", fmt.Errorf("%w: %d", ErrInvalidID, ref.ID)
	}
	if err := d.err(ref); err != nil {
		return "", err
	}
	if d != nil && d.Names != nil {
		if name, ok := d.Names[ref]; ok {
			return name, nil
		}
	}
	return fallbackName(ref), nil
}

func (d *FakeDirectory) URL(ref Ref) (string, bool) {
	if d != nil && d.URLs != nil {
		if u, ok := d.URLs[ref]; ok {
			return u, true
		}
	}
	return URL(ref)
}

func (d *FakeDirectory) err(ref Ref) error {
	if d == nil || d.Errors == nil {
		return nil
	}
	return d.Errors[ref]
}
