package services

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/jackc/pgx/v5"
)

// canonicalActivityScope keeps operational history on the same row/field
// projection as exports. Configuration and sealed feeds have separate gates.
func canonicalActivityScope(scope ActivityScope) (authorization.ActivityFilter, bool) {
	f := authorization.ActivityFilter{}
	operationalOnly := false
	if filtered, ok := scope.(FilteredActivityScope); ok {
		if !filtered.Operational {
			return f, false
		}
		operationalOnly = true
		f.ActorID = filtered.ActorID
		f.Actions = filtered.Actions
		if !filtered.From.IsZero() {
			f.From = &filtered.From
		}
		if !filtered.Until.IsZero() {
			f.Until = &filtered.Until
		}
		scope = filtered.Scope
	}
	switch scope := scope.(type) {
	case TenantActivityScope:
		if len(f.Actions) == 0 {
			f.Actions = scope.Actions
		}
	case TypeActivityScope:
		for _, typ := range scope.Types {
			if !authorization.ValidActivityType(string(typ)) {
				if operationalOnly {
					continue
				}
				return f, false
			}
			f.Types = append(f.Types, string(typ))
		}
		if len(f.Types) == 0 {
			f.NoMatch = true
		}
		if len(f.Actions) == 0 {
			f.Actions = scope.Actions
		}
	case ObjectActivityScope:
		if len(scope.Refs) != 1 || !authorization.ValidActivityType(string(scope.Refs[0].Type)) {
			return f, false
		}
		f.Types = []string{string(scope.Refs[0].Type)}
		f.RecordID = scope.Refs[0].ID
	case ScheduleActivityScope:
		f.Types = []string{"job"}
		f.Actions = []string{"scheduled", "rescheduled", "dispatched"}
	case ConversionActivityScope:
		f.ProvenanceEstimateID = scope.EstimateID
	default:
		return f, false
	}
	return f, true
}

func (s *ActivityService) listAuthorized(ctx context.Context, req authorization.Request, request ActivityListRequest, f authorization.ActivityFilter) (ActivityPage, error) {
	base := request
	if wrapped, ok := base.Scope.(FilteredActivityScope); ok {
		base.Scope = wrapped.Scope
	}
	if scope, ok := base.Scope.(ConversionActivityScope); ok {
		scope.CurrentAuthority = true
		base.Scope = scope
	}
	// Scope/target validation is canonical here, independent of the legacy
	// collaboration directory (Contacts/Locations have history, not Comments).
	if err := authorization.ValidateActivityFilter(f); err != nil {
		return ActivityPage{}, err
	}
	base.Scope = TenantActivityScope{}
	limit, direction, err := s.validateListRequest(base)
	if err != nil {
		return ActivityPage{}, err
	}
	entries := []ActivityEntry{}
	err = req.Service.WithinActor(ctx, req.Actor, func(tx pgx.Tx, p authorization.Principal) error {
		if p.CompanyID != request.CompanyID {
			return authorization.ErrNotFound
		}
		return authorization.WalkActivity(ctx, tx, p, f, false, func(e authorization.ActivityRecord) (bool, error) {
			if c := request.Page.Cursor; c != nil {
				newer := e.Accepted.After(c.CreatedAt) || e.Accepted.Equal(c.CreatedAt) && e.ID > c.ID
				older := e.Accepted.Before(c.CreatedAt) || e.Accepted.Equal(c.CreatedAt) && e.ID < c.ID
				if direction == ActivityNewer && !newer {
					return false, nil
				}
				if direction != ActivityNewer && !older {
					return true, nil
				}
			}
			meta, err := json.Marshal(map[string]any{"actor_name": e.Actor, "entity_name": e.Name, "facts": e.Facts, "occurred_at": e.Occurred})
			if err != nil {
				return false, err
			}
			url := ""
			if e.ProvenanceEstimateID == 0 {
				prefix := e.Type + "s"
				if e.Type == "time_entry" {
					prefix = "time-entries"
				}
				url = fmt.Sprintf("/%s/%d", prefix, e.RecordID)
			}
			entries = append(entries, ActivityEntry{CurrentURL: url, ID: e.ID, ActorID: e.ActorID, Action: e.Action, Target: objectref.New(objectref.Type(e.Type), e.RecordID), Metadata: string(meta), CreatedAt: e.Accepted})
			if direction == ActivityNewer {
				if len(entries) > limit+1 {
					entries = entries[1:]
				}
				return true, nil
			}
			return len(entries) <= limit, nil
		})
	})
	if err != nil {
		return ActivityPage{}, err
	}
	page := ActivityPage{}
	if direction == ActivityNewer {
		page.HasOlder = request.Page.Cursor != nil
		page.HasNewer = len(entries) > limit
		if page.HasNewer {
			entries = entries[1:]
		}
	} else {
		page.HasNewer = request.Page.Cursor != nil
		page.HasOlder = len(entries) > limit
		if page.HasOlder {
			entries = entries[:limit]
		}
	}
	page.Entries = entries
	if len(entries) > 0 {
		first, last := entries[0], entries[len(entries)-1]
		page.NewerCursor = &ActivityCursor{CreatedAt: first.CreatedAt, ID: first.ID}
		page.OlderCursor = &ActivityCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}
