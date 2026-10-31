package authorization

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type LeaseNoticeInput struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Action           string `json:"action"`
	Category         string `json:"category"`
	Initiator        string `json:"initiator"`
	NoticeOn         string `json:"notice_on"`
	EffectiveOn      string `json:"effective_on"`
	Reason           string `json:"reason"`
	OperationKey     string `json:"operation_key"`
}

type LeaseNoticeRecord struct {
	LeaseNoticeInput
	Revision   int64
	RecordedAt string
}

type LeaseNoticeReview struct {
	Base      DraftLeaseBase
	Today     string
	Revision  int64
	CanManage bool
	History   []LeaseNoticeRecord
}

func readLeaseNotices(ctx context.Context, tx pgx.Tx, id int64) ([]LeaseNoticeRecord, error) {
	rows, err := tx.Query(ctx, `SELECT id,after_value,recorded_at::text FROM lease_notice_revisions WHERE lease_id=$1 ORDER BY id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []LeaseNoticeRecord
	for rows.Next() {
		var r LeaseNoticeRecord
		var raw []byte
		if err = rows.Scan(&r.Revision, &raw, &r.RecordedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &r.LeaseNoticeInput); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func (s *Service) ReviewLeaseNotice(ctx context.Context, a Actor, id int64) (LeaseNoticeReview, error) {
	var out LeaseNoticeReview
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
		if err = tx.QueryRow(ctx, `SELECT lease_dependency_today($1)::text`, p.CompanyID).Scan(&out.Today); err != nil {
			return err
		}
		out.History, err = readLeaseNotices(ctx, tx, id)
		if len(out.History) > 0 {
			out.Revision = out.History[0].Revision
		}
		return err
	})
	return out, err
}

// Notice is base agreement authority. It neither edits billing terms nor grants
// access to resulting Invoices; source review flags are an invariant side effect.
func (s *Service) ChangeLeaseNotice(ctx context.Context, a Actor, id int64, in LeaseNoticeInput) error {
	return s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
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
		if strings.TrimSpace(in.Reason) == "" || len(in.OperationKey) < 16 || len(in.OperationKey) > 128 {
			return invalid("A reason and submission identity are required")
		}
		intent, err := json.Marshal(in)
		if err != nil {
			return err
		}
		var same bool
		err = tx.QueryRow(ctx, `SELECT actor_id=$2 AND lease_id=$3 AND intent=$4::jsonb FROM lease_notice_revisions WHERE operation_key=$1`, in.OperationKey, p.UserID, id, intent).Scan(&same)
		if err == nil {
			if !same {
				return ErrConflict
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		records, err := readLeaseNotices(ctx, tx, id)
		if err != nil {
			return err
		}
		var prior LeaseNoticeInput
		var revision int64
		if len(records) > 0 {
			prior = records[0].LeaseNoticeInput
			revision = records[0].Revision
		}
		if revision != in.ExpectedRevision {
			return ErrConflict
		}
		var today string
		if err = tx.QueryRow(ctx, `SELECT lease_dependency_today($1)::text`, p.CompanyID).Scan(&today); err != nil {
			return err
		}
		if base.Lifecycle == "terminated" {
			return invalid("Effective termination requires a separate factual correction")
		}
		active := revision != 0 && prior.Action != "withdraw"
		switch in.Action {
		case "create":
			if active {
				return ErrConflict
			}
		case "revise", "withdraw":
			if !active || prior.Category == "cancel_before_start" || prior.EffectiveOn < today {
				return ErrConflict
			}
		default:
			return invalid("Select Record, Revise or Withdraw")
		}
		if in.Action == "withdraw" {
			saved := prior
			saved.Action = in.Action
			saved.Reason = in.Reason
			saved.OperationKey = in.OperationKey
			saved.ExpectedRevision = in.ExpectedRevision
			in = saved
		} else {
			initiators := map[string]string{"cancel_before_start": "", "customer_ordinary": "customer", "company_ordinary": "company", "customer_early": "customer", "company_default": "company", "mutual_early": "mutual", "other_early": "other"}
			expected, ok := initiators[in.Category]
			if !ok {
				return invalid("Select a termination category")
			}
			if (expected != "" && in.Initiator != expected) || (in.Initiator != "customer" && in.Initiator != "company" && in.Initiator != "mutual" && in.Initiator != "other") {
				return invalid("Select the initiator appropriate to this category")
			}
			_, err := time.Parse(time.DateOnly, in.NoticeOn)
			if err != nil || in.NoticeOn > today {
				return invalid("Notice date must be a valid current or past company date")
			}
			if in.Category == "cancel_before_start" {
				if base.StartsOn <= today {
					return invalid("Cancel Before Start requires a Scheduled Lease")
				}
				in.EffectiveOn = today
			} else {
				if base.StartsOn > today {
					return invalid("Use Cancel Before Start for a Scheduled Lease")
				}
				if strings.HasSuffix(in.Category, "_ordinary") {
					boundary, err := leaseOrdinaryNoticeEnd(base.EndsOn, in.NoticeOn)
					if err != nil {
						return err
					}
					in.EffectiveOn = boundary
				}
				if _, err = time.Parse(time.DateOnly, in.EffectiveOn); err != nil || in.EffectiveOn < base.StartsOn || in.EffectiveOn < in.NoticeOn {
					return invalid("Effective date must be an inclusive in-force day on or after notice")
				}
			}
		}
		coverageEvents, err := readCoverageEvents(ctx, tx, id, false)
		if err != nil {
			return err
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
		var oldEnd *string
		if active {
			oldEnd = &prior.EffectiveOn
		}
		previous, err := projectLeaseCoverage(initial, base.StartsOn, oldEnd, coverageActions(coverageEvents))
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(previous, base.Coverage) {
			return invalid("Existing coverage differs from recorded history; repair source mapping before termination")
		}
		var coverage []LeaseAssetDependency
		if in.Category == "cancel_before_start" && in.Action != "withdraw" {
			if len(coverageActions(coverageEvents)) > 0 {
				return invalid("Repair scheduled coverage changes before cancellation")
			}
		} else {
			var end *string
			if in.Action != "withdraw" {
				end = &in.EffectiveOn
			}
			coverage, err = projectLeaseCoverage(initial, base.StartsOn, end, coverageActions(coverageEvents))
			if err != nil {
				return err
			}
		}
		before, err := json.Marshal(prior)
		if err != nil {
			return err
		}
		after, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO lease_notice_revisions(lease_id,actor_id,operation_key,intent,action,category,initiator,notice_on,effective_on,reason,before_value,after_value) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, p.UserID, in.OperationKey, intent, in.Action, in.Category, in.Initiator, in.NoticeOn, in.EffectiveOn, in.Reason, before, after); err != nil {
			return err
		}
		// Keep the stored lifecycle unchanged: the clock and notice determine reads.
		var state string
		if err = tx.QueryRow(ctx, `SELECT lifecycle FROM leases WHERE id=$1`, id).Scan(&state); err != nil {
			return err
		}
		if err = s.WriteLeaseDependencies(ctx, tx, a, LeaseDependencies{LeaseID: id, CustomerID: base.CustomerID, PremisesID: base.PremisesID, Lifecycle: state, Coverage: coverage}); err != nil {
			return err
		}
		first := in.EffectiveOn
		if active && prior.EffectiveOn < first {
			first = prior.EffectiveOn
		}
		if in.Category == "cancel_before_start" {
			first = "0001-01-01"
		}
		if _, err = tx.Exec(ctx, `SELECT record_lease_billing_source_change($1,$2::date,NULL,$3)`, id, first, "Termination "+in.Action+": "+in.Reason); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "lease.notice."+in.Action, All, "lease", id, map[string]any{"before": json.RawMessage(before), "after": json.RawMessage(after)})
	})
}
