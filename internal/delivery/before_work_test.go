package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

func TestWorkerRefreshFailurePreventsClaim(t *testing.T) {
	c, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// This adapter fixture proves refresh ordering only. The owner is a local
	// mock, not evidence of an installed database generation or engine recovery.
	owner := &refreshAdmissionOwner{}
	if err := c.BindResolutionOwner(owner); err != nil {
		t.Fatal(err)
	}
	unlock, err := c.OperationLock()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := c.BeginMaintenance(context.Background())
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	owner.lease = lease
	err = lease.Open(context.Background())
	lease.Close()
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	s := New(nil, "") // Any claim before refresh would dereference the absent pool.
	s.SetInstanceControl(c)
	want := errors.New("generation unavailable")
	calls := 0
	s.SetBeforeWork(func() error { calls++; return want })
	processed, err := s.ProcessOne(context.Background(), nil, nil)
	if processed || !errors.Is(err, want) || calls != 1 {
		t.Fatalf("processed=%v err=%v calls=%d", processed, err, calls)
	}
	if err := c.KeepClosed(); err != nil {
		t.Fatal(err)
	}
	processed, err = s.ProcessOne(context.Background(), nil, nil)
	if processed || err != nil || calls != 1 {
		t.Fatal("refresh ran without admission")
	}
}

type refreshAdmissionOwner struct {
	lease *instancecontrol.MaintenanceLease
}

func (o *refreshAdmissionOwner) PrepareAdmission(_ context.Context, lease *instancecontrol.MaintenanceLease) (json.RawMessage, error) {
	if lease != o.lease {
		return nil, instancecontrol.ErrMaintenance
	}
	return json.RawMessage(`{"adapter":"worker-refresh"}`), nil
}

func (*refreshAdmissionOwner) VerifyAdmission(b json.RawMessage) error {
	if string(b) != `{"adapter":"worker-refresh"}` {
		return instancecontrol.ErrMaintenance
	}
	return nil
}
