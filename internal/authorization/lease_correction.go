package authorization

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// LeaseFactInput intentionally has no identity, pricing or termination fields.
// Those facts cannot be overwritten by an ordinary term/premises correction.
type LeaseFactInput struct {
	ExpectedRevision int64  `json:"expected_revision"`
	StartsOn         string `json:"starts_on"`
	EndsOn           string `json:"ends_on"`
	PremisesID       int64  `json:"premises_id"`
	Reason           string `json:"reason"`
	OperationKey     string `json:"operation_key"`
	Factual          bool   `json:"factual"`
}

type leaseFactSnapshot struct {
	StartsOn   string          `json:"starts_on"`
	EndsOn     string          `json:"ends_on"`
	PremisesID *int64          `json:"premises_id"`
	Premises   json.RawMessage `json:"premises"`
}

type LeaseFactRecord struct {
	ID, ActorID                                                      int64
	Reason, RecordedAt, BeforeStart, BeforeEnd, AfterStart, AfterEnd string
	BeforePremises, AfterPremises                                    *int64
}

type LeaseFactReview struct {
	Base      DraftLeaseBase
	CanManage bool
	History   []LeaseFactRecord
	Premises  []LeaseChoice
}

func (s *Service) ReviewLeaseFacts(ctx context.Context, a Actor, id int64) (LeaseFactReview, error) {
	var out LeaseFactReview
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
		out.CanManage = !out.Base.Archived && p.Allows("lease.manage", All)
		if p.Allows("location.view", All) {
			choices, err := tx.Query(ctx, `SELECT id,title FROM locations WHERE company_id=$1 AND object_type='customer' AND object_id=$2 AND deleted_at IS NULL ORDER BY title,id`, p.CompanyID, out.Base.CustomerID)
			if err != nil {
				return err
			}
			for choices.Next() {
				var c LeaseChoice
				if err = choices.Scan(&c.ID, &c.Label); err != nil {
					choices.Close()
					return err
				}
				out.Premises = append(out.Premises, c)
			}
			err = choices.Err()
			choices.Close()
			if err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT id,actor_id,reason,recorded_at::text,before_value->>'starts_on',before_value->>'ends_on',(before_value->>'premises_id')::bigint,after_value->>'starts_on',after_value->>'ends_on',(after_value->>'premises_id')::bigint FROM lease_fact_corrections WHERE lease_id=$1 ORDER BY id DESC`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r LeaseFactRecord
			if err = rows.Scan(&r.ID, &r.ActorID, &r.Reason, &r.RecordedAt, &r.BeforeStart, &r.BeforeEnd, &r.BeforePremises, &r.AfterStart, &r.AfterEnd, &r.AfterPremises); err != nil {
				return err
			}
			out.History = append(out.History, r)
		}
		return rows.Err()
	})
	return out, err
}

// CorrectLeaseFacts preserves the original finalization, billing terms and
// physical placement. The existing shared fence serializes all domain writers.
func (s *Service) CorrectLeaseFacts(ctx context.Context, a Actor, id int64, in LeaseFactInput) error {
	return s.transactOutcome(ctx, a, false, false, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.view", All) || !p.Allows("lease.manage", All) {
			return ErrForbidden
		}
		base, err := readLeaseBase(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if base.Archived || base.Lifecycle == "draft" {
			return ErrConflict
		}
		invalid := func(message string) error { return &LeaseValidationError{Message: message} }
		if !in.Factual {
			return invalid("Confirm a factual correction; renegotiation requires a new Lease")
		}
		if strings.TrimSpace(in.Reason) == "" || len(in.OperationKey) < 16 || len(in.OperationKey) > 128 {
			return invalid("A reason and submission identity are required")
		}
		intent, err := json.Marshal(in)
		if err != nil {
			return err
		}
		var same bool
		err = tx.QueryRow(ctx, `SELECT actor_id=$2 AND lease_id=$3 AND intent=$4::jsonb FROM lease_fact_corrections WHERE operation_key=$1`, in.OperationKey, p.UserID, id, intent).Scan(&same)
		if err == nil {
			if !same {
				return ErrConflict
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if base.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		for _, day := range []string{in.StartsOn, in.EndsOn} {
			if _, err = time.Parse(time.DateOnly, day); err != nil {
				return invalid("Valid term dates are required")
			}
		}
		if in.EndsOn < in.StartsOn {
			return invalid("Fixed term end must be on or after start")
		}
		var due, storedState string
		var raw, premises, oldPremises []byte
		if err = tx.QueryRow(ctx, `SELECT f.initial_due_on::text,f.terms,f.premises_snapshot,l.lifecycle FROM lease_finalizations f JOIN leases l ON l.id=f.lease_id WHERE f.lease_id=$1`, id).Scan(&due, &raw, &oldPremises, &storedState); err != nil {
			return err
		}
		if due > in.StartsOn {
			return invalid("Initial due date must remain on or before commencement")
		}
		if in.PremisesID < 1 {
			return invalid("Select an active same-Customer premises")
		}
		if base.PremisesID == nil || *base.PremisesID != in.PremisesID {
			if !p.Allows("location.view", All) {
				return ErrNotFound
			}
		}
		err = tx.QueryRow(ctx, `SELECT to_jsonb(l) FROM locations l WHERE id=$1 AND company_id=$2 AND object_type='customer' AND object_id=$3 AND deleted_at IS NULL`, in.PremisesID, p.CompanyID, base.CustomerID).Scan(&premises)
		if err == pgx.ErrNoRows {
			return invalid("Select an active same-Customer premises")
		}
		if err != nil {
			return err
		}
		// The prior snapshot is contractual history, not today's mutable Location.
		err = tx.QueryRow(ctx, `SELECT after_value->'premises' FROM lease_fact_corrections WHERE lease_id=$1 ORDER BY id DESC LIMIT 1`, id).Scan(&oldPremises)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		notices, err := readLeaseNotices(ctx, tx, id)
		if err != nil {
			return err
		}
		var end *string
		cancelled := false
		if len(notices) > 0 && notices[0].Action != "withdraw" {
			n := notices[0]
			end = &n.EffectiveOn
			cancelled = n.Category == "cancel_before_start"
			if strings.HasSuffix(n.Category, "_ordinary") {
				boundary, e := leaseOrdinaryNoticeEnd(in.EndsOn, n.NoticeOn)
				if e != nil {
					return e
				}
				if boundary != n.EffectiveOn {
					return invalid("Repair or withdraw the recorded ordinary notice before changing its eligible boundary")
				}
			}
		}
		if storedState == "terminated" {
			return invalid("Imported termination requires explicit source mapping before correction")
		}
		events, err := readCoverageEvents(ctx, tx, id, false)
		if err != nil {
			return err
		}
		var terms []DraftLeaseTerms
		if err = json.Unmarshal(raw, &terms); err != nil {
			return err
		}
		initial := func(start string) []LeaseAssetDependency {
			var rows []LeaseAssetDependency
			for _, term := range terms {
				if term.Selected {
					rows = append(rows, LeaseAssetDependency{AssetID: term.AssetID, StartsOn: start})
				}
			}
			return rows
		}
		var coverage []LeaseAssetDependency
		if !cancelled {
			previous, e := projectLeaseCoverage(initial(base.StartsOn), base.StartsOn, end, coverageActions(events))
			if e != nil {
				return e
			}
			if !reflect.DeepEqual(previous, base.Coverage) {
				return invalid("Existing coverage differs from recorded history; repair source mapping first")
			}
			coverage, err = projectLeaseCoverage(initial(in.StartsOn), in.StartsOn, end, coverageActions(events))
			if err != nil {
				return err
			}
		} else if in.StartsOn != base.StartsOn {
			return invalid("Correct cancellation facts before changing cancelled commencement")
		}
		before, err := json.Marshal(leaseFactSnapshot{base.StartsOn, base.EndsOn, base.PremisesID, oldPremises})
		if err != nil {
			return err
		}
		after, err := json.Marshal(leaseFactSnapshot{in.StartsOn, in.EndsOn, &in.PremisesID, premises})
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO lease_fact_corrections(lease_id,actor_id,operation_key,intent,reason,before_value,after_value) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, p.UserID, in.OperationKey, intent, in.Reason, before, after); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE leases SET starts_on=$2,ends_on=$3,premises_id=$4,details_revision=details_revision+1,updated_at=clock_timestamp() WHERE id=$1`, id, in.StartsOn, in.EndsOn, in.PremisesID); err != nil {
			return err
		}
		if err = s.WriteLeaseDependencies(ctx, tx, a, LeaseDependencies{LeaseID: id, CustomerID: base.CustomerID, PremisesID: &in.PremisesID, Lifecycle: storedState, Coverage: coverage}); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `SELECT record_lease_billing_source_change($1,'0001-01-01'::date,NULL,$2)`, id, "Agreement correction: "+in.Reason); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "lease.facts.correct", All, "lease", id, map[string]any{"before": json.RawMessage(before), "after": json.RawMessage(after), "reason": in.Reason})
	})
}
