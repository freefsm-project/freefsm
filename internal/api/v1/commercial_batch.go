package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
)

func decodeBatchCommercial(w http.ResponseWriter, r *http.Request, raw []byte) (*authorization.BatchCommercialOperation, bool) {
	var wire struct {
		authorization.BatchCommercialOperation
		DependsOn []*string `json:"depends_on"`
	}
	part := r.Clone(r.Context())
	part.Body = io.NopCloser(bytes.NewReader(raw))
	if !decodeExactJSON(w, part, &wire, "operation_id", "type", "target", "depends_on", "commercial") {
		return nil, false
	}
	op := wire.BatchCommercialOperation
	id, err := uuid.Parse(op.OperationID)
	target, targetErr := strconv.ParseInt(op.Target, 10, 64)
	if err != nil || id == uuid.Nil || id.String() != op.OperationID || targetErr != nil || target <= 0 || strconv.FormatInt(target, 10) != op.Target || wire.DependsOn == nil {
		writeError(w, 400, "invalid_request", "A canonical commercial operation, target and dependencies are required")
		return nil, false
	}
	op.DependsOn = []string{}
	for _, dep := range wire.DependsOn {
		if dep == nil {
			writeError(w, 400, "invalid_request", "Dependencies must be strings")
			return nil, false
		}
		op.DependsOn = append(op.DependsOn, *dep)
	}
	part.Body = io.NopCloser(bytes.NewReader(op.Commercial))
	switch op.Type {
	case "estimate.convert", "invoice.revert":
		var in conversion.ReviewedIntent
		properties := []string{"job_id", "revision", "occurred_at"}
		if op.Type == "estimate.convert" {
			properties = append(properties, "custom_fields")
		} else {
			properties = append(properties, "cycle_id")
		}
		if !decodeExactJSON(w, part, &in, properties...) {
			return nil, false
		}
	case "estimate.edit", "invoice.edit":
		var in services.CommercialInput
		if !decodeExactJSON(w, part, &in, "job_id", "customer_id", "revision", "occurred_at", "title", "notes", "terms", "tax_rate", "invoice_date", "due_date", "valid_through", "line_items", "custom_fields", "adjustments") {
			return nil, false
		}
	case "estimate.status", "invoice.status", "invoice.finalize":
		var in statusflow.CommercialIntent
		if !decodeExactJSON(w, part, &in, "job_id", "revision", "occurred_at", "status_id", "meaning_revision") {
			return nil, false
		}
		// The shared decoder allows null status_id for other operation families.
		// Commercial workflow selections are never nullable.
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(op.Commercial, &fields)
		if bytes.Equal(bytes.TrimSpace(fields["status_id"]), []byte("null")) {
			writeError(w, 400, "invalid_json", "A supplied status_id must be an integer")
			return nil, false
		}
	default:
		writeError(w, 400, "invalid_request", "Unsupported commercial batch operation")
		return nil, false
	}
	return &op, true
}

func executeBatchCommercial(ctx context.Context, op authorization.BatchCommercialOperation) (authorization.CommercialReceipt, error) {
	id, _ := strconv.ParseInt(op.Target, 10, 64)
	kind, action, _ := strings.Cut(op.Type, ".")
	switch op.Type {
	case "estimate.convert", "invoice.revert":
		var in conversion.ReviewedIntent
		if err := json.Unmarshal(op.Commercial, &in); err != nil {
			return authorization.CommercialReceipt{}, err
		}
		in.OperationID = op.OperationID
		req, _ := authorization.FromContext(ctx)
		return conversion.New(req.Service.Pool).ExecuteReviewed(ctx, kind, id, in)
	case "estimate.edit", "invoice.edit":
		var in services.CommercialInput
		if err := json.Unmarshal(op.Commercial, &in); err != nil {
			return authorization.CommercialReceipt{}, err
		}
		in.OperationID = op.OperationID
		return services.SaveCommercialAPI(ctx, kind, id, in)
	case "estimate.status", "invoice.status", "invoice.finalize":
		var in statusflow.CommercialIntent
		if err := json.Unmarshal(op.Commercial, &in); err != nil {
			return authorization.CommercialReceipt{}, err
		}
		in.OperationID = op.OperationID
		req, _ := authorization.FromContext(ctx)
		return statusflow.New(req.Service.Pool).ApplyCommercial(ctx, req.Actor, statusflow.ObjectType(kind), id, action == "finalize", in)
	default:
		return authorization.CommercialReceipt{}, authorization.ErrCommercialInput
	}
}
