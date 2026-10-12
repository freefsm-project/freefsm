package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
)

func historicalFieldAccess(p Principal, manage bool) bool {
	action := "configuration.fields.view"
	if manage {
		action = "configuration.fields.manage"
	}
	return p.Allows(action, Configuration) && p.Allows("estimate.view", All) && p.Allows("invoice.view", All) && p.Allows("estimate.provenance.view", All) && p.Allows("invoice.provenance.view", All)
}

func (s *Service) HistoricalFieldSnapshot(ctx context.Context, a Actor, cycle string) (string, error) {
	if _, err := uuid.Parse(cycle); err != nil {
		return "", ErrNotFound
	}
	p, err := s.Principal(ctx, a)
	if err != nil {
		return "", err
	}
	if !historicalFieldAccess(p, false) {
		return "", ErrForbidden
	}
	var snapshot string
	err = s.Pool.QueryRow(ctx, `SELECT source_snapshot::text FROM estimate_invoice_conversion_cycles WHERE id=$1 AND company_id=$2`, cycle, p.CompanyID).Scan(&snapshot)
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	return snapshot, err
}

// ReviewHistoricalFields records an attributable retrospective decision without
// altering the original cycle. Pair identities are supplied explicitly; labels
// and the current configuration are never used to guess historical pairing.
func (s *Service) ReviewHistoricalFields(ctx context.Context, a Actor, cycle, reason string, evidence customfields.CycleEvidence) error {
	if _, err := uuid.Parse(cycle); err != nil {
		return ErrNotFound
	}
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !historicalFieldAccess(p, true) {
			return ErrForbidden
		}
		if strings.TrimSpace(reason) == "" || evidence.Pairs == nil || evidence.Definitions == nil {
			return &customfields.FieldError{Name: "Historical review", Message: "explicit pairing, definitions and a review reason are required"}
		}
		var snapshot []byte
		var invoice int64
		var known bool
		err := tx.QueryRow(ctx, `SELECT source_snapshot,invoice_id,conversion_field_snapshot(id)->'field_pairs' IS NOT NULL FROM estimate_invoice_conversion_cycles WHERE id=$1 AND company_id=$2 FOR UPDATE`, cycle, p.CompanyID).Scan(&snapshot, &invoice, &known)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if known {
			return ErrConflict
		}
		defs := map[int64]customfields.Definition{}
		for _, d := range evidence.Definitions {
			if d.ID <= 0 || defs[d.ID].ID != 0 || !customfields.SupportsType(d.Type) {
				return &customfields.FieldError{Name: "Historical review", Message: "unique valid definition identities and types are required"}
			}
			if d.Type == "number" && (d.Precision == nil || *d.Precision < 0 || *d.Precision > 18) {
				return &customfields.FieldError{ID: d.ID, Name: d.Name, Message: "explicit historical precision is required"}
			}
			if _, err := customfields.Options(d.Options); err != nil {
				return err
			}
			defs[d.ID] = customfields.Definition{ID: d.ID, Name: d.Name, Type: d.Type, Precision: d.Precision, Options: d.Options}
		}
		targets := map[int64]bool{}
		for source, target := range evidence.Pairs {
			e, eok := defs[source]
			i, iok := defs[target]
			if !eok || !iok || targets[target] || e.Type != i.Type {
				return &customfields.FieldError{Name: "Historical review", Message: "pair exactly one compatible Estimate and Invoice definition"}
			}
			targets[target] = true
			ep, _ := json.Marshal(e.Precision)
			ip, _ := json.Marshal(i.Precision)
			var eo, io []string
			json.Unmarshal([]byte(e.Options), &eo)
			json.Unmarshal([]byte(i.Options), &io)
			eopts, _ := json.Marshal(eo)
			iopts, _ := json.Marshal(io)
			if string(ep) != string(ip) || string(eopts) != string(iopts) {
				return &customfields.FieldError{Name: "Historical review", Message: "paired precision and ordered option texts must be identical"}
			}
			var valid bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM custom_field_definitions e,custom_field_definitions i WHERE e.id=$1 AND i.id=$2 AND e.object_type='estimate' AND i.object_type='invoice' AND e.company_id=$3 AND i.company_id=$3)`, source, target, p.CompanyID).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return ErrNotFound
			}
		}
		if len(defs) != 2*len(evidence.Pairs) {
			return &customfields.FieldError{Name: "Historical review", Message: "supply exactly the paired definitions; unpaired source values remain original"}
		}
		var source struct {
			Values []customfields.Value `json:"custom_fields"`
		}
		if err = json.Unmarshal(snapshot, &source); err != nil {
			return err
		}
		for _, v := range source.Values {
			if _, paired := evidence.Pairs[v.DefinitionID]; paired {
				if err = customfields.ValidateValue(defs[v.DefinitionID], v.Value); err != nil {
					return err
				}
			}
		}
		encoded, err := json.Marshal(evidence)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO conversion_field_reviews(cycle_id,evidence,reason,reviewed_by) VALUES($1,$2,$3,$4)`, cycle, encoded, strings.TrimSpace(reason), p.UserID); err != nil {
			return err
		}
		var current string
		if err = tx.QueryRow(ctx, `SELECT custom_fields::text FROM invoices WHERE id=$1 FOR SHARE`, invoice).Scan(&current); err != nil {
			return err
		}
		if err = customfields.CheckCycleValues(ctx, tx, "invoice", invoice, current); err != nil {
			return fmt.Errorf("review cannot represent current Invoice: %w", err)
		}
		return audit(ctx, tx, a, p, "configuration.fields.manage", Configuration, "invoice", invoice, map[string]any{"cycle_id": cycle, "review_reason": reason, "historical_field_evidence": evidence})
	})
}
