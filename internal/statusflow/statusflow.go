// Package statusflow owns status categories, configuration, and record transitions.
// status_workflows remains an internal tenant container until legacy Ent and handler
// consumers are migrated to this package, at which point migration 046+ may remove it.
package statusflow

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ObjectType string
type CategoryKey string

const (
	Job      ObjectType = "job"
	Project  ObjectType = "project"
	Estimate ObjectType = "estimate"
	Invoice  ObjectType = "invoice"

	JobNew               CategoryKey = "job:new"
	JobTravelTime        CategoryKey = "job:travel_time"
	JobInProgress        CategoryKey = "job:in_progress"
	JobPending           CategoryKey = "job:pending"
	JobCompleted         CategoryKey = "job:completed"
	JobCanceled          CategoryKey = "job:canceled"
	ProjectNew           CategoryKey = "project:new"
	ProjectInProgress    CategoryKey = "project:in_progress"
	ProjectPending       CategoryKey = "project:pending"
	ProjectCompleted     CategoryKey = "project:completed"
	ProjectCanceled      CategoryKey = "project:canceled"
	EstimateDraft        CategoryKey = "estimate:draft"
	EstimateEstimate     CategoryKey = "estimate:estimate"
	EstimateSent         CategoryKey = "estimate:sent"
	EstimateAccepted     CategoryKey = "estimate:accepted"
	EstimateRejected     CategoryKey = "estimate:rejected"
	EstimateCompleted    CategoryKey = "estimate:completed"
	InvoiceDraft         CategoryKey = "invoice:draft"
	InvoiceInvoiced      CategoryKey = "invoice:invoiced"
	InvoiceSent          CategoryKey = "invoice:sent"
	InvoicePartiallyPaid CategoryKey = "invoice:partially_paid"
	InvoicePaid          CategoryKey = "invoice:paid"
	InvoiceVoid          CategoryKey = "invoice:void"
)

type Category struct {
	Key            CategoryKey
	ObjectType     ObjectType
	Closed, Manual bool
}

var Categories = []Category{
	{JobNew, Job, false, true}, {JobTravelTime, Job, false, true}, {JobInProgress, Job, false, true}, {JobPending, Job, false, true}, {JobCompleted, Job, true, true}, {JobCanceled, Job, true, true},
	{ProjectNew, Project, false, true}, {ProjectInProgress, Project, false, true}, {ProjectPending, Project, false, true}, {ProjectCompleted, Project, true, true}, {ProjectCanceled, Project, true, true},
	{EstimateDraft, Estimate, false, true}, {EstimateEstimate, Estimate, false, true}, {EstimateSent, Estimate, false, true}, {EstimateAccepted, Estimate, false, true}, {EstimateRejected, Estimate, false, true}, {EstimateCompleted, Estimate, true, true},
	{InvoiceDraft, Invoice, false, true}, {InvoiceInvoiced, Invoice, false, true}, {InvoiceSent, Invoice, false, true}, {InvoicePartiallyPaid, Invoice, false, false}, {InvoicePaid, Invoice, false, false}, {InvoiceVoid, Invoice, true, true},
}

var (
	ErrForbidden            = errors.New("statusflow operation forbidden")
	ErrNotFound             = errors.New("statusflow record not found")
	ErrWrongType            = errors.New("status belongs to the wrong object type")
	ErrReplacementRequired  = errors.New("same-category replacement status is required")
	ErrConfirmationRequired = errors.New("moving an in-use status requires confirmation")
	ErrPaymentDerived       = errors.New("payment-derived invoice status is not manually selectable")
	ErrActiveSettlement     = errors.New("invoice status cannot be changed while settlement is active")
	ErrInvalidTransition    = errors.New("status transition is not valid from the current category")
	ErrInvalidInput         = errors.New("status label, color, or category is invalid")
)

type Actor struct {
	ID, CompanyID int64
	Role          string
}
type Status struct {
	ID, WorkflowID int64
	Name, Color    string
	Category       CategoryKey
	Order          int
	Default        bool
}
type ConfigStatus struct {
	Status
	Usage int
}
type Service struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// Configuration returns the complete workflow, including archived record usage.
// Categories are emitted in the fixed domain order and statuses in their category order.
func (s *Service) Configuration(ctx context.Context, companyID int64, typ ObjectType) ([]ConfigStatus, error) {
	if !validObjectType(typ) {
		return nil, ErrWrongType
	}
	query := fmt.Sprintf(`SELECT s.id,s.workflow_id,s.name,s.color,s.category_key,s.category_order,s.is_category_default,
		(SELECT count(*) FROM %ss o WHERE o.status_id=s.id)
		FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id
		WHERE w.company_id=$1 AND w.object_type=$2`, typ)
	rows, err := s.db.Query(ctx, query, companyID, typ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byCategory := make(map[CategoryKey][]ConfigStatus)
	for rows.Next() {
		var item ConfigStatus
		if err := rows.Scan(&item.ID, &item.WorkflowID, &item.Name, &item.Color, &item.Category, &item.Order, &item.Default, &item.Usage); err != nil {
			return nil, err
		}
		byCategory[item.Category] = append(byCategory[item.Category], item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var result []ConfigStatus
	for _, category := range Categories {
		if category.ObjectType != typ {
			continue
		}
		items := byCategory[category.Key]
		slices.SortFunc(items, func(a, b ConfigStatus) int { return cmp.Compare(a.Order, b.Order) })
		result = append(result, items...)
	}
	return result, nil
}

func DefaultCreationCategory(typ ObjectType) CategoryKey {
	switch typ {
	case Job:
		return JobNew
	case Project:
		return ProjectNew
	case Estimate:
		return EstimateDraft
	case Invoice:
		return InvoiceDraft
	}
	return ""
}

func IsClosed(key CategoryKey) bool {
	return key == JobCompleted || key == JobCanceled || key == ProjectCompleted || key == ProjectCanceled || key == EstimateCompleted || key == InvoiceVoid
}
func CountsAsCompletion(key CategoryKey) bool {
	return key == JobCompleted || key == ProjectCompleted || key == EstimateCompleted
}

func (s *Service) Default(ctx context.Context, companyID int64, typ ObjectType, key CategoryKey) (Status, error) {
	var out Status
	err := s.db.QueryRow(ctx, `SELECT s.id,s.workflow_id,s.name,s.color,s.category_key,s.category_order,s.is_category_default
	 FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id
	 WHERE w.company_id=$1 AND w.object_type=$2 AND s.category_key=$3 AND s.is_category_default`, companyID, typ, key).
		Scan(&out.ID, &out.WorkflowID, &out.Name, &out.Color, &out.Category, &out.Order, &out.Default)
	if errors.Is(err, pgx.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	return out, err
}

func (s *Service) TransitionJob(ctx context.Context, a Actor, id, statusID int64) error {
	return s.transition(ctx, a, Job, id, statusID)
}
func (s *Service) TransitionProject(ctx context.Context, a Actor, id, statusID int64) error {
	return s.transition(ctx, a, Project, id, statusID)
}
func (s *Service) TransitionEstimate(ctx context.Context, a Actor, id, statusID int64) error {
	return s.transition(ctx, a, Estimate, id, statusID)
}
func (s *Service) TransitionInvoice(ctx context.Context, a Actor, id, statusID int64) error {
	return s.transition(ctx, a, Invoice, id, statusID)
}

func (s *Service) FinalizeInvoice(ctx context.Context, a Actor, id int64, invoiceDate time.Time, dueDays int) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		verified, err := authenticate(ctx, tx, a)
		if err != nil {
			return ErrForbidden
		}
		a = verified
		var current CategoryKey
		if err = tx.QueryRow(ctx, `SELECT s.category_key FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE i.id=$1 AND i.company_id=$2 AND i.deleted_at IS NULL FOR UPDATE`, id, a.CompanyID).Scan(&current); err != nil {
			return mapNoRows(err)
		}
		if current != InvoiceDraft {
			return ErrInvalidTransition
		}
		var target int64
		if err = tx.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE w.company_id=$1 AND w.object_type='invoice' AND s.category_key=$2 AND s.is_category_default`, a.CompanyID, InvoiceInvoiced).Scan(&target); err != nil {
			return mapNoRows(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE invoices SET invoice_date=$1,due_date=$2,updated_at=now() WHERE id=$3`, invoiceDate, invoiceDate.AddDate(0, 0, dueDays), id); err != nil {
			return err
		}
		return s.transitionTx(ctx, tx, a, Invoice, id, target)
	})
}

func (s *Service) transition(ctx context.Context, a Actor, typ ObjectType, id, statusID int64) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		return s.transitionTx(ctx, tx, a, typ, id, statusID)
	})
}

func (s *Service) transitionTx(ctx context.Context, tx pgx.Tx, a Actor, typ ObjectType, id, statusID int64) error {
	verified, err := authenticate(ctx, tx, a)
	if err != nil {
		return ErrForbidden
	}
	a = verified
	table := string(typ) + "s"
	var oldID *int64
	var jobID *int64
	query := fmt.Sprintf(`SELECT status_id%s FROM %s WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL FOR UPDATE`, map[ObjectType]string{Job: "", Project: "", Estimate: ",job_id", Invoice: ",job_id"}[typ], table)
	if typ == Estimate || typ == Invoice {
		if err := tx.QueryRow(ctx, query, id, a.CompanyID).Scan(&oldID, &jobID); err != nil {
			return mapNoRows(err)
		}
	} else {
		if err := tx.QueryRow(ctx, query, id, a.CompanyID).Scan(&oldID); err != nil {
			return mapNoRows(err)
		}
	}
	if !authorizedRecord(ctx, tx, a, typ, id, jobID) {
		return ErrForbidden
	}
	var key CategoryKey
	if err := tx.QueryRow(ctx, `SELECT s.category_key FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id
		 WHERE s.id=$1 AND s.company_id=$2 AND w.company_id=$2 AND w.object_type=$3`, statusID, a.CompanyID, typ).Scan(&key); err != nil {
		return ErrWrongType
	}
	if typ == Invoice {
		if key == InvoicePaid || key == InvoicePartiallyPaid {
			return ErrPaymentDerived
		}
		if oldID != nil {
			var oldKey CategoryKey
			if err := tx.QueryRow(ctx, `SELECT category_key FROM statuses WHERE id=$1`, *oldID).Scan(&oldKey); err != nil {
				return err
			}
			if oldKey == InvoiceVoid && key != InvoiceDraft {
				return ErrInvalidTransition
			}
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT invoice_has_active_settlement($1)`, id).Scan(&active); err != nil {
			return err
		}
		if active {
			return ErrActiveSettlement
		}
	}
	if err := allowStatusUpdates(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET status_id=$1,updated_at=now() WHERE id=$2`, table), statusID, id); err != nil {
		return err
	}
	return activity(ctx, tx, a, "status_transitioned", typ, id, map[string]any{"from_status_id": oldID, "to_status_id": statusID, "category_key": key})
}

func authorizedRecord(ctx context.Context, tx pgx.Tx, a Actor, typ ObjectType, id int64, jobID *int64) bool {
	if a.Role == "admin" || a.Role == "dispatcher" {
		return true
	}
	if a.Role != "tech" && a.Role != "technician" {
		return false
	}
	var ok bool
	switch typ {
	case Job:
		_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_assignments WHERE job_id=$1 AND user_id=$2)`, id, a.ID).Scan(&ok)
	case Estimate, Invoice:
		if jobID != nil {
			_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_assignments WHERE job_id=$1 AND user_id=$2)`, *jobID, a.ID).Scan(&ok)
		}
	case Project:
		_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.project_id=$1 AND a.user_id=$2 AND j.deleted_at IS NULL)`, id, a.ID).Scan(&ok)
	}
	return ok
}

type CreateRequest struct {
	Type        ObjectType
	Name, Color string
	Category    CategoryKey
}

func (s *Service) Create(ctx context.Context, a Actor, r CreateRequest) (Status, error) {
	if r.Type == Invoice {
		return Status{}, ErrWrongType
	}
	if strings.TrimSpace(r.Name) == "" || (r.Color != "" && !validColor(r.Color)) || !categoryBelongs(r.Category, r.Type) {
		return Status{}, ErrInvalidInput
	}
	var out Status
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		verified, err := authenticateAdmin(ctx, tx, a)
		if err != nil {
			return err
		}
		a = verified
		var workflow int64
		if err := tx.QueryRow(ctx, `SELECT id FROM status_workflows WHERE company_id=$1 AND object_type=$2 FOR UPDATE`, a.CompanyID, r.Type).Scan(&workflow); err != nil {
			return mapNoRows(err)
		}
		name := strings.TrimSpace(r.Name)
		color := r.Color
		if color == "" {
			color = "#6B7280"
		}
		err = tx.QueryRow(ctx, `INSERT INTO statuses(company_id,workflow_id,name,color,category_key,category_order,is_category_default)
		 VALUES($1,$2,$3,$4,$5,(SELECT coalesce(max(category_order),0)+1 FROM statuses WHERE workflow_id=$2 AND category_key=$5),false)
		 RETURNING id,workflow_id,name,color,category_key,category_order,is_category_default`, a.CompanyID, workflow, name, color, r.Category).
			Scan(&out.ID, &out.WorkflowID, &out.Name, &out.Color, &out.Category, &out.Order, &out.Default)
		if err != nil {
			return err
		}
		return configActivity(ctx, tx, a, "status_created", out.ID, workflow, nil, statusSnapshot(out))
	})
	return out, err
}

func (s *Service) Update(ctx context.Context, a Actor, id int64, name, color string) (Status, error) {
	if strings.TrimSpace(name) == "" || !validColor(color) {
		return Status{}, ErrInvalidInput
	}
	var out Status
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		verified, err := authenticateAdmin(ctx, tx, a)
		if err != nil {
			return err
		}
		a = verified
		var before Status
		if err = tx.QueryRow(ctx, `SELECT s.id,s.workflow_id,s.name,s.color,s.category_key,s.category_order,s.is_category_default FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.id=$1 AND w.company_id=$2 FOR UPDATE`, id, a.CompanyID).Scan(&before.ID, &before.WorkflowID, &before.Name, &before.Color, &before.Category, &before.Order, &before.Default); err != nil {
			return mapNoRows(err)
		}
		err = tx.QueryRow(ctx, `UPDATE statuses s SET name=$1,color=$2 FROM status_workflows w WHERE s.id=$3 AND w.id=s.workflow_id AND w.company_id=$4
		 RETURNING s.id,s.workflow_id,s.name,s.color,s.category_key,s.category_order,s.is_category_default`, strings.TrimSpace(name), color, id, a.CompanyID).
			Scan(&out.ID, &out.WorkflowID, &out.Name, &out.Color, &out.Category, &out.Order, &out.Default)
		if err != nil {
			return mapNoRows(err)
		}
		return configActivity(ctx, tx, a, "status_updated", id, out.WorkflowID, statusSnapshot(before), statusSnapshot(out))
	})
	return out, err
}

type MoveRequest struct {
	StatusID            int64
	Category            CategoryKey
	Order               int
	SourceReplacementID int64
	ConfirmInUse        bool
}

func (s *Service) Move(ctx context.Context, a Actor, r MoveRequest) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		verified, err := authenticateAdmin(ctx, tx, a)
		if err != nil {
			return err
		}
		a = verified
		var workflow int64
		var old CategoryKey
		var def bool
		var typ ObjectType
		var before Status
		if err := tx.QueryRow(ctx, `SELECT s.workflow_id,s.category_key,s.is_category_default,w.object_type,s.name,s.color,s.category_order FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.id=$1 AND w.company_id=$2 FOR UPDATE`, r.StatusID, a.CompanyID).Scan(&workflow, &old, &def, &typ, &before.Name, &before.Color, &before.Order); err != nil {
			return mapNoRows(err)
		}
		before.ID = r.StatusID
		before.WorkflowID = workflow
		before.Category = old
		before.Default = def
		if typ == Invoice {
			return ErrWrongType
		}
		if !categoryBelongs(r.Category, typ) {
			return ErrWrongType
		}
		if _, err = tx.Exec(ctx, `SELECT 1 FROM status_workflows WHERE id=$1 FOR UPDATE`, workflow); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `SELECT 1 FROM statuses WHERE workflow_id=$1 AND category_key IN ($2,$3) ORDER BY category_key,category_order FOR UPDATE`, workflow, old, r.Category); err != nil {
			return err
		}
		crossCategory := old != r.Category
		count, err := usage(ctx, tx, typ, r.StatusID)
		if err != nil {
			return err
		}
		if crossCategory && count > 0 && !r.ConfirmInUse {
			return ErrConfirmationRequired
		}
		if crossCategory && def {
			if r.SourceReplacementID <= 0 {
				return ErrReplacementRequired
			}
			var replacement int64
			if err = tx.QueryRow(ctx, `SELECT id FROM statuses WHERE id=$1 AND id<>$2 AND workflow_id=$3 AND category_key=$4 FOR UPDATE`, r.SourceReplacementID, r.StatusID, workflow, old).Scan(&replacement); err != nil {
				return ErrReplacementRequired
			}
			if _, err = tx.Exec(ctx, `UPDATE statuses SET is_category_default=false WHERE id=$1`, r.StatusID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE statuses SET is_category_default=true WHERE id=$1`, replacement); err != nil {
				return err
			}
		}
		var destinationCount int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM statuses WHERE workflow_id=$1 AND category_key=$2 AND id<>$3`, workflow, r.Category, r.StatusID).Scan(&destinationCount); err != nil {
			return err
		}
		order := r.Order
		if order < 1 {
			order = 1
		}
		if order > destinationCount+1 {
			order = destinationCount + 1
		}
		// Move all affected rows out of the unique order range before assigning dense positions.
		if _, err = tx.Exec(ctx, `UPDATE statuses SET category_order=category_order+1000000 WHERE workflow_id=$1 AND category_key IN ($2,$3)`, workflow, old, r.Category); err != nil {
			return err
		}
		if old != r.Category {
			if _, err = tx.Exec(ctx, `WITH ranked AS (SELECT id,row_number() OVER(ORDER BY category_order,id) n FROM statuses WHERE workflow_id=$1 AND category_key=$2 AND id<>$3) UPDATE statuses s SET category_order=r.n FROM ranked r WHERE s.id=r.id`, workflow, old, r.StatusID); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `WITH ranked AS (SELECT id,row_number() OVER(ORDER BY category_order,id) n FROM statuses WHERE workflow_id=$1 AND category_key=$2 AND id<>$3) UPDATE statuses s SET category_order=CASE WHEN r.n >= $4 THEN r.n+1 ELSE r.n END FROM ranked r WHERE s.id=r.id`, workflow, r.Category, r.StatusID, order); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE statuses SET category_key=$1,category_order=$2,is_category_default=$4 WHERE id=$3`, r.Category, order, r.StatusID, def && !crossCategory); err != nil {
			return err
		}
		var after Status
		if err = tx.QueryRow(ctx, `SELECT id,workflow_id,name,color,category_key,category_order,is_category_default FROM statuses WHERE id=$1`, r.StatusID).Scan(&after.ID, &after.WorkflowID, &after.Name, &after.Color, &after.Category, &after.Order, &after.Default); err != nil {
			return err
		}
		return configActivity(ctx, tx, a, "status_moved", r.StatusID, workflow, statusSnapshot(before), map[string]any{"status": statusSnapshot(after), "in_use": count})
	})
}

func (s *Service) SetDefault(ctx context.Context, a Actor, statusID int64) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		verified, err := authenticateAdmin(ctx, tx, a)
		if err != nil {
			return err
		}
		a = verified
		var workflow int64
		var key CategoryKey
		var typ ObjectType
		if err := tx.QueryRow(ctx, `SELECT s.workflow_id,s.category_key,w.object_type FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.id=$1 AND w.company_id=$2 FOR UPDATE`, statusID, a.CompanyID).Scan(&workflow, &key, &typ); err != nil {
			return mapNoRows(err)
		}
		if typ == Invoice {
			return ErrWrongType
		}
		if _, err := tx.Exec(ctx, `UPDATE statuses SET is_category_default=(id=$1) WHERE workflow_id=$2 AND category_key=$3`, statusID, workflow, key); err != nil {
			return err
		}
		return configActivity(ctx, tx, a, "status_default_changed", statusID, workflow, nil, map[string]any{"category_key": key, "default_status_id": statusID})
	})
}

func (s *Service) Delete(ctx context.Context, a Actor, statusID, replacementID int64) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		verified, err := authenticateAdmin(ctx, tx, a)
		if err != nil {
			return err
		}
		a = verified
		var workflow int64
		var key CategoryKey
		var def bool
		var typ ObjectType
		if err := tx.QueryRow(ctx, `SELECT s.workflow_id,s.category_key,s.is_category_default,w.object_type FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.id=$1 AND w.company_id=$2 FOR UPDATE`, statusID, a.CompanyID).Scan(&workflow, &key, &def, &typ); err != nil {
			return mapNoRows(err)
		}
		if typ == Invoice {
			return ErrWrongType
		}
		if _, err = tx.Exec(ctx, `SELECT 1 FROM status_workflows WHERE id=$1 FOR UPDATE`, workflow); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `SELECT 1 FROM statuses WHERE workflow_id=$1 AND category_key=$2 ORDER BY category_order FOR UPDATE`, workflow, key); err != nil {
			return err
		}
		count, err := usage(ctx, tx, typ, statusID)
		if err != nil {
			return err
		}
		var categoryCount int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM statuses WHERE workflow_id=$1 AND category_key=$2`, workflow, key).Scan(&categoryCount); err != nil {
			return err
		}
		needsReplacement := count > 0 || def || categoryCount == 1
		var replacementDefault bool
		if replacementID > 0 {
			err = tx.QueryRow(ctx, `SELECT is_category_default FROM statuses WHERE id=$1 AND id<>$2 AND workflow_id=$3 AND category_key=$4 FOR UPDATE`, replacementID, statusID, workflow, key).Scan(&replacementDefault)
		}
		if needsReplacement && (replacementID <= 0 || err != nil) {
			return ErrReplacementRequired
		}
		if !needsReplacement {
			replacementID = 0
		}
		table := string(typ) + "s"
		if count > 0 {
			if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET status_id=$1 WHERE status_id=$2`, table), replacementID, statusID); err != nil {
				return err
			}
		}
		if def && !replacementDefault {
			if _, err := tx.Exec(ctx, `UPDATE statuses SET is_category_default=false WHERE id=$1`, statusID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE statuses SET is_category_default=true WHERE id=$1`, replacementID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM statuses WHERE id=$1`, statusID); err != nil {
			return err
		}
		return configActivity(ctx, tx, a, "status_deleted", statusID, workflow, map[string]any{"status_id": statusID, "category_key": key, "was_default": def, "usage": count}, map[string]any{"replacement_status_id": replacementID})
	})
}

func (s *Service) EffectiveInvoiceStatus(ctx context.Context, companyID, invoiceID int64) (Status, error) {
	var out Status
	err := s.db.QueryRow(ctx, `SELECT s.id,s.workflow_id,s.name,s.color,s.category_key,s.category_order,s.is_category_default FROM invoice_effective_status e JOIN statuses s ON s.id=e.status_id WHERE e.company_id=$1 AND e.invoice_id=$2`, companyID, invoiceID).Scan(&out.ID, &out.WorkflowID, &out.Name, &out.Color, &out.Category, &out.Order, &out.Default)
	if errors.Is(err, pgx.ErrNoRows) {
		return Status{}, ErrNotFound
	}
	return out, err
}

func (s *Service) CurrentCategory(ctx context.Context, companyID int64, typ ObjectType, id int64) (CategoryKey, error) {
	var key CategoryKey
	err := s.db.QueryRow(ctx, fmt.Sprintf(`SELECT s.category_key FROM %ss o JOIN statuses s ON s.id=o.status_id WHERE o.id=$1 AND o.company_id=$2`, typ), id, companyID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return key, err
}

func usage(ctx context.Context, tx pgx.Tx, typ ObjectType, statusID int64) (int, error) {
	var n int
	err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %ss WHERE status_id=$1`, typ), statusID).Scan(&n)
	return n, err
}
func activity(ctx context.Context, tx pgx.Tx, a Actor, action string, typ ObjectType, id int64, metadata map[string]any) error {
	b, _ := json.Marshal(metadata)
	_, err := tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,$3,$4,$5,$6)`, a.CompanyID, a.ID, action, typ, id, b)
	return err
}
func configActivity(ctx context.Context, tx pgx.Tx, a Actor, action string, statusID, workflowID int64, before, after any) error {
	b, _ := json.Marshal(map[string]any{"workflow_id": workflowID, "status_id": statusID, "before": before, "after": after})
	_, err := tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,$3,'status',$4,$5)`, a.CompanyID, a.ID, action, statusID, b)
	return err
}
func statusSnapshot(s Status) map[string]any {
	return map[string]any{"id": s.ID, "workflow_id": s.WorkflowID, "name": s.Name, "color": s.Color, "category_key": s.Category, "category_order": s.Order, "is_category_default": s.Default}
}
func categoryBelongs(key CategoryKey, typ ObjectType) bool {
	for _, category := range Categories {
		if category.Key == key && category.ObjectType == typ {
			return true
		}
	}
	return false
}
func validObjectType(typ ObjectType) bool {
	return typ == Job || typ == Project || typ == Estimate || typ == Invoice
}
func validColor(color string) bool {
	if len(color) != 7 || color[0] != '#' {
		return false
	}
	_, err := strconv.ParseUint(color[1:], 16, 24)
	return err == nil
}
func authenticate(ctx context.Context, tx pgx.Tx, a Actor) (Actor, error) {
	if a.ID <= 0 || a.CompanyID <= 0 {
		return Actor{}, ErrForbidden
	}
	var role string
	if err := tx.QueryRow(ctx, `SELECT role FROM users WHERE id=$1 AND company_id=$2 FOR SHARE`, a.ID, a.CompanyID).Scan(&role); err != nil {
		return Actor{}, ErrForbidden
	}
	a.Role = role
	return a, nil
}
func authenticateAdmin(ctx context.Context, tx pgx.Tx, a Actor) (Actor, error) {
	verified, err := authenticate(ctx, tx, a)
	if err != nil || verified.Role != "admin" {
		return Actor{}, ErrForbidden
	}
	return verified, nil
}
func mapNoRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func (s *Service) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	for attempt := 0; attempt < 3; attempt++ {
		tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return err
		}
		err = allowStatusUpdates(ctx, tx)
		if err == nil {
			err = fn(tx)
		}
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if !retryable(err) || attempt == 2 {
			return err
		}
	}
	return nil
}

func allowStatusUpdates(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT set_config('freefsm.status_transition','allowed',true)`)
	return err
}
func retryable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}
