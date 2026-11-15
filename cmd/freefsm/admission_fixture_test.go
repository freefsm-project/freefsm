package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

// Routing-only adapter mock: no database generation/recovery proof is claimed.
type routeAdmissionOwner struct {
	lease *instancecontrol.MaintenanceLease
}

func (o *routeAdmissionOwner) PrepareAdmission(_ context.Context, l *instancecontrol.MaintenanceLease) (json.RawMessage, error) {
	if l != o.lease {
		return nil, instancecontrol.ErrMaintenance
	}
	return json.RawMessage(`{"adapter":"routing"}`), nil
}
func (*routeAdmissionOwner) VerifyAdmission(b json.RawMessage) error {
	if string(b) != `{"adapter":"routing"}` {
		return instancecontrol.ErrMaintenance
	}
	return nil
}
func bootstrapRouteAdmission(t *testing.T, c *instancecontrol.Control) {
	t.Helper()
	o := &routeAdmissionOwner{}
	if err := c.BindResolutionOwner(o); err != nil {
		t.Fatal(err)
	}
	unlock, err := c.OperationLock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	l, err := c.BeginMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	o.lease = l
	if err := l.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
}
