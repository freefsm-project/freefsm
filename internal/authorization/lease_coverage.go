package authorization

import (
	"context"
	"encoding/json"
	"fmt"
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
		var cancelled bool
		if err = tx.QueryRow(ctx, `SELECT (SELECT effective_on::text FROM lease_notice_current WHERE lease_id=$1 AND action<>'withdraw'),EXISTS(SELECT 1 FROM lease_notice_current WHERE lease_id=$1 AND action<>'withdraw' AND category='cancel_before_start')`, id).Scan(&end, &cancelled); err != nil {
			return err
		}
		if cancelled {
			return ErrConflict
		}
		if base.Lifecycle == "terminated" && end == nil {
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
		billingRevisions, err := readLeaseBillingRevisions(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = validateBillingAllocationSuccession(terms, previousRecords, records, billingRevisions); err != nil {
			return err
		}
		if _, err = coverageBillingTerms(terms, records, "9999-12-31", false, billingRevisions...); err != nil {
			return err
		}
		var exempt bool
		if err = tx.QueryRow(ctx, `SELECT tax_exempt FROM customers WHERE id=$1`, base.CustomerID).Scan(&exempt); err != nil {
			return err
		}
		// Coverage changes preserve the agreement's stored lifecycle. The current
		// lifecycle used above is derived from dates/notices and can advance without
		// a write; persisting it only in dependencies would split the agreement.
		var storedState string
		if err = tx.QueryRow(ctx, `SELECT lifecycle FROM leases WHERE id=$1`, id).Scan(&storedState); err != nil {
			return err
		}
		if err = s.WriteLeaseDependencies(ctx, tx, a, LeaseDependencies{LeaseID: id, CustomerID: base.CustomerID, PremisesID: base.PremisesID, Lifecycle: storedState, Coverage: coverage}); err != nil {
			return err
		}
		payload, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO lease_coverage_revisions(event_id,actor_id,operation_key,intent,action,reason,payload) VALUES($1,$2,$3,$4,$5,$6,$7)`, eventID, p.UserID, in.OperationKey, intent, in.Action, in.Reason, payload); err != nil {
			return err
		}
		// Raw terms and allocation succession were checked above. Validate tax
		// compatibility only for eligible, ungenerated obligations, using the same
		// schedule as billing revisions even when no pricing revision exists yet.
		var due string
		if err = tx.QueryRow(ctx, `SELECT initial_due_on::text FROM lease_finalizations WHERE lease_id=$1`, id).Scan(&due); err != nil {
			return err
		}
		if err = validateLeaseBillingSchedule(ctx, tx, id, terms, exempt, due); err != nil {
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
func coverageBillingTerms(initial []DraftLeaseTerms, records []LeaseCoverageRecord, due string, initialCharge bool, revisions ...LeaseBillingRevisionRecord) ([]DraftLeaseTerms, error) {
	kind := "monthly"
	if initialCharge {
		kind = "initial"
	}
	return replayLeaseBillingTerms(initial, records, due, kind, revisions)
}

// All charge kinds replay the same allocation succession. An explicit Add
// starts fresh terms, even when this Asset had an earlier allocation.
func replayLeaseBillingTerms(initial []DraftLeaseTerms, records []LeaseCoverageRecord, due, kind string, revisions []LeaseBillingRevisionRecord) ([]DraftLeaseTerms, error) {
	state, err := replayLeaseBillingAllocations(initial, records, due, kind, revisions)
	if err != nil {
		return nil, err
	}
	allocations := state.active
	if kind == "coverage" {
		allocations = state.charges
	}
	var ordered []leaseBillingAllocation
	for _, allocation := range allocations {
		ordered = append(ordered, allocation)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].AssetID == ordered[j].AssetID {
			return ordered[i].eventID < ordered[j].eventID
		}
		return ordered[i].AssetID < ordered[j].AssetID
	})
	var out []DraftLeaseTerms
	for _, allocation := range ordered {
		out = append(out, allocation.DraftLeaseTerms)
	}
	return out, nil
}

// Allocation identity is its initial Asset or its Add/Replace event, not just
// the reusable Asset ID. These are replay facts, never invented stored history.
type leaseBillingAllocation struct {
	DraftLeaseTerms
	eventID int64
}

// Membership and one-time entitlement are separate projections: removing or
// replacing an allocation does not withdraw its approved Add charge. Charges
// are keyed by event, so even two same-day Adds of one Asset remain distinct.
type leaseBillingReplay struct {
	active  map[int64]leaseBillingAllocation
	charges map[int64]leaseBillingAllocation
}

func replayLeaseBillingAllocations(initial []DraftLeaseTerms, records []LeaseCoverageRecord, due, kind string, revisions []LeaseBillingRevisionRecord) (leaseBillingReplay, error) {
	state := leaseBillingReplay{active: map[int64]leaseBillingAllocation{}, charges: map[int64]leaseBillingAllocation{}}
	active := state.active
	for _, term := range initial {
		if term.Selected {
			active[term.AssetID] = leaseBillingAllocation{DraftLeaseTerms: term}
		}
	}
	// Replay pricing and membership in effective order. A replacement carries
	// the outgoing terms at its event, not its original finalization allocation.
	nextRevision := 0
	applyThrough := func(day string) {
		for nextRevision < len(revisions) && revisions[nextRevision].EffectiveOn <= day {
			r := revisions[nextRevision]
			for _, change := range r.After {
				if term, ok := active[change.AssetID]; ok {
					rent := change.RentCents
					term.RentCents, term.TaxRate, term.BillingRevisionID = &rent, change.TaxRate, r.ID
					active[change.AssetID] = term
					if _, charged := state.charges[term.eventID]; charged {
						state.charges[term.eventID] = term
					}
				}
			}
			nextRevision++
		}
	}
	if kind != "initial" {
		ordered := append([]LeaseCoverageRecord(nil), records...)
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].EffectiveOn == ordered[j].EffectiveOn {
				return ordered[i].ID < ordered[j].ID
			}
			return ordered[i].EffectiveOn < ordered[j].EffectiveOn
		})
		for _, r := range ordered {
			if r.Action == "withdraw" || r.EffectiveOn > due || (r.EffectiveOn == due && kind != "coverage") {
				continue
			}
			applyThrough(r.EffectiveOn)
			var term leaseBillingAllocation
			switch r.Kind {
			case "add":
				zero := int64(0)
				term = leaseBillingAllocation{DraftLeaseTerms: DraftLeaseTerms{AssetID: r.IncomingID, Selected: true, InitialCents: &zero, RentCents: r.RentCents, TaxRate: r.TaxRate}}
				if r.ChargeCents != nil {
					term.InitialCents = r.ChargeCents
				}
			case "replace", "remove":
				var ok bool
				term, ok = active[r.OutgoingID]
				if !ok {
					return state, &LeaseValidationError{Message: "Outgoing Asset has no approved billing allocation"}
				}
				delete(active, r.OutgoingID)
				if r.Kind == "remove" {
					continue
				}
				zero := int64(0)
				term.AssetID = r.IncomingID
				term.InitialCents = &zero
			default:
				return state, ErrConflict
			}
			term.eventID = r.ID
			if _, err := leaseObligationInput([]DraftLeaseTerms{term.DraftLeaseTerms}, true, false); err != nil {
				return state, err
			}
			if _, exists := active[term.AssetID]; exists {
				return state, ErrConflict
			}
			active[term.AssetID] = term
			if r.Kind == "add" && r.EffectiveOn == due && *term.InitialCents > 0 {
				state.charges[r.ID] = term
			}
		}
	}
	applyThrough(due)
	return state, nil
}

func validateBillingAllocationSuccession(initial []DraftLeaseTerms, before, after []LeaseCoverageRecord, revisions []LeaseBillingRevisionRecord) error {
	for _, revision := range revisions {
		prior, err := replayLeaseBillingAllocations(initial, before, revision.EffectiveOn, "monthly", nil)
		if err != nil {
			return err
		}
		next, err := replayLeaseBillingAllocations(initial, after, revision.EffectiveOn, "monthly", nil)
		if err != nil {
			return err
		}
		for _, change := range revision.After {
			oldAllocation, wasPresent := prior.active[change.AssetID]
			newAllocation, isPresent := next.active[change.AssetID]
			if !wasPresent || !isPresent || oldAllocation.eventID != newAllocation.eventID {
				return &LeaseValidationError{Message: fmt.Sprintf("This coverage change would detach approved billing revision %d for Asset %d effective %s from its billing allocation. Keep that Add or Replace before the revision date, or discard this proposal and review Billing revisions and Coverage together; approved terms cannot be silently reset.", revision.ID, change.AssetID, revision.EffectiveOn)}
			}
		}
	}
	return nil
}

// This helper is shared by automatic generation and reviewed manual retry.
func currentCoverageBillingTerms(ctx context.Context, tx pgx.Tx, lease int64, initial []DraftLeaseTerms, due time.Time, kind string) ([]DraftLeaseTerms, bool, error) {
	terms, eventBilling, _, err := leaseCoverageBillingTermsAt(ctx, tx, lease, initial, due, kind, "9999-12-31")
	return terms, eventBilling, err
}

// effectiveLeasePeriodTerms projects entitlement from approved effective facts,
// independently of obligation/disposition materialization. asOf bounds notice
// applicability for historical reports; due still selects the period's rates.
func effectiveLeasePeriodTerms(ctx context.Context, tx pgx.Tx, lease int64, initial []DraftLeaseTerms, due time.Time, kind, asOf string) ([]DraftLeaseTerms, bool, string, error) {
	if kind == "monthly" {
		var commenced bool
		if err := tx.QueryRow(ctx, `SELECT starts_on<$2::date FROM leases WHERE id=$1`, lease, due).Scan(&commenced); err != nil {
			return nil, false, "", err
		}
		if !commenced {
			return nil, true, "Monthly rent begins in the month after corrected commencement", nil
		}
	}
	return leaseCoverageBillingTermsAt(ctx, tx, lease, initial, due, kind, asOf)
}

// Allocation replay also serves billing-revision review on commencement itself;
// period entitlement is an additional rule, not a restriction on reviewing terms.
func leaseCoverageBillingTermsAt(ctx context.Context, tx pgx.Tx, lease int64, initial []DraftLeaseTerms, due time.Time, kind, asOf string) ([]DraftLeaseTerms, bool, string, error) {
	var start time.Time
	if err := tx.QueryRow(ctx, `SELECT starts_on FROM leases WHERE id=$1`, lease).Scan(&start); err != nil {
		return nil, false, "", err
	}
	revisions, err := readLeaseBillingRevisions(ctx, tx, lease)
	if err != nil {
		return nil, false, "", err
	}
	var unowed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_notice_current WHERE lease_id=$1 AND action<>'withdraw' AND notice_on<=$4::date AND (category='cancel_before_start' OR ($3<>'initial' AND effective_on<$2::date)))`, lease, due, kind, asOf).Scan(&unowed); err != nil {
		return nil, false, "", err
	}
	if unowed {
		return nil, true, "not owed — termination", nil
	}
	records, err := readCoverageEvents(ctx, tx, lease, false)
	if err != nil {
		return nil, false, "", err
	}
	var ended bool
	if err = tx.QueryRow(ctx, `SELECT lifecycle='terminated' AND NOT EXISTS(SELECT 1 FROM lease_notice_current WHERE lease_id=$1 AND notice_on>$3::date) AND $2::date>coalesce((SELECT max(ends_on) FROM lease_asset_dependencies WHERE lease_id=$1),'-infinity'::date) FROM lease_master_dependencies WHERE lease_id=$1`, lease, due, asOf).Scan(&ended); err != nil {
		return nil, true, "", err
	}
	if ended && kind != "initial" {
		return nil, true, "No Assets covered at period start; no charge owed", nil
	}
	terms, err := replayLeaseBillingTerms(initial, records, due.Format(time.DateOnly), kind, revisions)
	if err != nil {
		return nil, false, "", err
	}
	selected := 0
	for i := range terms {
		if len(records) == 0 {
			coveredAt := due
			if kind == "initial" {
				coveredAt = start
			}
			var covered bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_asset_dependencies WHERE lease_id=$1 AND asset_id=$2 AND starts_on<=$3::date AND (ends_on IS NULL OR ends_on>=$3::date))`, lease, terms[i].AssetID, coveredAt).Scan(&covered); err != nil {
				return nil, false, "", err
			}
			terms[i].Selected = terms[i].Selected && covered
		}
		if terms[i].Selected {
			selected++
		}
	}
	if selected == 0 {
		return terms, true, "No Assets covered at period start; no charge owed", nil
	}
	return terms, len(records) > 0, "", nil
}
