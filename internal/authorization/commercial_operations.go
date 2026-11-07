package authorization

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrCommercialInput = errors.New("invalid commercial document")
var ErrConversionCycle = errors.New("reviewed conversion cycle is no longer active")

type CommercialIntent struct {
	OperationID string `json:"operation_id"`
	ClientID    string `json:"client_id,omitempty"`
	JobID       int64  `json:"job_id"`
	// Omission must retain historical canonical intent bytes for receipt replay.
	CustomerID int64      `json:"customer_id,omitempty"`
	Revision   string     `json:"revision,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
}

// Receipts deliberately contain only effect identity and disposition. Replaying
// after scope loss does not disclose a previously visible commercial document.
type CommercialReceipt struct {
	CycleID      string `json:"cycle_id,omitempty"`
	DocumentType string `json:"document_type,omitempty"`
	OperationID  string `json:"operation_id"`
	ID           int64  `json:"id,omitempty"`
	Number       string `json:"number,omitempty"`
	Revision     string `json:"revision,omitempty"`
	Outcome      string `json:"outcome"`
}

type commercialOperationKey struct{}

func commercialReceiptClient(a Actor) string {
	if a.Channel == "web" {
		return "web"
	}
	return a.SessionHash
}

func commercialAuditDetail(ctx context.Context, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, detail any) (any, error) {
	in, ok := ctx.Value(commercialOperationKey{}).(CommercialIntent)
	if !ok {
		return detail, nil
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return nil, err
	}
	values := map[string]any{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if values == nil {
		values = map[string]any{}
	}
	var accepted time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&accepted); err != nil {
		return nil, err
	}
	values["operation_id"], values["accepted_at"], values["occurred_at"], values["occurrence_source"] = in.OperationID, accepted, accepted, "server"
	if in.OccurredAt != nil {
		values["occurred_at"], values["occurrence_source"] = in.OccurredAt, "client"
	}
	return values, nil
}

func commercialReviewed(ctx context.Context) bool {
	_, ok := ctx.Value(commercialOperationKey{}).(CommercialIntent)
	return ok
}

func (s *Service) ReadCommercial(ctx context.Context, a Actor, kind string, id int64) (map[string]any, error) {
	var state map[string]any
	if kind != "estimate" && kind != "invoice" {
		return nil, ErrNotFound
	}
	err := s.WithinParent(ctx, a, kind, id, "", func(tx pgx.Tx, p Principal) error {
		var err error
		state, err = readCommercial(ctx, tx, p, a, kind, id)
		return err
	})
	return state, err
}

// readCommercial is shared by direct reads and durable repeatable-read snapshots.
// Its caller owns the transaction and has already authorized this document.
func readCommercial(ctx context.Context, tx pgx.Tx, p Principal, a Actor, kind string, id int64) (map[string]any, error) {
	var state map[string]any
	err := func() error {
		var raw []byte
		var job int64
		err := tx.QueryRow(ctx, `SELECT coalesce(job_id,0),jsonb_build_object('id',id,'number',number_label,'job_id',job_id,'title',title,'notes',notes,'terms',terms,'custom_fields',custom_fields,'status_id',status_id) || CASE WHEN $2='invoice' THEN jsonb_build_object('invoice_date',to_jsonb(d)->'invoice_date','due_date',CASE WHEN to_jsonb(d)->>'due_date' LIKE '0001-01-01%' THEN NULL ELSE to_jsonb(d)->'due_date' END) ELSE jsonb_build_object('valid_through',to_jsonb(d)->'valid_through') END FROM `+kind+`s d WHERE id=$1`, id, kind).Scan(&job, &raw)
		if err != nil {
			return err
		}
		if job == 0 {
			return ErrNotFound
		}
		// The retained anchor grants no Job context, and does not add a Job
		// View prerequisite to independent All Records document authority.
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&state); err != nil {
			return err
		}
		// Retained documents can have an explicitly unknown manual status.
		// Reading must preserve that absence and expose the revision/choices for
		// a deliberate repair, rather than fabricating a Draft or hiding the row.
		var manual *string
		if err := tx.QueryRow(ctx, `SELECT s.category_key FROM `+kind+`s d LEFT JOIN statuses s ON s.id=d.status_id WHERE d.id=$1`, id).Scan(&manual); err != nil {
			return err
		}
		state["manual_category"] = manual
		state["effective_category"] = manual
		state["effective_status_id"] = state["status_id"]
		state["settlement_locked"] = false
		var workflow json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('id',s.id,'name',s.name,'color',s.color,'category',s.category_key,'meaning_revision',s.meaning_revision::text) ORDER BY s.category_key,s.category_order,s.id),'[]'::jsonb) FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE w.company_id=$1 AND w.object_type=$2 AND s.category_key NOT IN ('invoice:paid','invoice:partially_paid')`, p.CompanyID, kind).Scan(&workflow); err != nil {
			return err
		}
		state["workflow"] = workflow
		hasActiveCycle := false
		if kind == "invoice" {
			var cycle *string
			if err := tx.QueryRow(ctx, `SELECT (SELECT id::text FROM estimate_invoice_conversion_cycles WHERE invoice_id=$1 AND reverted_at IS NULL)`, id).Scan(&cycle); err != nil {
				return err
			}
			state["cycle_id"] = cycle
			hasActiveCycle = cycle != nil
			var effective *string
			var effectiveID *int64
			var locked bool
			if err := tx.QueryRow(ctx, `SELECT s.id,s.category_key,invoice_has_active_settlement(i.id) FROM invoices i LEFT JOIN invoice_effective_status e ON e.invoice_id=i.id LEFT JOIN statuses s ON s.id=e.status_id WHERE i.id=$1`, id).Scan(&effectiveID, &effective, &locked); err != nil {
				return err
			}
			state["effective_category"], state["settlement_locked"] = effective, locked
			state["effective_status_id"] = effectiveID
		}
		assigned, err := assignedJobAccess(ctx, tx, p.UserID, job, true)
		if err != nil {
			return err
		}
		var active bool
		var conversionActive bool
		var customer int64
		if err := tx.QueryRow(ctx, `SELECT d.customer_id,d.deleted_at IS NULL AND j.deleted_at IS NULL AND c.deleted_at IS NULL,d.deleted_at IS NULL AND c.deleted_at IS NULL AND j.customer_id=d.customer_id FROM `+kind+`s d JOIN jobs j ON j.id=d.job_id JOIN customers c ON c.id=d.customer_id WHERE d.id=$1`, id).Scan(&customer, &active, &conversionActive); err != nil {
			return err
		}
		state["customer_id"] = customer
		eligible := active && state["settlement_locked"] == false
		allows := func(permission string) bool {
			return p.Allows(permission, All) || assigned && p.Allows(permission, Assigned)
		}
		draft := manual != nil && *manual == "invoice:draft"
		// Discovery is advisory; mutation admission and the owning workflow and
		// settlement services still enforce all current invariants at commit.
		state["actions"] = map[string]bool{
			"edit":     eligible && allows(kind+".edit"),
			"status":   eligible && allows(kind+".status") && !draft,
			"finalize": eligible && kind == "invoice" && draft && allows("invoice.finalize"),
			// Conversion alone may retain an archived Job under independent
			// All Records authority. assigned excludes archived Jobs.
			"convert":    kind == "estimate" && conversionActive && allows("estimate.convert") && allows("invoice.create"),
			"revert":     kind == "invoice" && eligible && hasActiveCycle && allows("invoice.revert"),
			"provenance": active && (kind == "estimate" || hasActiveCycle) && allows(kind+".provenance.view"),
		}
		if p.Allows("pricing.view", All) || assigned && p.Allows("pricing.view", Assigned) {
			var lines json.RawMessage
			var tax string
			var financial string
			if err := tx.QueryRow(ctx, `SELECT line_items,tax_rate,financial FROM `+kind+`s WHERE id=$1`, id).Scan(&lines, &tax, &financial); err != nil {
				return err
			}
			state["line_items"], state["tax_rate"] = lines, tax
			if financial != "" {
				state["financial"] = json.RawMessage(financial)
			}
		}
		revision, err := saveaction.Revision(ctx, tx, a.UserID, fmt.Sprintf("/%ss/%d", kind, id))
		state["revision"] = revision
		return err
	}()
	return state, err
}

func (s *Service) CommercialReceipt(ctx context.Context, a Actor, key string) (CommercialReceipt, error) {
	var receipt CommercialReceipt
	if _, err := uuid.Parse(key); err != nil {
		return receipt, ErrNotFound
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT receipt FROM commercial_operations WHERE user_id=$1 AND client_hash=$2 AND operation_id=$3`, a.UserID, commercialReceiptClient(a), key).Scan(&raw); err != nil {
			if err == pgx.ErrNoRows {
				var legacyErr error
				receipt, legacyErr = installedConversionReceipt(ctx, tx, p, key)
				return legacyErr
			}
			return err
		}
		return json.Unmarshal(raw, &receipt)
	})
	return receipt, err
}

// CommercialOperation owns identity, stale-intent rejection and the receipt.
// The callback uses this transaction for all existing details/Item/field logic.
func (s *Service) CommercialOperation(ctx context.Context, a Actor, kind string, id int64, in CommercialIntent, intent []byte, legacyIntent func() ([]byte, error), write func(context.Context, pgx.Tx, int64) (int64, error)) (CommercialReceipt, error) {
	return s.commercialOperation(ctx, a, kind, id, kind+".edit", in, intent, legacyIntent, func(ctx context.Context, tx pgx.Tx, customer int64, _ Principal) (int64, error) {
		return write(ctx, tx, customer)
	}, nil)
}

// CommercialWorkflowOperation shares commercial admission and immutable receipts
// with details saves, but checks the independently granted workflow action.
func (s *Service) CommercialWorkflowOperation(ctx context.Context, a Actor, kind string, id int64, finalize bool, in CommercialIntent, intent []byte, write func(context.Context, pgx.Tx, Principal) error) (CommercialReceipt, error) {
	permission := kind + ".status"
	if finalize {
		if kind != "invoice" {
			return CommercialReceipt{}, ErrCommercialInput
		}
		permission = "invoice.finalize"
	}
	if id <= 0 {
		return CommercialReceipt{}, ErrCommercialInput
	}
	return s.commercialOperation(ctx, a, kind, id, permission, in, intent, nil, func(ctx context.Context, tx pgx.Tx, _ int64, p Principal) (int64, error) {
		return id, write(ctx, tx, p)
	}, nil)
}

// CommercialTransition uses the same admission and receipt transaction as edits,
// but owns a domain-specific authorization/precondition check and changes the
// identity of the current document. It never creates a second receipt protocol.
type CommercialTransition struct {
	Check func(context.Context, pgx.Tx) error
	Write func(context.Context, pgx.Tx) (CommercialReceipt, error)
}

func (s *Service) CommercialTransitionOperation(ctx context.Context, a Actor, kind string, id int64, in CommercialIntent, intent []byte, transition CommercialTransition) (CommercialReceipt, error) {
	return s.commercialOperation(ctx, a, kind, id, "", in, intent, nil, nil, &transition)
}

func (s *Service) commercialOperation(ctx context.Context, a Actor, kind string, id int64, permission string, in CommercialIntent, intent []byte, legacyIntent func() ([]byte, error), write func(context.Context, pgx.Tx, int64, Principal) (int64, error), transition *CommercialTransition) (CommercialReceipt, error) {
	r := CommercialReceipt{OperationID: in.OperationID}
	batch, _ := ctx.Value(commercialBatchKey{}).(*commercialBatchExecution)
	if batch != nil {
		if batch.operation.OperationID != in.OperationID || id <= 0 {
			return r, ErrCommercialInput
		}
		var err error
		intent, err = json.Marshal(map[string]any{"type": batch.operation.Type, "target": batch.operation.Target, "depends_on": batch.operation.DependsOn, "commercial": json.RawMessage(intent)})
		if err != nil {
			return r, err
		}
		legacyIntent = nil
	}
	if kind != "estimate" && kind != "invoice" {
		return r, ErrNotFound
	}
	if key, err := uuid.Parse(in.OperationID); err != nil || key == uuid.Nil {
		return r, ErrCommercialInput
	}
	ctx = context.WithValue(ctx, commercialOperationKey{}, in)
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var prior []byte
		canonical := intent
		var version int
		versionErr := tx.QueryRow(ctx, `SELECT intent_version FROM commercial_operations WHERE user_id=$1 AND client_hash=$2 AND operation_id=$3`, a.UserID, commercialReceiptClient(a), in.OperationID).Scan(&version)
		if versionErr == nil && version == 0 {
			if legacyIntent == nil {
				return ErrConflict
			}
			var err error
			canonical, err = legacyIntent()
			if err != nil {
				return err
			}
		} else if versionErr != nil && versionErr != pgx.ErrNoRows {
			return versionErr
		}
		if err := admitOperation(ctx, tx, a, in.OperationID, "commercial."+kind, map[string]any{"id": id, "intent": json.RawMessage(canonical)}); err != nil {
			return err
		}
		var same bool
		err := tx.QueryRow(ctx, `SELECT document_type=$4 AND requested_id=$5 AND intent=$6::jsonb,receipt FROM commercial_operations WHERE user_id=$1 AND client_hash=$2 AND operation_id=$3`, a.UserID, commercialReceiptClient(a), in.OperationID, kind, id, canonical).Scan(&same, &prior)
		if err == nil {
			if !same {
				return ErrConflict
			}
			return json.Unmarshal(prior, &r)
		}
		if err != pgx.ErrNoRows {
			return err
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		effect := func() error {
			if batch != nil {
				if err := batch.dependencies.validate(ctx, sp, a); err != nil {
					return err
				}
			}
			if (a.Channel != "api" || in.JobID <= 0) && (transition == nil || a.Channel != "web") {
				return ErrCommercialInput
			}
			if in.OccurredAt != nil && (in.OccurredAt.IsZero() || in.OccurredAt.After(time.Now())) {
				return ErrCommercialInput
			}
			var customer int64
			if id == 0 {
				if in.CustomerID <= 0 {
					return saveaction.ErrRevisionRequired
				}
				if _, err := parentAccess(ctx, sp, p, "job", in.JobID, "", true); err != nil {
					return err
				}
				if err := sp.QueryRow(ctx, `SELECT customer_id FROM jobs WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, in.JobID).Scan(&customer); err != nil {
					if err == pgx.ErrNoRows {
						return ErrNotFound
					}
					return err
				}
				if customer != in.CustomerID {
					return ErrConflict
				}
				if key, err := uuid.Parse(in.ClientID); err != nil || key == uuid.Nil {
					return ErrCommercialInput
				}
				var used bool
				if err := sp.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM commercial_client_identities WHERE client_id=$1)`, in.ClientID).Scan(&used); err != nil {
					return err
				}
				if used {
					return ErrConflict
				}
			} else {
				if transition != nil {
					if err := transition.Check(ctx, sp); err != nil {
						return err
					}
				} else {
					if _, err := parentAccess(ctx, sp, p, kind, id, permission, true); err != nil {
						return err
					}
				}
				var job int64
				if err := sp.QueryRow(ctx, `SELECT coalesce(job_id,0),customer_id FROM `+kind+`s WHERE id=$1`, id).Scan(&job, &customer); err != nil {
					return err
				}
				if job == 0 && a.Channel == "api" {
					return ErrNotFound
				}
				if job != in.JobID && a.Channel == "api" {
					return ErrConflict
				}
				// An optional reviewed Customer is a precondition, never a
				// replacement anchor. Older edit clients omit it and remain
				// guarded by the whole-commercial revision. Terminal replay
				// above must precede this current-state validation.
				if in.CustomerID != 0 && in.CustomerID != customer {
					return ErrConflict
				}
				current, err := saveaction.Revision(ctx, sp, a.UserID, fmt.Sprintf("/%ss/%d", kind, id))
				if err != nil {
					return err
				}
				if in.Revision == "" && transition != nil {
					return saveaction.ErrRevisionRequired
				}
				if in.Revision == "" || in.Revision != current {
					return saveaction.ErrStale
				}
			}
			var err error
			if transition != nil {
				effect, err := transition.Write(s.WithTransactionReads(ctx, sp), sp)
				if err != nil {
					return err
				}
				r = effect
				r.OperationID = in.OperationID
				return nil
			}
			r.ID, err = write(s.WithTransactionReads(ctx, sp), sp, customer, p)
			if err != nil {
				return err
			}
			if id == 0 {
				if _, err := sp.Exec(ctx, `INSERT INTO commercial_client_identities VALUES($1,$2,$3,$4,$5)`, in.ClientID, kind, r.ID, a.UserID, a.SessionHash); err != nil {
					return err
				}
			}
			if err := sp.QueryRow(ctx, `SELECT number_label FROM `+kind+`s WHERE id=$1`, r.ID).Scan(&r.Number); err != nil {
				return err
			}
			r.Revision, err = saveaction.Revision(ctx, sp, a.UserID, fmt.Sprintf("/%ss/%d", kind, r.ID))
			return err
		}()
		if effect != nil {
			_ = sp.Rollback(ctx)
			r.ID = 0
			r.Number = ""
			r.Revision = ""
			var field *customfields.FieldError
			var dependency *JobExecutionConflict
			// Only the originating domain boundary can reject commercial intent.
			// A CHECK/FK error can also originate in audit, projection or storage;
			// its SQLSTATE alone must never consume an operation identity.
			switch {
			case errors.As(effect, &dependency):
				r.Outcome = dependency.Code
			case errors.Is(effect, ErrNotFound):
				r.Outcome = "not_found"
			case errors.Is(effect, ErrForbidden):
				r.Outcome = "forbidden"
			case errors.Is(effect, saveaction.ErrStale):
				r.Outcome = "stale_commercial_revision"
			case errors.Is(effect, saveaction.ErrRevisionRequired):
				r.Outcome = "review_required"
			case errors.Is(effect, ErrConversionCycle):
				r.Outcome = "stale_conversion_cycle"
			case errors.Is(effect, ErrConflict):
				r.Outcome = "commercial_conflict"
			case errors.Is(effect, ErrCommercialInput), errors.As(effect, &field):
				r.Outcome = "invalid_document"
			default:
				return effect // dependency/audit failure stays nonterminal
			}
		} else {
			if err := sp.Commit(ctx); err != nil {
				return err
			}
			r.Outcome = "accepted"
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO commercial_operations(user_id,client_hash,operation_id,document_type,requested_id,intent,receipt,intent_version) VALUES($1,$2,$3,$4,$5,$6,$7,1)`, a.UserID, commercialReceiptClient(a), in.OperationID, kind, id, canonical, raw)
		return err
	})
	// A populated draft is not a receipt until the enclosing transaction commits.
	// This also covers a failed receipt insert or an uncertain Commit: callers
	// must reconcile the original identity, never acknowledge an uncommitted effect.
	if err != nil {
		return CommercialReceipt{OperationID: in.OperationID}, err
	}
	return r, err
}
