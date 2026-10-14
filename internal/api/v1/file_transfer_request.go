package v1

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/services"
)

// Transfer requests have different semantics from transfer observations. In
// particular, omitted progress/occurrence fields are allowed, but explicit
// JSON null is not a zero value in the published request contract.
type fileTransferRequest struct {
	ID            string          `json:"id"`
	PublicationID string          `json:"publication_id"`
	ObjectType    string          `json:"object_type"`
	ObjectID      int64           `json:"object_id"`
	Name          string          `json:"name"`
	Size          int64           `json:"size"`
	Digest        string          `json:"sha256"`
	Transferred   json.RawMessage `json:"transferred"`
	Verified      json.RawMessage `json:"verified"`
	OccurredAt    json.RawMessage `json:"occurred_at"`
}

func optionalFileValue[T any](raw json.RawMessage) (*T, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value *T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("null is not a File request value")
	}
	return value, nil
}

func validTransferParent(kind string, id int64) bool {
	return id > 0 && (kind == "job" || kind == "estimate" || kind == "invoice")
}

func (in fileTransferRequest) transfer() (services.FileTransfer, error) {
	out := services.FileTransfer{
		ID: in.ID, PublicationID: in.PublicationID, ObjectType: in.ObjectType,
		ObjectID: in.ObjectID, Name: in.Name, Size: in.Size, Digest: in.Digest,
	}
	transferred, err := optionalFileValue[int64](in.Transferred)
	if err != nil {
		return out, fmt.Errorf("invalid initial transfer progress")
	}
	if transferred != nil {
		out.Transferred = *transferred
	}
	verified, err := optionalFileValue[bool](in.Verified)
	if err != nil {
		return out, fmt.Errorf("invalid initial transfer verification")
	}
	if verified != nil {
		out.Verified = *verified
	}
	out.OccurredAt, err = optionalFileValue[time.Time](in.OccurredAt)
	return out, err
}
