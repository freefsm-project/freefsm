package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/activitylog"
	"github.com/MartialM1nd/freefsm/internal/objectref"
)

type ActivityService struct {
	client  *ent.Client
	objects objectref.Directory
}

func NewActivityService(client *ent.Client, objects objectref.Directory) *ActivityService {
	return &ActivityService{client: client, objects: objects}
}

func (s *ActivityService) Record(ctx context.Context, actorID int64, action string, objectType string, objectID int64, metadata map[string]interface{}) error {
	md, err := json.Marshal(metadata)
	if err != nil {
		md = []byte("{}")
	}
	_, err = s.client.ActivityLog.Create().
		SetActorID(actorID).
		SetAction(action).
		SetObjectType(objectType).
		SetObjectID(objectID).
		SetMetadata(string(md)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("record activity: %w", err)
	}
	return nil
}

func (s *ActivityService) LookupEntityName(ctx context.Context, objectType string, objectID int64) string {
	ref, err := s.objects.Parse(objectType, objectID)
	if err != nil {
		return fmt.Sprintf("%s #%d", objectType, objectID)
	}
	name, err := s.objects.DisplayName(ctx, ref)
	if err != nil {
		return fmt.Sprintf("%s #%d", objectType, objectID)
	}
	return name
}

func (s *ActivityService) ListForObject(ctx context.Context, objectType string, objectID int64, limit int) ([]*ent.ActivityLog, error) {
	q := s.client.ActivityLog.Query().
		Where(
			activitylog.ObjectTypeEQ(objectType),
			activitylog.ObjectIDEQ(objectID),
		).
		Order(ent.Desc(activitylog.FieldCreatedAt))
	if limit > 0 {
		q = q.Limit(limit)
	}
	entries, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list activity: %w", err)
	}
	return entries, nil
}

func (s *ActivityService) ListAll(ctx context.Context, offset, limit int, isAdmin bool) ([]*ent.ActivityLog, int, error) {
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
	return entries, total, nil
}

func (s *ActivityService) ListByType(ctx context.Context, objectType string, offset, limit int) ([]*ent.ActivityLog, int, error) {
	q := s.client.ActivityLog.Query().
		Where(activitylog.ObjectTypeEQ(objectType))
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
	return entries, total, nil
}

func (s *ActivityService) ListByTypeAndActions(ctx context.Context, objectType string, actions []string, offset, limit int) ([]*ent.ActivityLog, int, error) {
	q := s.client.ActivityLog.Query().
		Where(
			activitylog.ObjectTypeEQ(objectType),
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
	return entries, total, nil
}

func (s *ActivityService) ListByTypes(ctx context.Context, objectTypes []string, offset, limit int) ([]*ent.ActivityLog, int, error) {
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
	return entries, total, nil
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
