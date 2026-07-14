// Package conversion owns atomic estimate-to-invoice conversion and reversal.
package conversion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrForbidden           = errors.New("conversion operation forbidden")
	ErrNotFound            = errors.New("document not found")
	ErrArchived            = errors.New("archived document must be restored first")
	ErrSettlement          = errors.New("invoice has financial settlement blockers")
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")
	ErrTransactionConflict = errors.New("conversion transaction conflict; retry the operation")
)

type Actor struct {
	ID, CompanyID int64
	Role          string
}
type Operation struct{ Key uuid.UUID }
type ConvertRequest struct {
	Operation  Operation
	EstimateID int64
}
type RevertRequest struct {
	Operation Operation
	InvoiceID int64
}
type Result struct {
	CycleID                                 uuid.UUID `json:"cycle_id"`
	EstimateID, InvoiceID, InvoiceNumber    int64
	Reverted                                bool
	SourceCustomFields, InvoiceCustomFields json.RawMessage
}
type Blocker string

const (
	BlockerArchived                Blocker = "archived"
	BlockerActivePayment           Blocker = "active_payment_allocation"
	BlockerActiveCreditApplication Blocker = "active_credit_application"
	BlockerUnresolvedPaymentCredit Blocker = "unresolved_payment_credit"
)

type Eligibility struct {
	Allowed  bool
	Active   *Result
	Blockers []Blocker
}
type TimelineEntry struct {
	ID                 int64
	ObjectType, Action string
	ObjectID           int64
	ActorID            int64
	Metadata           json.RawMessage
	CreatedAt          time.Time
}

type Service struct {
	db  *pgxpool.Pool
	now func() time.Time
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db, now: time.Now} }

func (s *Service) ConversionEligibility(ctx context.Context, actor Actor, estimateID int64) (Eligibility, error) {
	var jobID *int64
	var deletedAt, hiddenAt *time.Time
	err := s.db.QueryRow(ctx, `SELECT e.job_id,e.deleted_at,e.conversion_hidden_at FROM estimates e WHERE e.company_id=$1 AND e.id=$2`, actor.CompanyID, estimateID).Scan(&jobID, &deletedAt, &hiddenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Eligibility{}, ErrNotFound
	}
	if err != nil {
		return Eligibility{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Eligibility{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = authorizeDocument(ctx, tx, actor, jobID); err != nil {
		return Eligibility{}, err
	}
	e := Eligibility{Allowed: deletedAt == nil && hiddenAt == nil}
	var r Result
	err = tx.QueryRow(ctx, `SELECT id,estimate_id,invoice_id,invoice_number FROM estimate_invoice_conversion_cycles WHERE company_id=$1 AND estimate_id=$2 AND reverted_at IS NULL`, actor.CompanyID, estimateID).Scan(&r.CycleID, &r.EstimateID, &r.InvoiceID, &r.InvoiceNumber)
	if err == nil {
		e.Active = &r
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Eligibility{}, err
	}
	return e, nil
}

func (s *Service) RevertEligibility(ctx context.Context, actor Actor, invoiceID int64) (Eligibility, error) {
	var r Result
	var jobID *int64
	var archived *time.Time
	err := s.db.QueryRow(ctx, `SELECT c.id,c.estimate_id,c.invoice_id,c.invoice_number,i.job_id,i.deleted_at FROM estimate_invoice_conversion_cycles c JOIN invoices i ON i.id=c.invoice_id WHERE c.company_id=$1 AND c.invoice_id=$2 AND c.reverted_at IS NULL`, actor.CompanyID, invoiceID).Scan(&r.CycleID, &r.EstimateID, &r.InvoiceID, &r.InvoiceNumber, &jobID, &archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return Eligibility{}, ErrNotFound
	}
	if err != nil {
		return Eligibility{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Eligibility{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = authorizeDocument(ctx, tx, actor, jobID); err != nil {
		return Eligibility{}, err
	}
	b, err := settlement.RevertBlockers(ctx, tx, actor.CompanyID, invoiceID)
	if err != nil {
		return Eligibility{}, err
	}
	e := Eligibility{Active: &r}
	if archived != nil {
		e.Blockers = append(e.Blockers, BlockerArchived)
	}
	for _, v := range b {
		e.Blockers = append(e.Blockers, Blocker(v))
	}
	e.Allowed = len(e.Blockers) == 0
	return e, nil
}

// ActivityEstimateID resolves and authorizes the root estimate for a conversion activity request.
func (s *Service) ActivityEstimateID(ctx context.Context, actor Actor, document objectref.Ref) (int64, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var estimateID int64
	var jobID *int64
	switch document.Type {
	case objectref.TypeEstimate:
		err = tx.QueryRow(ctx, `SELECT id,job_id FROM estimates WHERE company_id=$1 AND id=$2`, actor.CompanyID, document.ID).Scan(&estimateID, &jobID)
	case objectref.TypeInvoice:
		err = tx.QueryRow(ctx, `SELECT c.estimate_id,i.job_id FROM estimate_invoice_conversion_cycles c JOIN invoices i ON i.id=c.invoice_id AND i.company_id=c.company_id WHERE c.company_id=$1 AND c.invoice_id=$2`, actor.CompanyID, document.ID).Scan(&estimateID, &jobID)
	default:
		return 0, ErrNotFound
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if _, err = authorizeDocument(ctx, tx, actor, jobID); err != nil {
		return 0, err
	}
	return estimateID, nil
}

// Timeline preserves original activity targets and assembles all documents in the estimate's conversion history.
func (s *Service) Timeline(ctx context.Context, actor Actor, estimateID int64) ([]TimelineEntry, error) {
	var jobID *int64
	if err := s.db.QueryRow(ctx, `SELECT job_id FROM estimates WHERE company_id=$1 AND id=$2`, actor.CompanyID, estimateID).Scan(&jobID); err != nil {
		return nil, ErrNotFound
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = authorizeDocument(ctx, tx, actor, jobID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT a.id,a.object_type,a.object_id,a.action,a.actor_id,a.metadata,a.created_at FROM activity_logs a WHERE a.company_id=$1 AND ((a.object_type='estimate' AND a.object_id=$2) OR (a.object_type='invoice' AND a.object_id IN (SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE company_id=$1 AND estimate_id=$2))) ORDER BY a.created_at,a.id`, actor.CompanyID, estimateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimelineEntry
	for rows.Next() {
		var e TimelineEntry
		if err = rows.Scan(&e.ID, &e.ObjectType, &e.ObjectID, &e.Action, &e.ActorID, &e.Metadata, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) Convert(ctx context.Context, actor Actor, req ConvertRequest) (Result, error) {
	return s.transact(ctx, actor, "convert", req.Operation, req, func(tx pgx.Tx, actorName string) (Result, error) {
		var customerID int64
		var jobID *int64
		var title, notes, taxRate string
		var lineItems, customFields, snapshot []byte
		var deletedAt, hiddenAt *time.Time
		err := tx.QueryRow(ctx, `SELECT e.customer_id,e.job_id,e.title,e.notes,e.tax_rate::text,e.line_items,e.custom_fields,e.deleted_at,e.conversion_hidden_at,to_jsonb(e) FROM estimates e WHERE e.company_id=$1 AND e.id=$2 FOR UPDATE OF e`, actor.CompanyID, req.EstimateID).Scan(&customerID, &jobID, &title, &notes, &taxRate, &lineItems, &customFields, &deletedAt, &hiddenAt, &snapshot)
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrNotFound
		}
		if err != nil {
			return Result{}, err
		}
		if deletedAt != nil {
			return Result{}, ErrArchived
		}
		if hiddenAt != nil {
			return Result{}, ErrNotFound
		}
		if _, err = authorizeDocument(ctx, tx, actor, jobID); err != nil {
			return Result{}, err
		}
		items, err := services.DecodeLineItems(string(lineItems))
		if err != nil {
			return Result{}, err
		}
		totals, err := services.CalculateTotals(items, taxRate)
		if err != nil {
			return Result{}, err
		}
		if totals.Total.MinorUnits() < 0 {
			return Result{}, services.ErrNegativeInvoiceTotal
		}
		settlementState := "unpaid"
		if totals.Total.MinorUnits() == 0 {
			settlementState = "paid"
		}
		invoiceCustom, err := mapCustomFields(ctx, tx, actor.CompanyID, customFields, "estimate", "invoice", false)
		if err != nil {
			return Result{}, err
		}

		var settingsID, number int64
		if err = tx.QueryRow(ctx, `SELECT id,next_invoice_number FROM company_settings WHERE company_id=$1 FOR UPDATE`, actor.CompanyID).Scan(&settingsID, &number); err != nil {
			return Result{}, err
		}
		var statusID int64
		if err = tx.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.company_id=$1 AND w.company_id=$1 AND w.object_type='invoice' AND s.category_key='invoice:draft' AND s.is_category_default`, actor.CompanyID).Scan(&statusID); err != nil {
			return Result{}, fmt.Errorf("resolve invoice draft status: %w", err)
		}
		now := s.now()
		var invoiceID int64
		err = tx.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,job_id,estimate_id,status_id,invoice_number,title,notes,invoice_date,due_date,tax_rate,line_items,custom_fields,settlement_state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`, actor.CompanyID, customerID, jobID, req.EstimateID, statusID, number, title, notes, now, now.AddDate(0, 0, 30), taxRate, lineItems, invoiceCustom, settlementState).Scan(&invoiceID)
		if err != nil {
			return Result{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE company_settings SET next_invoice_number=$1 WHERE id=$2`, number+1, settingsID); err != nil {
			return Result{}, err
		}
		cycleID := uuid.New()
		if _, err = tx.Exec(ctx, `INSERT INTO estimate_invoice_conversion_cycles(id,company_id,estimate_id,invoice_id,invoice_number,source_snapshot,converted_by,converted_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, cycleID, actor.CompanyID, req.EstimateID, invoiceID, number, snapshot, actor.ID, now); err != nil {
			return Result{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE estimates SET conversion_hidden_at=$1,updated_at=$1 WHERE id=$2`, now, req.EstimateID); err != nil {
			return Result{}, err
		}
		if err = transferRelations(ctx, tx, actor.CompanyID, "estimate", req.EstimateID, "invoice", invoiceID); err != nil {
			return Result{}, err
		}
		if err = logActivity(ctx, tx, actor, actorName, title, "estimate", req.EstimateID, "estimate_converted", cycleID, invoiceID); err != nil {
			return Result{}, err
		}
		return Result{CycleID: cycleID, EstimateID: req.EstimateID, InvoiceID: invoiceID, InvoiceNumber: number, SourceCustomFields: customFields, InvoiceCustomFields: invoiceCustom}, nil
	})
}

func (s *Service) Revert(ctx context.Context, actor Actor, req RevertRequest) (Result, error) {
	return s.transact(ctx, actor, "revert", req.Operation, req, func(tx pgx.Tx, actorName string) (Result, error) {
		var r Result
		var jobID *int64
		var deletedAt, hiddenAt *time.Time
		var customerID int64
		var title, notes, taxRate string
		var lineItems, invoiceCustom, sourceCustom []byte
		err := tx.QueryRow(ctx, `SELECT c.id,c.estimate_id,c.invoice_id,c.invoice_number,i.customer_id,i.job_id,i.title,i.notes,i.tax_rate::text,i.line_items,i.custom_fields,c.source_snapshot->'custom_fields',i.deleted_at,i.conversion_hidden_at FROM estimate_invoice_conversion_cycles c JOIN invoices i ON i.id=c.invoice_id AND i.company_id=c.company_id WHERE c.company_id=$1 AND c.invoice_id=$2 AND c.reverted_at IS NULL FOR UPDATE OF c,i`, actor.CompanyID, req.InvoiceID).Scan(&r.CycleID, &r.EstimateID, &r.InvoiceID, &r.InvoiceNumber, &customerID, &jobID, &title, &notes, &taxRate, &lineItems, &invoiceCustom, &sourceCustom, &deletedAt, &hiddenAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrNotFound
		}
		if err != nil {
			return Result{}, err
		}
		if deletedAt != nil {
			return Result{}, ErrArchived
		}
		if hiddenAt != nil {
			return Result{}, ErrNotFound
		}
		if _, err = authorizeDocument(ctx, tx, actor, jobID); err != nil {
			return Result{}, err
		}
		blockers, err := settlement.RevertBlockers(ctx, tx, actor.CompanyID, r.InvoiceID)
		if err != nil {
			return Result{}, err
		}
		if len(blockers) > 0 {
			return Result{}, fmt.Errorf("%w: %v", ErrSettlement, blockers)
		}
		var draftStatus int64
		if err = tx.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.company_id=$1 AND w.company_id=$1 AND w.object_type='estimate' AND s.category_key='estimate:draft' AND s.is_category_default`, actor.CompanyID).Scan(&draftStatus); err != nil {
			return Result{}, fmt.Errorf("resolve estimate draft status: %w", err)
		}
		now := s.now()
		if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.conversion_revert_cycle',$1,true)`, r.CycleID.String()); err != nil {
			return Result{}, err
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.status_transition','allowed',true)`); err != nil {
			return Result{}, err
		}
		mergedCustom, err := mergeRevertCustomFields(ctx, tx, actor.CompanyID, sourceCustom, invoiceCustom)
		if err != nil {
			return Result{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE estimate_invoice_conversion_cycles SET reverted_by=$1,reverted_at=$2 WHERE id=$3`, actor.ID, now, r.CycleID); err != nil {
			return Result{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE invoices SET conversion_hidden_at=$1,updated_at=$1 WHERE id=$2`, now, r.InvoiceID); err != nil {
			return Result{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE estimates SET customer_id=$1,job_id=$2,status_id=$3,title=$4,notes=$5,tax_rate=$6,line_items=$7,custom_fields=$8,deleted_at=NULL,conversion_hidden_at=NULL,updated_at=$9 WHERE id=$10`, customerID, jobID, draftStatus, title, notes, taxRate, lineItems, mergedCustom, now, r.EstimateID); err != nil {
			return Result{}, err
		}
		if err = transferRelations(ctx, tx, actor.CompanyID, "invoice", r.InvoiceID, "estimate", r.EstimateID); err != nil {
			return Result{}, err
		}
		if err = logActivity(ctx, tx, actor, actorName, title, "invoice", r.InvoiceID, "invoice_conversion_reverted", r.CycleID, r.EstimateID); err != nil {
			return Result{}, err
		}
		r.Reverted = true
		r.SourceCustomFields = sourceCustom
		r.InvoiceCustomFields = invoiceCustom
		return r, nil
	})
}

func authorizeDocument(ctx context.Context, tx pgx.Tx, a Actor, jobID *int64) (string, error) {
	if a.ID <= 0 || a.CompanyID <= 0 {
		return "", ErrForbidden
	}
	var role, name string
	if err := tx.QueryRow(ctx, `SELECT role,name FROM users WHERE id=$1 AND company_id=$2`, a.ID, a.CompanyID).Scan(&role, &name); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	} else if err != nil {
		return "", err
	}
	if role != a.Role {
		return "", ErrForbidden
	}
	if role == "admin" || role == "dispatcher" {
		return name, nil
	}
	if role != "tech" && role != "technician" || jobID == nil {
		return "", ErrForbidden
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_assignments a JOIN jobs j ON j.id=a.job_id WHERE a.job_id=$1 AND a.user_id=$2 AND j.company_id=$3 AND j.deleted_at IS NULL)`, *jobID, a.ID, a.CompanyID).Scan(&ok)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrForbidden
	}
	return name, nil
}

func authorizeRequest(ctx context.Context, tx pgx.Tx, actor Actor, request any) (string, error) {
	var jobID *int64
	var err error
	switch req := request.(type) {
	case ConvertRequest:
		err = tx.QueryRow(ctx, `SELECT job_id FROM estimates WHERE company_id=$1 AND id=$2`, actor.CompanyID, req.EstimateID).Scan(&jobID)
	case RevertRequest:
		err = tx.QueryRow(ctx, `SELECT job_id FROM invoices WHERE company_id=$1 AND id=$2`, actor.CompanyID, req.InvoiceID).Scan(&jobID)
	default:
		return "", ErrForbidden
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return authorizeDocument(ctx, tx, actor, jobID)
}

type customFieldValue struct {
	DefinitionID int64           `json:"definition_id"`
	Value        json.RawMessage `json:"value"`
}

func decodeCustomFields(encoded []byte) ([]customFieldValue, error) {
	var values []customFieldValue
	if err := json.Unmarshal(encoded, &values); err != nil || values == nil {
		return nil, errors.New("custom fields must be a JSON array")
	}
	seen := make(map[int64]bool, len(values))
	for _, value := range values {
		if value.DefinitionID <= 0 || len(value.Value) == 0 || seen[value.DefinitionID] {
			return nil, errors.New("custom fields contain an invalid or duplicate definition")
		}
		seen[value.DefinitionID] = true
	}
	return values, nil
}

func customFieldPairs(ctx context.Context, tx pgx.Tx, companyID int64) (map[int64]int64, error) {
	rows, err := tx.Query(ctx, `SELECT e.id,i.id FROM custom_field_definitions e JOIN custom_field_definitions i ON i.company_id=e.company_id AND i.conversion_key=e.conversion_key AND i.object_type='invoice' WHERE e.company_id=$1 AND e.object_type='estimate' AND e.conversion_key IS NOT NULL`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pairs := make(map[int64]int64)
	for rows.Next() {
		var estimateID, invoiceID int64
		if err = rows.Scan(&estimateID, &invoiceID); err != nil {
			return nil, err
		}
		pairs[estimateID] = invoiceID
	}
	return pairs, rows.Err()
}

func mapCustomFields(ctx context.Context, tx pgx.Tx, companyID int64, encoded []byte, from, to string, keepUnpaired bool) ([]byte, error) {
	values, err := decodeCustomFields(encoded)
	if err != nil {
		return nil, err
	}
	pairs, err := customFieldPairs(ctx, tx, companyID)
	if err != nil {
		return nil, err
	}
	if from == "invoice" {
		inverse := make(map[int64]int64, len(pairs))
		for e, i := range pairs {
			inverse[i] = e
		}
		pairs = inverse
	}
	out := make([]customFieldValue, 0, len(values))
	for _, value := range values {
		if target, ok := pairs[value.DefinitionID]; ok {
			value.DefinitionID = target
			out = append(out, value)
		} else if keepUnpaired {
			out = append(out, value)
		}
	}
	return json.Marshal(out)
}

func mergeRevertCustomFields(ctx context.Context, tx pgx.Tx, companyID int64, source, currentInvoice []byte) ([]byte, error) {
	original, err := decodeCustomFields(source)
	if err != nil {
		return nil, err
	}
	mapped, err := mapCustomFields(ctx, tx, companyID, currentInvoice, "invoice", "estimate", false)
	if err != nil {
		return nil, err
	}
	current, err := decodeCustomFields(mapped)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]customFieldValue, len(original))
	order := make([]int64, 0, len(original))
	for _, v := range original {
		byID[v.DefinitionID] = v
		order = append(order, v.DefinitionID)
	}
	for _, v := range current {
		if _, ok := byID[v.DefinitionID]; !ok {
			order = append(order, v.DefinitionID)
		}
		byID[v.DefinitionID] = v
	}
	out := make([]customFieldValue, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return json.Marshal(out)
}

func transferRelations(ctx context.Context, tx pgx.Tx, companyID int64, from string, fromID int64, to string, toID int64) error {
	for _, table := range []string{"files", "tag_links", "comments"} {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET object_type=$1,object_id=$2 WHERE company_id=$3 AND object_type=$4 AND object_id=$5`, table), to, toID, companyID, from, fromID); err != nil {
			return err
		}
	}
	return nil
}
func logActivity(ctx context.Context, tx pgx.Tx, a Actor, actorName, entityName, typ string, id int64, action string, cycle uuid.UUID, other int64) error {
	direction := "estimate_to_invoice"
	estimateID, invoiceID := id, other
	if typ == "invoice" {
		direction = "invoice_to_estimate"
		estimateID, invoiceID = other, id
	}
	metadata, _ := json.Marshal(map[string]any{"conversion_cycle_id": cycle, "estimate_id": estimateID, "invoice_id": invoiceID, "direction": direction, "actor_name": actorName, "entity_name": entityName})
	_, err := tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,$3,$4,$5,$6)`, a.CompanyID, a.ID, action, typ, id, metadata)
	return err
}
func (s *Service) transact(ctx context.Context, a Actor, kind string, op Operation, request any, fn func(pgx.Tx, string) (Result, error)) (Result, error) {
	if op.Key == uuid.Nil {
		return Result{}, errors.New("idempotency UUID is required")
	}
	b, _ := json.Marshal(request)
	sum := sha256.Sum256(b)
	fingerprint := hex.EncodeToString(sum[:])
	const maxTransactionAttempts = 10
	for attempt := 0; attempt < maxTransactionAttempts; attempt++ {
		r, err := s.transactOnce(ctx, a, kind, op, request, fingerprint, fn)
		if !retryableTransactionError(err) {
			return r, err
		}
		if attempt+1 < maxTransactionAttempts {
			timer := time.NewTimer(time.Duration(attempt+1) * 5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Result{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return Result{}, ErrTransactionConflict
}

func (s *Service) transactOnce(ctx context.Context, a Actor, kind string, op Operation, request any, fingerprint string, fn func(pgx.Tx, string) (Result, error)) (Result, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx)
	actorName, err := authorizeRequest(ctx, tx, a, request)
	if err != nil {
		return Result{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("%d:%s:%s", a.CompanyID, kind, op.Key)); err != nil {
		return Result{}, err
	}
	var old string
	var originalActor int64
	var resultJSON []byte
	err = tx.QueryRow(ctx, `SELECT actor_id,request_fingerprint,result FROM estimate_invoice_conversion_operations WHERE company_id=$1 AND operation=$2 AND idempotency_key=$3 FOR UPDATE`, a.CompanyID, kind, op.Key).Scan(&originalActor, &old, &resultJSON)
	if err == nil {
		if old != fingerprint {
			return Result{}, ErrIdempotencyConflict
		}
		var r Result
		if err = json.Unmarshal(resultJSON, &r); err != nil {
			return Result{}, err
		}
		return r, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO estimate_invoice_conversion_operations(company_id,operation,idempotency_key,actor_id,request_fingerprint) VALUES($1,$2,$3,$4,$5)`, a.CompanyID, kind, op.Key, a.ID, fingerprint); err != nil {
		return Result{}, err
	}
	r, err := fn(tx, actorName)
	if err != nil {
		return Result{}, err
	}
	resultJSON, _ = json.Marshal(r)
	if _, err = tx.Exec(ctx, `UPDATE estimate_invoice_conversion_operations SET result=$1 WHERE company_id=$2 AND operation=$3 AND idempotency_key=$4`, resultJSON, a.CompanyID, kind, op.Key); err != nil {
		return Result{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return r, nil
}

func retryableTransactionError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}
