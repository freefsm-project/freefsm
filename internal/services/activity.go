package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/activitylog"
	"github.com/freefsm-project/freefsm/internal/ent/predicate"
	"github.com/freefsm-project/freefsm/internal/ent/user"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

const (
	DefaultActivityPageLimit = 25
	MaxActivityPageLimit     = 100
)

type ActivityDirection string

const (
	ActivityOlder ActivityDirection = "older"
	ActivityNewer ActivityDirection = "newer"
)

type ActivityCursor struct {
	CreatedAt time.Time
	ID        int64
}

type ActivityPageRequest struct {
	Limit     int
	Direction ActivityDirection
	Cursor    *ActivityCursor
}

type ActivityScope interface {
	activityScope()
}

type TenantActivityScope struct {
	IncludeAdminOnly bool
	Actions          []string
}

func (TenantActivityScope) activityScope() {}

type TypeActivityScope struct {
	Types   []objectref.Type
	Actions []string
}

func (TypeActivityScope) activityScope() {}

type ObjectActivityScope struct {
	Refs []objectref.Ref
}

func (ObjectActivityScope) activityScope() {}

type ScheduleActivityScope struct {
	ViewerID   int64
	ViewerRole string
}

func (ScheduleActivityScope) activityScope() {}

type ConversionActivityScope struct {
	EstimateID int64
	ViewerID   int64
	ViewerRole string
}

func (ConversionActivityScope) activityScope() {}

type ActivityListRequest struct {
	CompanyID int64
	Scope     ActivityScope
	Page      ActivityPageRequest
}

type ActivityPage struct {
	Entries     []ActivityEntry
	HasOlder    bool
	HasNewer    bool
	OlderCursor *ActivityCursor
	NewerCursor *ActivityCursor
}

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

// List returns a keyset-paginated activity page for one explicitly scoped company.
func (s *ActivityService) List(ctx context.Context, request ActivityListRequest) (ActivityPage, error) {
	var empty ActivityPage
	limit, direction, err := s.validateListRequest(request)
	if err != nil {
		return empty, err
	}
	if s.client == nil {
		return empty, fmt.Errorf("activity client is required")
	}

	q := s.client.ActivityLog.Query().Where(activitylog.CompanyIDEQ(request.CompanyID))
	q = q.Where(s.scopePredicates(request.Scope)...)
	if request.Page.Cursor != nil {
		q = q.Where(activityCursorPredicate(*request.Page.Cursor, direction))
	}
	if direction == ActivityNewer {
		q = q.Order(ent.Asc(activitylog.FieldCreatedAt), ent.Asc(activitylog.FieldID))
	} else {
		q = q.Order(ent.Desc(activitylog.FieldCreatedAt), ent.Desc(activitylog.FieldID))
	}

	rows, err := q.Limit(limit + 1).All(ctx)
	if err != nil {
		return empty, fmt.Errorf("list activity: %w", err)
	}
	hasExtra := len(rows) > limit
	if hasExtra {
		rows = rows[:limit]
	}
	if direction == ActivityNewer {
		reverseActivityRows(rows)
	}

	page := ActivityPage{Entries: mapActivityEntries(rows)}
	if direction == ActivityNewer {
		page.HasOlder = request.Page.Cursor != nil
		page.HasNewer = hasExtra
	} else {
		page.HasOlder = hasExtra
		page.HasNewer = request.Page.Cursor != nil
	}
	if len(rows) > 0 {
		page.NewerCursor = activityRowCursor(rows[0])
		page.OlderCursor = activityRowCursor(rows[len(rows)-1])
	}
	return page, nil
}

func (s *ActivityService) validateListRequest(request ActivityListRequest) (int, ActivityDirection, error) {
	if err := validateCompanyID(request.CompanyID); err != nil {
		return 0, "", err
	}
	direction := request.Page.Direction
	if direction == "" {
		direction = ActivityOlder
	}
	if direction != ActivityOlder && direction != ActivityNewer {
		return 0, "", fmt.Errorf("invalid activity direction: %q", request.Page.Direction)
	}
	if direction == ActivityNewer && request.Page.Cursor == nil {
		return 0, "", fmt.Errorf("newer activity direction requires a cursor")
	}
	if request.Page.Cursor != nil && (request.Page.Cursor.ID <= 0 || request.Page.Cursor.CreatedAt.IsZero()) {
		return 0, "", fmt.Errorf("activity cursor requires a positive id and created_at")
	}
	limit := request.Page.Limit
	if limit == 0 {
		limit = DefaultActivityPageLimit
	}
	if limit < 1 || limit > MaxActivityPageLimit {
		return 0, "", fmt.Errorf("activity limit must be between 1 and %d", MaxActivityPageLimit)
	}

	switch scope := request.Scope.(type) {
	case TenantActivityScope:
		for _, action := range scope.Actions {
			if strings.TrimSpace(action) == "" {
				return 0, "", fmt.Errorf("activity action must not be empty")
			}
		}
	case TypeActivityScope:
		if len(scope.Types) == 0 {
			return 0, "", fmt.Errorf("activity types must not be empty")
		}
		for _, typ := range scope.Types {
			if err := s.validateType(typ); err != nil {
				return 0, "", err
			}
		}
		for _, action := range scope.Actions {
			if strings.TrimSpace(action) == "" {
				return 0, "", fmt.Errorf("activity action must not be empty")
			}
		}
	case ObjectActivityScope:
		if len(scope.Refs) == 0 {
			return 0, "", fmt.Errorf("activity object refs must not be empty")
		}
		for _, ref := range scope.Refs {
			if err := s.validateTarget(ref); err != nil {
				return 0, "", err
			}
		}
	case ScheduleActivityScope:
		if scope.ViewerID <= 0 {
			return 0, "", fmt.Errorf("schedule activity viewer id must be positive")
		}
		if !validActivityViewerRole(scope.ViewerRole) {
			return 0, "", fmt.Errorf("invalid schedule activity viewer role: %q", scope.ViewerRole)
		}
	case ConversionActivityScope:
		if scope.EstimateID <= 0 {
			return 0, "", fmt.Errorf("conversion activity estimate id must be positive")
		}
		if scope.ViewerID <= 0 {
			return 0, "", fmt.Errorf("conversion activity viewer id must be positive")
		}
		if !validActivityViewerRole(scope.ViewerRole) {
			return 0, "", fmt.Errorf("invalid conversion activity viewer role: %q", scope.ViewerRole)
		}
	default:
		return 0, "", fmt.Errorf("activity scope is required")
	}
	return limit, direction, nil
}

func (s *ActivityService) scopePredicates(scope ActivityScope) []predicate.ActivityLog {
	switch scope := scope.(type) {
	case TenantActivityScope:
		predicates := make([]predicate.ActivityLog, 0, 2)
		if scope.IncludeAdminOnly {
			if len(scope.Actions) > 0 {
				predicates = append(predicates, activitylog.ActionIn(scope.Actions...))
			}
			return predicates
		}
		types := objectref.AdminOnlyTypes()
		values := make([]string, len(types))
		for i, typ := range types {
			values[i] = string(typ)
		}
		predicates = append(predicates, activitylog.ObjectTypeNotIn(values...))
		if len(scope.Actions) > 0 {
			predicates = append(predicates, activitylog.ActionIn(scope.Actions...))
		}
		return predicates
	case TypeActivityScope:
		types := make([]string, len(scope.Types))
		for i, typ := range scope.Types {
			types[i] = string(typ)
		}
		predicates := []predicate.ActivityLog{activitylog.ObjectTypeIn(types...)}
		if len(scope.Actions) > 0 {
			predicates = append(predicates, activitylog.ActionIn(scope.Actions...))
		}
		return predicates
	case ObjectActivityScope:
		return []predicate.ActivityLog{func(selector *entsql.Selector) {
			refs := make([]*entsql.Predicate, 0, len(scope.Refs))
			for _, ref := range scope.Refs {
				refs = append(refs, entsql.And(
					entsql.EQ(selector.C(activitylog.FieldObjectType), ref.ObjectType()),
					entsql.EQ(selector.C(activitylog.FieldObjectID), ref.ObjectID()),
				))
			}
			selector.Where(entsql.Or(refs...))
		}}
	case ScheduleActivityScope:
		predicates := []predicate.ActivityLog{
			activitylog.ObjectTypeEQ(string(objectref.TypeJob)),
			activitylog.ActionIn("scheduled", "rescheduled", "dispatched"),
		}
		role := strings.ToLower(strings.TrimSpace(scope.ViewerRole))
		if role == "tech" || role == "technician" {
			predicates = append(predicates, assignedActiveJobPredicate(scope.ViewerID))
		}
		return predicates
	case ConversionActivityScope:
		return []predicate.ActivityLog{conversionActivityPredicate(scope)}
	default:
		return nil
	}
}

func validActivityViewerRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin", "dispatcher", "tech", "technician":
		return true
	default:
		return false
	}
}

func conversionActivityPredicate(scope ConversionActivityScope) predicate.ActivityLog {
	return func(selector *entsql.Selector) {
		objectType := selector.C(activitylog.FieldObjectType)
		objectID := selector.C(activitylog.FieldObjectID)
		companyID := selector.C(activitylog.FieldCompanyID)
		estimateTarget := entsql.And(
			entsql.EQ(objectType, string(objectref.TypeEstimate)),
			entsql.EQ(objectID, scope.EstimateID),
		)
		invoiceTarget := entsql.And(
			entsql.EQ(objectType, string(objectref.TypeInvoice)),
			conversionInvoiceExists(companyID, objectID, scope.EstimateID, 0),
		)
		role := strings.ToLower(strings.TrimSpace(scope.ViewerRole))
		if role == "tech" || role == "technician" {
			estimateTarget = entsql.And(estimateTarget, conversionDocumentReadableExists(
				"estimates", companyID, objectID, scope.ViewerID,
			))
			invoiceTarget = entsql.And(
				entsql.EQ(objectType, string(objectref.TypeInvoice)),
				conversionInvoiceExists(companyID, objectID, scope.EstimateID, scope.ViewerID),
			)
		}
		selector.Where(entsql.Or(estimateTarget, invoiceTarget))
	}
}

func conversionInvoiceExists(companyColumn, invoiceColumn string, estimateID, viewerID int64) *entsql.Predicate {
	cycles := entsql.Table("estimate_invoice_conversion_cycles").As("activity_cycles")
	query := entsql.Select(cycles.C("invoice_id")).From(cycles)
	conditions := []*entsql.Predicate{
		entsql.ColumnsEQ(cycles.C("company_id"), companyColumn),
		entsql.ColumnsEQ(cycles.C("invoice_id"), invoiceColumn),
		entsql.EQ(cycles.C("estimate_id"), estimateID),
	}
	if viewerID > 0 {
		documents := entsql.Table("invoices").As("activity_invoices")
		jobs := entsql.Table("jobs").As("activity_invoice_jobs")
		assignments := entsql.Table("job_assignments").As("activity_invoice_assignments")
		query = query.
			Join(documents).
			On(cycles.C("invoice_id"), documents.C("id")).
			Join(jobs).
			On(documents.C("job_id"), jobs.C("id")).
			Join(assignments).
			On(jobs.C("id"), assignments.C("job_id"))
		conditions = append(conditions,
			entsql.ColumnsEQ(documents.C("company_id"), companyColumn),
			entsql.IsNull(documents.C("conversion_hidden_at")),
			entsql.ColumnsEQ(jobs.C("company_id"), companyColumn),
			entsql.IsNull(jobs.C("deleted_at")),
			entsql.EQ(assignments.C("user_id"), viewerID),
		)
	}
	return entsql.Exists(query.Where(entsql.And(conditions...)))
}

func conversionDocumentReadableExists(table, companyColumn, documentColumn string, viewerID int64) *entsql.Predicate {
	documents := entsql.Table(table).As("activity_estimates")
	jobs := entsql.Table("jobs").As("activity_estimate_jobs")
	assignments := entsql.Table("job_assignments").As("activity_estimate_assignments")
	query := entsql.Select(documents.C("id")).
		From(documents).
		Join(jobs).
		On(documents.C("job_id"), jobs.C("id")).
		Join(assignments).
		On(jobs.C("id"), assignments.C("job_id")).
		Where(entsql.And(
			entsql.ColumnsEQ(documents.C("id"), documentColumn),
			entsql.ColumnsEQ(documents.C("company_id"), companyColumn),
			entsql.IsNull(documents.C("conversion_hidden_at")),
			entsql.ColumnsEQ(jobs.C("company_id"), companyColumn),
			entsql.IsNull(jobs.C("deleted_at")),
			entsql.EQ(assignments.C("user_id"), viewerID),
		))
	return entsql.Exists(query)
}

func assignedActiveJobPredicate(viewerID int64) predicate.ActivityLog {
	return func(selector *entsql.Selector) {
		jobs := entsql.Table("jobs").As("activity_jobs")
		assignments := entsql.Table("job_assignments").As("activity_assignments")
		assignedJobs := entsql.Select(jobs.C("id")).
			From(jobs).
			Join(assignments).
			On(jobs.C("id"), assignments.C("job_id")).
			Where(entsql.And(
				entsql.ColumnsEQ(jobs.C("company_id"), selector.C(activitylog.FieldCompanyID)),
				entsql.IsNull(jobs.C("deleted_at")),
				entsql.EQ(assignments.C("user_id"), viewerID),
			))
		selector.Where(entsql.In(selector.C(activitylog.FieldObjectID), assignedJobs))
	}
}

func activityCursorPredicate(cursor ActivityCursor, direction ActivityDirection) predicate.ActivityLog {
	return func(selector *entsql.Selector) {
		createdAt := selector.C(activitylog.FieldCreatedAt)
		id := selector.C(activitylog.FieldID)
		if direction == ActivityNewer {
			selector.Where(entsql.Or(
				entsql.GT(createdAt, cursor.CreatedAt),
				entsql.And(entsql.EQ(createdAt, cursor.CreatedAt), entsql.GT(id, cursor.ID)),
			))
			return
		}
		selector.Where(entsql.Or(
			entsql.LT(createdAt, cursor.CreatedAt),
			entsql.And(entsql.EQ(createdAt, cursor.CreatedAt), entsql.LT(id, cursor.ID)),
		))
	}
}

func reverseActivityRows(rows []*ent.ActivityLog) {
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
}

func activityRowCursor(row *ent.ActivityLog) *ActivityCursor {
	return &ActivityCursor{CreatedAt: row.CreatedAt, ID: row.ID}
}

func (s *ActivityService) Record(ctx context.Context, companyID, actorID int64, action string, target objectref.Ref, metadata map[string]interface{}) error {
	if err := validateActivityIDs(companyID, actorID); err != nil {
		return err
	}
	if err := s.validateTarget(target); err != nil {
		return err
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return fmt.Errorf("activity action must not be empty")
	}
	if s.client == nil {
		return fmt.Errorf("activity client is required")
	}
	actorExists, err := s.client.User.Query().Where(user.IDEQ(actorID), user.CompanyIDEQ(companyID)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("validate activity actor: %w", err)
	}
	if !actorExists {
		return fmt.Errorf("activity actor not found for company")
	}

	md, err := json.Marshal(metadata)
	if err != nil {
		md = []byte("{}")
	}
	_, err = s.client.ActivityLog.Create().
		SetCompanyID(companyID).
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

func validateActivityIDs(companyID, actorID int64) error {
	if err := validateCompanyID(companyID); err != nil {
		return err
	}
	if actorID <= 0 {
		return fmt.Errorf("activity actor id must be positive: %d", actorID)
	}
	return nil
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
