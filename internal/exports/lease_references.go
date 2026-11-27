package exports

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

func decodeLeaseExportReference(raw json.RawMessage) (authorization.LeaseExportReference, error) {
	var ref authorization.LeaseExportReference
	root, err := leaseReferenceObject(raw, "version", "mode", "summary", "filter", "selection", "complete_generation", "leases")
	if err != nil {
		return ref, err
	}
	if _, err := leaseReferenceObject(root["filter"], "as_of", "search", "lifecycle", "customer", "asset", "premises", "ending_from", "ending_through", "archive", "placement", "billing", "sort"); err != nil {
		return ref, err
	}
	selection, err := leaseReferenceObject(root["selection"], "customers", "premises", "assets")
	if err != nil {
		return ref, err
	}
	for _, key := range []string{"customers", "premises", "assets"} {
		var selected []json.RawMessage
		if json.Unmarshal(selection[key], &selected) != nil {
			return ref, authorization.ErrForbidden
		}
		for _, related := range selected {
			if _, err := leaseReferenceObject(related, "id", "archived"); err != nil {
				return ref, err
			}
		}
	}
	var leases []json.RawMessage
	if json.Unmarshal(root["leases"], &leases) != nil {
		return ref, authorization.ErrForbidden
	}
	for _, lease := range leases {
		fields, err := leaseReferenceObject(lease, "id", "archived", "customers", "premises", "assets", "invoices", "actors")
		if err != nil {
			return ref, err
		}
		for _, key := range []string{"customers", "premises", "assets", "invoices"} {
			var children []json.RawMessage
			if json.Unmarshal(fields[key], &children) != nil {
				return ref, authorization.ErrForbidden
			}
			for _, child := range children {
				keys := []string{"id", "archived"}
				if key == "invoices" {
					keys = []string{"id", "job_id", "archived", "settlement"}
				}
				if _, err := leaseReferenceObject(child, keys...); err != nil {
					return ref, err
				}
			}
		}
		var actors []json.RawMessage
		if json.Unmarshal(fields["actors"], &actors) != nil {
			return ref, authorization.ErrForbidden
		}
		for _, actor := range actors {
			if bytes.Equal(bytes.TrimSpace(actor), []byte("null")) {
				return ref, authorization.ErrForbidden
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&ref) != nil || d.Decode(new(any)) != io.EOF {
		return ref, authorization.ErrForbidden
	}
	return ref, nil
}

// Required shape is checked before Go zero values can erase missing or null
// archive/settlement/completeness anchors. Exact keys also reject case aliases
// and duplicates that encoding/json would otherwise accept.
func leaseReferenceObject(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, authorization.ErrForbidden
	}
	fields := make(map[string]json.RawMessage, len(keys))
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, authorization.ErrForbidden
		}
		key, ok := token.(string)
		if !ok {
			return nil, authorization.ErrForbidden
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, authorization.ErrForbidden
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, authorization.ErrForbidden
		}
		fields[key] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, authorization.ErrForbidden
	}
	if d.Decode(new(any)) != io.EOF || len(fields) != len(keys) {
		return nil, authorization.ErrForbidden
	}
	for _, key := range keys {
		if _, exists := fields[key]; !exists {
			return nil, authorization.ErrForbidden
		}
	}
	return fields, nil
}

func leaseExportReferenceBytes(ref authorization.LeaseExportReference, mode authorization.LeaseExportMode) (json.RawMessage, error) {
	if ref.Mode != mode || ref.Version != 1 || ref.Leases == nil || !ref.Summary && len(ref.Leases) != 1 {
		return nil, authorization.ErrForbidden
	}
	raw, err := json.Marshal(ref)
	if err != nil {
		return nil, err
	}
	if _, err := decodeLeaseExportReference(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// The domain verifies the original AND current sensitive anchors, including
// summary contributors and empty-result selection prerequisites. Never salvage
// a subset or recompute values while preparing an immutable artifact.
func (d Leases) Recheck(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	if d.Kind() == "" {
		return authorization.ErrForbidden
	}
	ref, err := decodeLeaseExportReference(raw)
	if err != nil {
		return err
	}
	return authorization.CheckLeaseExportReference(ctx, tx, p, d.Mode, ref)
}
