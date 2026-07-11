package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/activitylog"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

type ActivityService struct {
	client  *ent.Client
	objects objectref.Directory
}

type ActivityEntry struct {
	ID               int64
	ActorID          int64
	Action           string
	Target           objectref.Ref
	HistoricalTarget string
	Metadata         string
	CreatedAt        time.Time
}

func NewActivityService(client *ent.Client, objects objectref.Directory) *ActivityService {
	return &ActivityService{client: client, objects: objects}
}

func (s *ActivityService) Record(ctx context.Context, actorID int64, action string, target objectref.Ref, metadata map[string]interface{}) error {
	if err := s.validateTarget(target); err != nil {
		return err
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return fmt.Errorf("activity action must not be empty")
	}

	md, err := json.Marshal(metadata)
	if err != nil {
		md = []byte("{}")
	}
	_, err = s.client.ActivityLog.Create().
		SetActorID(actorID).
		SetAction(action).
		SetObjectType(target.ObjectType()).
		SetObjectID(target.ObjectID()).
		SetMetadata(string(md)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("record activity: %w", err)
	}
	return nil
}

func (s *ActivityService) ListForObject(ctx context.Context, target objectref.Ref, limit int) ([]ActivityEntry, error) {
	if err := s.validateTarget(target); err != nil {
		return nil, err
	}
	q := s.client.ActivityLog.Query().
		Where(
			activitylog.ObjectTypeEQ(target.ObjectType()),
			activitylog.ObjectIDEQ(target.ObjectID()),
		).
		Order(ent.Desc(activitylog.FieldCreatedAt))
	if limit > 0 {
		q = q.Limit(limit)
	}
	entries, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list activity: %w", err)
	}
	return mapActivityEntries(entries), nil
}

func (s *ActivityService) ListAll(ctx context.Context, offset, limit int, isAdmin bool) ([]ActivityEntry, int, error) {
	q := s.client.ActivityLog.Query()
	if !isAdmin {
		for _, t := range objectref.AdminOnlyTypes() {
			q = q.Where(activitylog.ObjectTypeNEQ(string(t)))
		}
	}
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count activity: %w", err)
	}
	entries, err := q.
		Order(ent.Desc(activitylog.FieldCreatedAt)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list all activity: %w", err)
	}
	return mapActivityEntries(entries), total, nil
}

func (s *ActivityService) ListByType(ctx context.Context, typ objectref.Type, offset, limit int) ([]ActivityEntry, int, error) {
	if err := s.validateType(typ); err != nil {
		return nil, 0, err
	}
	q := s.client.ActivityLog.Query().
		Where(activitylog.ObjectTypeEQ(string(typ)))
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count activity by type: %w", err)
	}
	entries, err := q.
		Order(ent.Desc(activitylog.FieldCreatedAt)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list activity by type: %w", err)
	}
	return mapActivityEntries(entries), total, nil
}

func (s *ActivityService) ListByTypeAndActions(ctx context.Context, typ objectref.Type, actions []string, offset, limit int) ([]ActivityEntry, int, error) {
	if err := s.validateType(typ); err != nil {
		return nil, 0, err
	}
	if len(actions) == 0 {
		return nil, 0, fmt.Errorf("activity actions must not be empty")
	}
	for _, action := range actions {
		if strings.TrimSpace(action) == "" {
			return nil, 0, fmt.Errorf("activity action must not be empty")
		}
	}
	q := s.client.ActivityLog.Query().
		Where(
			activitylog.ObjectTypeEQ(string(typ)),
			activitylog.ActionIn(actions...),
		)
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count activity by type and actions: %w", err)
	}
	entries, err := q.
		Order(ent.Desc(activitylog.FieldCreatedAt)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list activity by type and actions: %w", err)
	}
	return mapActivityEntries(entries), total, nil
}

func (s *ActivityService) ListByTypes(ctx context.Context, types []objectref.Type, offset, limit int) ([]ActivityEntry, int, error) {
	if len(types) == 0 {
		return nil, 0, fmt.Errorf("activity types must not be empty")
	}
	objectTypes := make([]string, len(types))
	for i, typ := range types {
		if err := s.validateType(typ); err != nil {
			return nil, 0, err
		}
		objectTypes[i] = string(typ)
	}
	q := s.client.ActivityLog.Query().
		Where(activitylog.ObjectTypeIn(objectTypes...))
	total, err := q.Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count activity by types: %w", err)
	}
	entries, err := q.
		Order(ent.Desc(activitylog.FieldCreatedAt)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list activity by types: %w", err)
	}
	return mapActivityEntries(entries), total, nil
}

func (s *ActivityService) validateTarget(target objectref.Ref) error {
	if !target.Valid() {
		_, err := objectref.Parse(target.ObjectType(), target.ObjectID())
		return fmt.Errorf("invalid activity target: %w", err)
	}
	return s.validateType(target.Type)
}

func (s *ActivityService) validateType(typ objectref.Type) error {
	if !objectref.Known(typ) {
		return fmt.Errorf("%w: %s", objectref.ErrUnknownType, typ)
	}
	if s.objects == nil {
		return fmt.Errorf("activity object directory is required")
	}
	if !s.objects.Supports(typ, objectref.CapActivity) {
		return fmt.Errorf("object type does not support activity: %s", typ)
	}
	return nil
}

func mapActivityEntries(rows []*ent.ActivityLog) []ActivityEntry {
	entries := make([]ActivityEntry, len(rows))
	for i, row := range rows {
		entry := ActivityEntry{
			ID:        row.ID,
			ActorID:   row.ActorID,
			Action:    row.Action,
			Metadata:  row.Metadata,
			CreatedAt: row.CreatedAt,
		}
		if target, err := objectref.Parse(row.ObjectType, row.ObjectID); err == nil {
			entry.Target = target
		} else {
			entry.HistoricalTarget = fmt.Sprintf("%s #%d", row.ObjectType, row.ObjectID)
		}
		entries[i] = entry
	}
	return entries
}

func ActivityRelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		w := int(d.Hours() / (24 * 7))
		if w == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", w)
	case d < 365*24*time.Hour:
		m := int(d.Hours() / (24 * 30))
		if m == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", m)
	default:
		y := int(d.Hours() / (24 * 365))
		if y == 1 {
			return "1 year ago"
		}
		return fmt.Sprintf("%d years ago", y)
	}
}
