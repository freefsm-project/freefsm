package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

// clockWire keeps supplied values distinct from omission before domain defaults
// are applied. encoding/json's struct decoder accepts null scalars and matches
// property names case-insensitively; neither matches the published contract.
type clockWire struct{ authorization.ClockCommand }

func (wire *clockWire) UnmarshalJSON(data []byte) error {
	return wire.decode(data, nil)
}

func (wire *clockWire) decode(data []byte, extra map[string]any) error {
	var gps json.RawMessage
	in := authorization.ClockCommand{}
	properties := map[string]any{
		"operation_id": &in.OperationID, "entry_id": &in.EntryID,
		"job_id": &in.JobID, "notes": &in.Notes, "gps": &gps,
		"occurred_at": &in.OccurredAt, "lease_id": &in.LeaseID,
	}
	for key, target := range extra {
		if target == nil {
			delete(properties, key)
		} else {
			properties[key] = target
		}
	}
	if err := clockObject(data, properties); err != nil {
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

// Batch references preserve installed bigint identities in JavaScript without
// widening the existing integer-only single-command request contract.
type batchClockWire struct {
	authorization.ClockCommand
	EntryRef, JobRef string
}

func (wire *batchClockWire) UnmarshalJSON(data []byte) error {
	var command clockWire
	var entryRef, jobRef *string
	if err := command.decode(data, map[string]any{"operation_id": nil, "entry_ref": &entryRef, "job_ref": &jobRef}); err != nil {
		return err
	}
	for _, reference := range []struct {
		text    *string
		target  *int64
		binding *string
	}{{entryRef, &command.EntryID, &wire.EntryRef}, {jobRef, &command.JobID, &wire.JobRef}} {
		if reference.text == nil {
			continue
		}
		text := *reference.text
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != text || *reference.target != 0 {
			return errors.New("invalid clock reference")
		}
		*reference.target = id
		*reference.binding = text
	}
	wire.ClockCommand = command.ClockCommand
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
