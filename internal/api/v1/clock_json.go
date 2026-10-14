package v1

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

// clockWire keeps supplied values distinct from omission before domain defaults
// are applied. encoding/json's struct decoder accepts null scalars and matches
// property names case-insensitively; neither matches the published contract.
type clockWire struct{ authorization.ClockCommand }

func (wire *clockWire) UnmarshalJSON(data []byte) error {
	var gps json.RawMessage
	in := authorization.ClockCommand{}
	if err := clockObject(data, map[string]any{
		"operation_id": &in.OperationID, "entry_id": &in.EntryID,
		"job_id": &in.JobID, "notes": &in.Notes, "gps": &gps,
		"occurred_at": &in.OccurredAt, "lease_id": &in.LeaseID,
	}); err != nil {
		return err
	}
	if gps != nil {
		in.GPS = &authorization.ClockGPS{}
		if err := clockObject(gps, map[string]any{
			"latitude": &in.GPS.Latitude, "longitude": &in.GPS.Longitude,
			"captured_at": &in.GPS.CapturedAt, "accuracy": &in.GPS.Accuracy,
		}); err != nil {
			return err
		}
	}
	wire.ClockCommand = in
	return nil
}

// Reject duplicate keys as well as unknown/case-mismatched keys and explicit
// null. Each declared property's concrete decoder enforces its JSON type.
func clockObject(data []byte, properties map[string]any) error {
	invalid := errors.New("invalid clock object")
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return invalid
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return invalid
		}
		name, ok := key.(string)
		if !ok {
			return invalid
		}
		target, ok := properties[name]
		if !ok {
			return invalid
		}
		delete(properties, name)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return invalid
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid
		}
		if err := json.Unmarshal(value, target); err != nil {
			return invalid
		}
		if name == "entry_id" || name == "job_id" {
			if *target.(*int64) <= 0 {
				return invalid
			}
		}
		if name == "lease_id" {
			id, err := uuid.Parse(*target.(*string))
			if err != nil || id == uuid.Nil {
				return invalid
			}
		}
	}
	_, err = decoder.Token()
	return err
}
