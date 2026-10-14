package authorization

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/jackc/pgx/v5"
)

// DispatchPlan is one shared interval and an unordered set of equal assignees.
// Nil instants explicitly mean unscheduled/no arrival window.
type DispatchPlan struct {
	Start, End, ArrivalStart, ArrivalEnd *time.Time
	Assignees                            []int64
}

type DispatchReview struct {
	ID, Title, Zone        string
	FormRevision           string
	Plan                   DispatchPlan
	CanSchedule, CanAssign bool
	Choices                []DispatchUser
	Overlaps               []DispatchOverlap
	Warnings               []string
	Draft                  map[string]string `json:"-"`
	Error                  string            `json:"-"`
	CurrentPlan            *DispatchPlan     `json:"-"`
	CurrentUsers           []string          `json:"-"`
}

// DispatchDraft exposes only the acting user's proposal while the current Job
// and planning action remain authorized. It never revives the old acceptance.
func (s *Service) DispatchDraft(ctx context.Context, a Actor, id int64, reviewID string) (DispatchPlan, error) {
	var plan DispatchPlan
	err := s.WithinActor(saveaction.WithoutAction(ctx), a, func(tx pgx.Tx, p Principal) error {
		if _, _, err := dispatchState(ctx, tx, p, id); err != nil {
			return err
		}
		var data []byte
		if err := tx.QueryRow(ctx, `SELECT proposal FROM job_dispatch_reviews WHERE id=$1 AND user_id=$2 AND job_id=$3`, reviewID, p.UserID, id).Scan(&data); err == pgx.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		var review DispatchReview
		if err := json.Unmarshal(data, &review); err != nil {
			return err
		}
		plan = review.Plan
		return nil
	})
	return plan, err
}

type DispatchOverlap struct {
	ID         int64
	Title      string
	Start, End time.Time
	Assignees  []int64
	Revision   string
}

type DispatchUser struct {
	ID                    int64
	Name                  string
	Selected, Unavailable bool
}

func dispatchState(ctx context.Context, tx pgx.Tx, p Principal, id int64) (DispatchReview, string, error) {
	var out DispatchReview
	if _, err := parentAccess(ctx, tx, p, "job", id, "", true); err != nil {
		return out, "", err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT j.job_type,j.start_time,j.end_time,j.arrival_window_start,j.arrival_window_end,j.deleted_at IS NULL,cs.timezone FROM jobs j JOIN company_settings cs ON cs.company_id=j.company_id WHERE j.id=$1`, id).Scan(&out.Title, &out.Plan.Start, &out.Plan.End, &out.Plan.ArrivalStart, &out.Plan.ArrivalEnd, &active, &out.Zone); err != nil {
		return out, "", err
	}
	if !active {
		return out, "", ErrForbidden
	}
	out.CanSchedule, out.CanAssign = p.Allows("job.schedule", All), p.Allows("job.assign", All)
	if !out.CanSchedule && !out.CanAssign {
		return out, "", ErrForbidden
	}
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(user_id ORDER BY user_id),'{}') FROM job_assignments WHERE job_id=$1`, id).Scan(&out.Plan.Assignees); err != nil {
		return out, "", err
	}
	var revision string
	if err := tx.QueryRow(ctx, `SELECT jsonb_build_array(j.xmin::text,j.due_date,(SELECT jsonb_agg(jsonb_build_array(a.id,a.user_id,a.xmin::text) ORDER BY a.id) FROM job_assignments a WHERE a.job_id=j.id))::text FROM jobs j WHERE j.id=$1`, id).Scan(&revision); err != nil {
		return out, "", err
	}
	data, err := json.Marshal(struct {
		Plan           DispatchPlan
		Zone, Revision string
	}{out.Plan, out.Zone, revision})
	if err != nil {
		return out, "", err
	}
	sum := sha256.Sum256(data)
	return out, hex.EncodeToString(sum[:]), nil
}

func (s *Service) DispatchForm(ctx context.Context, a Actor, id int64) (DispatchReview, error) {
	ctx = saveaction.WithoutAction(ctx)
	var out DispatchReview
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		var err error
		out, _, err = dispatchState(ctx, tx, p, id)
		if err != nil {
			return err
		}
		query := `SELECT id,name,NOT is_active,id=ANY($2) FROM users WHERE company_id=$1 AND (id=ANY($2)`
		if out.CanAssign && p.Allows("directory.view", All) {
			query += ` OR is_active`
		}
		rows, err := tx.Query(ctx, query+`) ORDER BY name,id`, p.CompanyID, out.Plan.Assignees)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u DispatchUser
			if err := rows.Scan(&u.ID, &u.Name, &u.Unavailable, &u.Selected); err != nil {
				return err
			}
			out.Choices = append(out.Choices, u)
		}
		return rows.Err()
	})
	return out, err
}

func validateDispatch(ctx context.Context, tx pgx.Tx, p Principal, before DispatchReview, in *DispatchPlan) error {
	for _, instant := range []*time.Time{in.Start, in.End, in.ArrivalStart, in.ArrivalEnd} {
		if instant != nil && instant.Nanosecond()%1000 != 0 {
			return fmt.Errorf("%w: use at most six fractional-second digits", ErrDispatchInput)
		}
	}
	sort.Slice(in.Assignees, func(i, j int) bool { return in.Assignees[i] < in.Assignees[j] })
	if in.Assignees == nil {
		in.Assignees = []int64{}
	}
	if (in.Start == nil) != (in.End == nil) || in.Start != nil && !in.Start.Before(*in.End) {
		return fmt.Errorf("%w: planned start must precede end", ErrDispatchInput)
	}
	if (in.ArrivalStart == nil) != (in.ArrivalEnd == nil) || in.ArrivalStart != nil && (in.Start == nil || in.ArrivalEnd.Before(*in.ArrivalStart) || in.Start.Before(*in.ArrivalStart) || in.Start.After(*in.ArrivalEnd)) {
		return fmt.Errorf("%w: arrival window must contain planned start", ErrDispatchInput)
	}
	if !before.CanSchedule && !sameDispatchSchedule(*in, before.Plan) {
		return ErrForbidden
	}
	if !reflect.DeepEqual(in.Assignees, before.Plan.Assignees) && !before.CanAssign {
		return ErrForbidden
	}
	for i, id := range in.Assignees {
		if id <= 0 || i > 0 && in.Assignees[i-1] == id {
			return ErrDispatchInput
		}
		retained := false
		for _, old := range before.Plan.Assignees {
			if old == id {
				retained = true
			}
		}
		if !retained && !p.Allows("directory.view", All) {
			return ErrForbidden
		}
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT is_active FROM users WHERE id=$1 AND company_id=$2 FOR UPDATE`, id, p.CompanyID).Scan(&enabled); err == pgx.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if !enabled && !retained {
			return fmt.Errorf("%w: new assignees must be enabled", ErrDispatchInput)
		}
	}
	return nil
}

func sameDispatchSchedule(a, b DispatchPlan) bool {
	left, right := []*time.Time{a.Start, a.End, a.ArrivalStart, a.ArrivalEnd}, []*time.Time{b.Start, b.End, b.ArrivalStart, b.ArrivalEnd}
	for i, t := range left {
		if (t == nil) != (right[i] == nil) || t != nil && !t.Equal(*right[i]) {
			return false
		}
	}
	return true
}

func sameDispatchOverlaps(a, b []DispatchOverlap) bool {
	if len(a) != len(b) {
		return false
	}
	for i, o := range a {
		other := b[i]
		if o.ID != other.ID || o.Title != other.Title || o.Revision != other.Revision || !o.Start.Equal(other.Start) || !o.End.Equal(other.End) || !reflect.DeepEqual(o.Assignees, other.Assignees) {
			return false
		}
	}
	return true
}

var ErrDispatchInput = fmt.Errorf("invalid dispatch")

func dispatchWarnings(ctx context.Context, tx pgx.Tx, p Principal, id int64, out *DispatchReview) error {
	out.Overlaps = nil
	out.Warnings = nil
	out.Choices = nil
	for _, uid := range out.Plan.Assignees {
		var u DispatchUser
		u.ID = uid
		u.Selected = true
		if err := tx.QueryRow(ctx, `SELECT name,NOT is_active FROM users WHERE id=$1 AND company_id=$2`, uid, p.CompanyID).Scan(&u.Name, &u.Unavailable); err != nil {
			return err
		}
		out.Choices = append(out.Choices, u)
		if u.Unavailable {
			out.Warnings = append(out.Warnings, u.Name+" is unavailable (disabled; retained assignment).")
			continue
		}
		assignee, err := principal(ctx, tx, Actor{UserID: uid}, false)
		if err != nil && err != ErrForbidden {
			return err
		}
		if err != nil || !assignee.Allows("job.view", Assigned) {
			out.Warnings = append(out.Warnings, u.Name+" has incompatible Job View grants. Assignment does not grant access.")
		}
	}
	if out.Plan.Start == nil {
		return nil
	}
	var beyond bool
	if err := tx.QueryRow(ctx, `SELECT due_date IS NOT NULL AND ($2::timestamptz AT TIME ZONE $3)::date>due_date FROM jobs WHERE id=$1`, id, out.Plan.End, out.Zone).Scan(&beyond); err != nil {
		return err
	}
	if beyond {
		out.Warnings = append(out.Warnings, "The planned interval extends beyond the independent due date. Dispatch does not change Status.")
	}
	rows, err := tx.Query(ctx, `SELECT j.id,j.job_type,j.start_time,j.end_time,array_agg(a.user_id ORDER BY a.user_id),jsonb_build_array(j.xmin::text,jsonb_agg(jsonb_build_array(a.id,a.xmin::text) ORDER BY a.id))::text
 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.company_id=$1 AND j.id<>$2 AND j.deleted_at IS NULL AND j.start_time<$4 AND j.end_time>$3
 AND EXISTS(SELECT 1 FROM job_assignments common WHERE common.job_id=j.id AND common.user_id=ANY($5)) GROUP BY j.id,j.xmin ORDER BY j.id`, p.CompanyID, id, out.Plan.Start, out.Plan.End, out.Plan.Assignees)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var o DispatchOverlap
		if err := rows.Scan(&o.ID, &o.Title, &o.Start, &o.End, &o.Assignees, &o.Revision); err != nil {
			return err
		}
		out.Overlaps = append(out.Overlaps, o)
	}
	return rows.Err()
}

func (s *Service) ReviewDispatch(ctx context.Context, a Actor, id int64, in DispatchPlan) (DispatchReview, error) {
	var out DispatchReview
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		var baseline string
		var err error
		out, baseline, err = dispatchState(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if a.Channel == "api" {
			return ErrForbidden
		}
		if err := validateDispatch(ctx, tx, p, out, &in); err != nil {
			return err
		}
		out.Plan = in
		out.FormRevision, err = saveaction.Revision(ctx, tx, p.UserID, fmt.Sprintf("/jobs/%d", id))
		if err != nil {
			return err
		}
		if err := dispatchWarnings(ctx, tx, p, id, &out); err != nil {
			return err
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		out.ID = hex.EncodeToString(b)
		data, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO job_dispatch_reviews(id,user_id,job_id,baseline,proposal) VALUES($1,$2,$3,$4,$5)`, out.ID, p.UserID, id, baseline, data)
		return err
	})
	return out, err
}

func (s *Service) ConfirmDispatch(ctx context.Context, a Actor, id int64, reviewID string, acknowledge bool) error {
	return s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		before, baseline, err := dispatchState(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if a.Channel == "api" {
			return ErrForbidden
		}
		var reviewed string
		var data []byte
		var committed bool
		err = tx.QueryRow(ctx, `SELECT baseline,proposal,committed_at IS NOT NULL FROM job_dispatch_reviews WHERE id=$1 AND user_id=$2 AND job_id=$3 AND (committed_at IS NOT NULL OR created_at>clock_timestamp()-interval '30 minutes') FOR UPDATE`, reviewID, p.UserID, id).Scan(&reviewed, &data, &committed)
		if err == pgx.ErrNoRows {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if committed {
			return saveaction.ConfirmNoChange(ctx, tx)
		}
		if baseline != reviewed {
			return ErrConflict
		}
		var review DispatchReview
		if err := json.Unmarshal(data, &review); err != nil {
			return err
		}
		if err := validateDispatch(ctx, tx, p, before, &review.Plan); err != nil {
			return err
		}
		current := review
		if err := dispatchWarnings(ctx, tx, p, id, &current); err != nil {
			return err
		}
		if !sameDispatchOverlaps(current.Overlaps, review.Overlaps) || !reflect.DeepEqual(current.Warnings, review.Warnings) || !reflect.DeepEqual(current.Choices, review.Choices) || len(current.Overlaps) > 0 && !acknowledge {
			return ErrConflict
		}
		in := review.Plan
		if sameDispatchSchedule(before.Plan, in) && reflect.DeepEqual(before.Plan.Assignees, in.Assignees) {
			if _, err := tx.Exec(ctx, `UPDATE job_dispatch_reviews SET committed_at=clock_timestamp() WHERE id=$1`, reviewID); err != nil {
				return err
			}
			return saveaction.ConfirmNoChange(ctx, tx)
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET start_time=$2,end_time=$3,arrival_window_start=$4,arrival_window_end=$5,updated_at=clock_timestamp() WHERE id=$1`, id, in.Start, in.End, in.ArrivalStart, in.ArrivalEnd); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND NOT(user_id=ANY($2))`, id, in.Assignees); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id,role) SELECT $1,x,'' FROM unnest($2::bigint[]) x WHERE NOT EXISTS(SELECT 1 FROM job_assignments WHERE job_id=$1 AND user_id=x)`, id, in.Assignees); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET assignments=(SELECT coalesce(jsonb_agg(jsonb_build_object('user_id',u.id,'name',u.name) ORDER BY u.id),'[]') FROM users u WHERE u.id=ANY($2)) WHERE id=$1`, id, in.Assignees); err != nil {
			return err
		}
		for _, facet := range []struct {
			permission string
			changed    bool
		}{{"job.schedule", !sameDispatchSchedule(before.Plan, in)}, {"job.assign", !reflect.DeepEqual(before.Plan.Assignees, in.Assignees)}} {
			if facet.changed {
				if err := audit(ctx, tx, a, p, facet.permission, All, "job", id, map[string]any{"before": before.Plan, "after": in, "review_id": reviewID, "overlaps": review.Overlaps, "overlaps_acknowledged": len(review.Overlaps) > 0 && acknowledge, "warnings": review.Warnings}); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE job_dispatch_reviews SET committed_at=clock_timestamp() WHERE id=$1`, reviewID)
		return err
	})
}
