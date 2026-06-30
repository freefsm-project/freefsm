package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/activitylog"
)

var adminObjectTypes = map[string]bool{
	"asset_type":       true,
	"asset_status":     true,
	"company_settings": true,
	"custom_field":     true,
	"job_status":       true,
	"tag":              true,
	"user":             true,
}

type ActivityService struct {
	client *ent.Client
}

func NewActivityService(client *ent.Client) *ActivityService {
	return &ActivityService{client: client}
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
	switch objectType {
	case "customer":
		c, err := s.client.Customer.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("customer #%d", objectID)
		}
		return c.DisplayName
	case "job":
		j, err := s.client.Job.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("job #%d", objectID)
		}
		return j.JobType
	case "project":
		p, err := s.client.Project.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("project #%d", objectID)
		}
		return p.Name
	case "estimate":
		e, err := s.client.Estimate.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("estimate #%d", objectID)
		}
		return e.Title
	case "invoice":
		i, err := s.client.Invoice.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("invoice #%d", objectID)
		}
		return i.Title
	case "asset":
		a, err := s.client.Asset.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("asset #%d", objectID)
		}
		return a.Name
	case "item":
		i, err := s.client.Item.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("item #%d", objectID)
		}
		return i.Name
	case "time_entry":
		te, err := s.client.TimeEntry.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("time entry #%d", objectID)
		}
		cs, _ := s.client.CompanySettings.Query().First(ctx)
		loc := companySettingsLocation(cs)
		clockIn := FormatCompanyDateTime(te.ClockIn, loc, cs)
		if te.ClockOut != nil {
			return fmt.Sprintf("%s — %s", clockIn, FormatCompanyTime(*te.ClockOut, loc, cs))
		}
		return clockIn
	case "asset_type":
		at, err := s.client.AssetType.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("asset type #%d", objectID)
		}
		return at.Name
	case "asset_status":
		as, err := s.client.AssetStatus.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("asset status #%d", objectID)
		}
		return as.Name
	case "job_status":
		st, err := s.client.Status.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("job status #%d", objectID)
		}
		return st.Name
	case "company_settings":
		cs, err := s.client.CompanySettings.Get(ctx, objectID)
		if err != nil {
			return "Company Settings"
		}
		name := cs.BusinessName
		if name == "" {
			name = "Company Settings"
		}
		return name
	case "custom_field":
		cf, err := s.client.CustomFieldDefinition.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("custom field #%d", objectID)
		}
		return cf.Name
	case "tag":
		t, err := s.client.Tag.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("tag #%d", objectID)
		}
		return t.Name
	case "user":
		u, err := s.client.User.Get(ctx, objectID)
		if err != nil {
			return fmt.Sprintf("user #%d", objectID)
		}
		return u.Name
	default:
		return fmt.Sprintf("%s #%d", objectType, objectID)
	}
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
		for t := range adminObjectTypes {
			q = q.Where(activitylog.ObjectTypeNEQ(t))
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
