package exports

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Summary contributors remain private durable references, never CSV enrichment.
type financialExportReference struct {
	Version    int                                `json:"version"`
	Kind       authorization.FinancialReportKind  `json:"kind"`
	Summary    bool                               `json:"summary"`
	References []authorization.FinancialReference `json:"references"`
	Provenance []financialProvenance              `json:"provenance,omitempty"`
}

func financialReferenceBytes(kind authorization.FinancialReportKind, summary bool, refs []authorization.FinancialReference, provenance ...financialProvenance) (json.RawMessage, error) {
	if refs == nil {
		refs = []authorization.FinancialReference{}
	}
	ref := financialExportReference{Version: 1, Kind: kind, Summary: summary, References: refs, Provenance: provenance}
	if err := validateFinancialReference(ref, kind); err != nil {
		return nil, err
	}
	return json.Marshal(ref)
}

func validateFinancialReference(ref financialExportReference, kind authorization.FinancialReportKind) error {
	if ref.Version != 1 || ref.Kind != kind || ref.References == nil || !ref.Summary && len(ref.References) == 0 {
		return authorization.ErrForbidden
	}
	if !ref.Summary && (kind == authorization.FinancialSettlement || kind == authorization.FinancialCredit || kind == authorization.FinancialCombined) && len(ref.Provenance) != 1 {
		return authorization.ErrForbidden
	}
	for _, source := range ref.References {
		if source.CustomerID <= 0 {
			return authorization.ErrForbidden
		}
		facet := source.Facet
		if kind == authorization.FinancialCombined {
			if facet != authorization.FinancialSettlement && facet != authorization.FinancialCredit {
				return authorization.ErrForbidden
			}
		} else if facet != kind {
			return authorization.ErrForbidden
		}
		switch facet {
		case authorization.FinancialAging, authorization.FinancialCustomers:
			if source.EventKind != "" || source.EventID != "" || source.CreditID != "" {
				return authorization.ErrForbidden
			}
			if facet == authorization.FinancialAging && len(source.Invoices) == 0 {
				return authorization.ErrForbidden
			}
			for _, invoice := range source.Invoices {
				if invoice.ID <= 0 || invoice.JobID < 0 {
					return authorization.ErrForbidden
				}
			}
		case authorization.FinancialSettlement:
			if len(source.Invoices) != 0 || source.CreditID != "" || !financialUUID(source.EventID) {
				return authorization.ErrForbidden
			}
			if _, ok := financialEventType(source.EventKind); !ok {
				return authorization.ErrForbidden
			}
		case authorization.FinancialCredit:
			if len(source.Invoices) != 0 || source.EventKind != "" || source.EventID != "" || !financialUUID(source.CreditID) {
				return authorization.ErrForbidden
			}
		default:
			return authorization.ErrForbidden
		}
	}
	for _, source := range ref.Provenance {
		if err := source.validate(kind); err != nil {
			return err
		}
	}
	return nil
}

func financialUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func recheckFinancialExport(ctx context.Context, tx pgx.Tx, p authorization.Principal, kind authorization.FinancialReportKind, raw json.RawMessage) error {
	if err := financialExportGrants(p, kind); err != nil {
		return err
	}
	var ref financialExportReference
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&ref); err != nil {
		return authorization.ErrForbidden
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return authorization.ErrForbidden
	}
	if err := validateFinancialReference(ref, kind); err != nil {
		return err
	}
	for _, source := range ref.References {
		if err := authorization.CheckFinancialReference(ctx, tx, p, source); err != nil {
			return err
		}
	}
	for _, source := range ref.Provenance {
		if err := source.recheck(ctx, tx, p); err != nil {
			return err
		}
	}
	return nil
}
