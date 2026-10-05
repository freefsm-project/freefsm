package delivery

import (
	"context"
	"errors"
	"testing"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

func TestWorkerRefreshFailurePreventsClaim(t *testing.T) {
	c, err := instancecontrol.New(t.TempDir())
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
