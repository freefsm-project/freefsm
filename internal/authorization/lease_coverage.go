package authorization

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type LeaseCoverageInput struct {
	LeaseCoverageEvent
	Revision         int64  `json:"revision"`
	ExpectedRevision int64  `json:"expected_revision"`
	Action           string `json:"action"`
	Reason           string `json:"reason"`
	OperationKey     string `json:"operation_key"`
	RentCents        *int64 `json:"rent_cents"`
	ChargeCents      *int64 `json:"charge_cents"`
	TaxRate          string `json:"tax_rate"`
}

type LeaseCoverageRecord struct {
	LeaseCoverageInput
	RecordedAt string
}

type LeaseCoverageReview struct {
	Base      DraftLeaseBase
	Revision  int64
	Today     string
	CanManage bool
	Events    []LeaseCoverageRecord
	History   []LeaseCoverageRecord
}

func readCoverageEvents(ctx context.Context, tx pgx.Tx, lease int64, history bool) ([]LeaseCoverageRecord, error) {
	query := `SELECT c.payload,c.id,c.revision,c.action,c.reason,c.recorded_at::text FROM lease_coverage_current c WHERE c.lease_id=$1 ORDER BY c.id`
	if history {
		query = `SELECT r.payload,e.id,r.id,r.action,r.reason,r.recorded_at::text FROM lease_coverage_events e JOIN lease_coverage_revisions r ON r.event_id=e.id WHERE e.lease_id=$1 ORDER BY r.id`
	}
	rows, err := tx.Query(ctx, query, lease)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LeaseCoverageRecord
	for rows.Next() {
		var row LeaseCoverageRecord
		var raw []byte
		var id, revision int64
		var action, reason, at string
		if err := rows.Scan(&raw, &id, &revision, &action, &reason, &at); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &row.LeaseCoverageInput); err != nil {
			return nil, err
		}
		row.ID, row.Revision, row.Action, row.Reason, row.RecordedAt = id, revision, action, reason, at
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Service) ReviewLeaseCoverage(ctx context.Context, a Actor, id int64) (LeaseCoverageReview, error) {
	var out LeaseCoverageReview
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.view", All) {
			return ErrNotFound
		}
		var err error
		out.Base, err = readLeaseBase(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if out.Base.Lifecycle == "draft" {
			return ErrConflict
		}
		out.CanManage = !out.Base.Archived && p.Allows("lease.manage", All) && p.Allows("lease.billing.manage", All) && p.Allows("lease.billing.view", All)
		if err = tx.QueryRow(ctx, `SELECT lease_dependency_today($1)::text,coalesce((SELECT max(r.id) FROM lease_coverage_revisions r JOIN lease_coverage_events e ON e.id=r.event_id WHERE e.lease_id=$2),0)`, p.CompanyID, id).Scan(&out.Today, &out.Revision); err != nil {
			return err
		}
		out.Events, err = readCoverageEvents(ctx, tx, id, false)
		if err != nil {
			return err
		}
		out.History, err = readCoverageEvents(ctx, tx, id, true)
		if !p.Allows("lease.billing.view", All) {
			for _, list := range [][]LeaseCoverageRecord{out.Events, out.History} {
				for i := range list {
					list[i].RentCents = nil
					list[i].ChargeCents = nil
					list[i].TaxRate = ""
				}
			}
		}
		return err
	})
	return out, err
}

func coverageActions(records []LeaseCoverageRecord) []LeaseCoverageEvent {
	var events []LeaseCoverageEvent
	for _, r := range records {
		if r.Action != "withdraw" {
			events = append(events, r.LeaseCoverageEvent)
		}
	}
	return events
}

func (s *Service) ChangeLeaseCoverage(ctx context.Context, a Actor, id int64, in LeaseCoverageInput) (int64, error) {
	var eventID int64
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.view", All) || !p.Allows("lease.manage", All) || !p.Allows("lease.billing.view", All) || !p.Allows("lease.billing.manage", All) {
			return ErrForbidden
		}
		base, err := readLeaseBase(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if base.Archived || base.Lifecycle == "draft" {
			return ErrConflict
		}
		if strings.TrimSpace(in.Reason) == "" || len(in.OperationKey) < 16 || len(in.OperationKey) > 128 {
			return &LeaseValidationError{Message: "A reason and submission identity are required"}
		}
		intent, err := json.Marshal(in)
		if err != nil {
			return err
		}
		var same bool
		err = tx.QueryRow(ctx, `SELECT r.event_id,r.actor_id=$2 AND e.lease_id=$3 AND r.intent=$4::jsonb FROM lease_coverage_revisions r JOIN lease_coverage_events e ON e.id=r.event_id WHERE r.operation_key=$1`, in.OperationKey, p.UserID, id, intent).Scan(&eventID, &same)
		if err == nil {
			if !same {
				return ErrConflict
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		var current int64
		var today string
		if err = tx.QueryRow(ctx, `SELECT coalesce(max(r.id),0),lease_dependency_today($2)::text FROM lease_coverage_revisions r JOIN lease_coverage_events e ON e.id=r.event_id WHERE e.lease_id=$1`, id, p.CompanyID).Scan(&current, &today); err != nil {
			return err
		}
		if current != in.ExpectedRevision {
			return ErrConflict
		}
		records, err := readCoverageEvents(ctx, tx, id, false)
		if err != nil {
			return err
		}
		previousRecords := append([]LeaseCoverageRecord(nil), records...)
		var original *LeaseCoverageRecord
		for i := range records {
			if records[i].ID == in.ID {
				original = &records[i]
				break
			}
		}
		priorDate := ""
		if original != nil {
			priorDate = original.EffectiveOn
		}
		if in.Action == "create" {
			if in.ID != 0 || in.Revision != 0 {
				return ErrConflict
			}
		} else {
			if original == nil || original.Revision != in.Revision || original.Action == "withdraw" {
				return ErrConflict
			}
			pending := original.EffectiveOn > today
			if in.Action == "revise" || in.Action == "withdraw" {
				if !pending {
					return &LeaseValidationError{Message: "Effective events require a factual correction; later real changes use new events"}
				}
			} else if in.Action == "correct" {
				if pending {
					return &LeaseValidationError{Message: "Use Revise for a pending change"}
				}
			} else {
				return ErrConflict
			}
			if in.Action == "correct" && (in.Kind != original.Kind || in.IncomingID != original.IncomingID || in.OutgoingID != original.OutgoingID || !reflect.DeepEqual(in.RentCents, original.RentCents) || !reflect.DeepEqual(in.ChargeCents, original.ChargeCents) || in.TaxRate != original.TaxRate) {
				return &LeaseValidationError{Message: "Factual coverage correction changes the effective date only"}
			}
			if in.Action == "withdraw" {
				saved := original.LeaseCoverageInput
				saved.Action = in.Action
				saved.Reason = in.Reason
				saved.OperationKey = in.OperationKey
				saved.ExpectedRevision = in.ExpectedRevision
				in = saved
			}
		}
		if in.Action != "withdraw" && in.Kind != "add" && (in.RentCents != nil || in.ChargeCents != nil || in.TaxRate != "") {
			return &LeaseValidationError{Message: "Remove and Replace do not accept new charges or rates; Replace carries the outgoing rate"}
		}
		if in.Action != "withdraw" && in.IncomingID > 0 && (original == nil || original.IncomingID != in.IncomingID || original.EffectiveOn != in.EffectiveOn) {
			if !p.Allows("asset.view", All) {
				return ErrNotFound
			}
			var eligible bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assets WHERE id=$1 AND company_id=$2 AND ownership_class='company_owned' AND deleted_at IS NULL)`, in.IncomingID, p.CompanyID).Scan(&eligible); err != nil {
				return err
			}
			if !eligible {
				return ErrConflict
			}
		}
		if in.Action == "create" {
			if err = tx.QueryRow(ctx, `INSERT INTO lease_coverage_events(lease_id,actor_id) VALUES($1,$2) RETURNING id`, id, p.UserID).Scan(&eventID); err != nil {
				return err
			}
			in.ID = eventID
		} else {
			eventID = in.ID
		}
		if original == nil {
			records = append(records, LeaseCoverageRecord{LeaseCoverageInput: in})
		} else {
			*original = LeaseCoverageRecord{LeaseCoverageInput: in}
		}
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT terms FROM lease_finalizations WHERE lease_id=$1`, id).Scan(&raw); err != nil {
			return err
		}
		var terms []DraftLeaseTerms
		if err = json.Unmarshal(raw, &terms); err != nil {
			return err
		}
		var initial []LeaseAssetDependency
		for _, term := range terms {
			if term.Selected {
				initial = append(initial, LeaseAssetDependency{AssetID: term.AssetID, StartsOn: base.StartsOn})
			}
		}
		var end *string
		if base.Lifecycle == "terminated" {
			for _, row := range base.Coverage {
				if row.EndsOn == nil {
					return ErrConflict
				}
				if end == nil || *row.EndsOn > *end {
					value := *row.EndsOn
					end = &value
				}
			}
			if end == nil {
				return ErrConflict
			}
		}
		priorCoverage, err := projectLeaseCoverage(initial, base.StartsOn, end, coverageActions(previousRecords))
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(priorCoverage, base.Coverage) {
			return &LeaseValidationError{Message: "Existing coverage differs from recorded membership history; explicit source mapping review is required before a new change"}
		}
		coverage, err := projectLeaseCoverage(initial, base.StartsOn, end, coverageActions(records))
		if err != nil {
			return err
		}
		// Validate the complete rate succession before changing the dependency
		// projection. Replacements inherit the outgoing allocation at that event.
		if _, err = coverageBillingTerms(terms, records, "9999-12-31", false); err != nil {
			return err
		}
		var exempt bool
		if err = tx.QueryRow(ctx, `SELECT tax_exempt FROM customers WHERE id=$1`, base.CustomerID).Scan(&exempt); err != nil {
			return err
		}
		for _, record := range records {
			if record.Action == "withdraw" {
				continue
			}
			charges := coverageChargeTerms(records, record.EffectiveOn)
			if len(charges) > 0 {
				if _, err = leaseObligationInput(charges, false, exempt); err != nil {
					return err
				}
			}
			date, err := time.Parse(time.DateOnly, record.EffectiveOn)
			if err != nil {
				return err
			}
			due := time.Date(date.Year(), date.Month()+1, 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
			if end != nil && due > *end {
				continue
			}
			selected, err := coverageBillingTerms(terms, records, due, false)
			if err != nil {
				return err
			}
			if len(selected) > 0 {
				if _, err = leaseObligationInput(selected, true, exempt); err != nil {
					return err
				}
			}
		}
		if err = s.WriteLeaseDependencies(ctx, tx, a, LeaseDependencies{LeaseID: id, CustomerID: base.CustomerID, PremisesID: base.PremisesID, Lifecycle: base.Lifecycle, Coverage: coverage}); err != nil {
			return err
		}
		payload, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO lease_coverage_revisions(event_id,actor_id,operation_key,intent,action,reason,payload) VALUES($1,$2,$3,$4,$5,$6,$7)`, eventID, p.UserID, in.OperationKey, intent, in.Action, in.Reason, payload); err != nil {
			return err
		}
		first := in.EffectiveOn
		if priorDate != "" && priorDate < first {
			first = priorDate
		}
		if err = s.RecordLeaseBillingSourceChange(ctx, tx, a, id, first, nil, "Coverage "+in.Action+": "+in.Reason); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "lease.coverage."+in.Action, All, "lease", id, map[string]any{"event_id": eventID, "effective_on": in.EffectiveOn, "kind": in.Kind, "outgoing_id": in.OutgoingID, "incoming_id": in.IncomingID, "reason": in.Reason, "coverage": coverage})
	})
	return eventID, err
}

// Coverage billing applies every action from the preceding month. This keeps
// the outgoing current-month allocation intact even for a first-day action.
func coverageBillingTerms(initial []DraftLeaseTerms, records []LeaseCoverageRecord, due string, initialCharge bool) ([]DraftLeaseTerms, error) {
	active := map[int64]DraftLeaseTerms{}
	for _, term := range initial {
		if term.Selected {
			active[term.AssetID] = term
		}
	}
	if !initialCharge {
		ordered := append([]LeaseCoverageRecord(nil), records...)
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].EffectiveOn == ordered[j].EffectiveOn {
				return ordered[i].ID < ordered[j].ID
			}
			return ordered[i].EffectiveOn < ordered[j].EffectiveOn
		})
		for _, r := range ordered {
			if r.Action == "withdraw" || r.EffectiveOn >= due {
				continue
			}
			var term DraftLeaseTerms
			switch r.Kind {
			case "add":
				zero := int64(0)
				term = DraftLeaseTerms{AssetID: r.IncomingID, Selected: true, InitialCents: &zero, RentCents: r.RentCents, TaxRate: r.TaxRate}
				if r.ChargeCents != nil {
					term.InitialCents = r.ChargeCents
				}
			case "replace", "remove":
				var ok bool
				term, ok = active[r.OutgoingID]
				if !ok {
					return nil, &LeaseValidationError{Message: "Outgoing Asset has no approved billing allocation"}
				}
				delete(active, r.OutgoingID)
				if r.Kind == "remove" {
					continue
				}
				zero := int64(0)
				term.AssetID = r.IncomingID
				term.InitialCents = &zero
			default:
				return nil, ErrConflict
			}
			if _, err := leaseObligationInput([]DraftLeaseTerms{term}, true, false); err != nil {
				return nil, err
			}
			if _, exists := active[term.AssetID]; exists {
				return nil, ErrConflict
			}
			active[term.AssetID] = term
		}
	}
	var out []DraftLeaseTerms
	for _, term := range active {
		out = append(out, term)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AssetID < out[j].AssetID })
	return out, nil
}

// This helper is shared by automatic generation and reviewed manual retry.
func currentCoverageBillingTerms(ctx context.Context, tx pgx.Tx, lease int64, initial []DraftLeaseTerms, due time.Time, kind string) ([]DraftLeaseTerms, bool, error) {
	records, err := readCoverageEvents(ctx, tx, lease, false)
	if err != nil || len(records) == 0 {
		return initial, false, err
	}
	var ended bool
	if err = tx.QueryRow(ctx, `SELECT lifecycle='terminated' AND $2::date>coalesce((SELECT max(ends_on) FROM lease_asset_dependencies WHERE lease_id=$1),'-infinity'::date) FROM lease_master_dependencies WHERE lease_id=$1`, lease, due).Scan(&ended); err != nil {
		return nil, true, err
	}
	if ended && kind != "initial" {
		return nil, true, nil
	}
	if kind == "coverage" {
		return coverageChargeTerms(records, due.Format(time.DateOnly)), true, nil
	}
	terms, err := coverageBillingTerms(initial, records, due.Format(time.DateOnly), kind == "initial")
	return terms, true, err
}

func coverageChargeTerms(records []LeaseCoverageRecord, day string) []DraftLeaseTerms {
	var terms []DraftLeaseTerms
	for _, r := range records {
		if r.Action != "withdraw" && r.Kind == "add" && r.EffectiveOn == day && r.ChargeCents != nil && *r.ChargeCents > 0 {
			terms = append(terms, DraftLeaseTerms{AssetID: r.IncomingID, Selected: true, InitialCents: r.ChargeCents, RentCents: r.RentCents, TaxRate: r.TaxRate})
		}
	}
	return terms
}
