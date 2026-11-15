package instancecontrol

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

func awaitBarrier(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("admission barrier timed out")
	}
}

func TestAdmissionReaderLinearizesBeforeClose(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	peer, err := New(c.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	bootstrapAdmissionFixture(t, c)
	before, decide, admitted, returnReader := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	allowDecision, allowReturn := sync.OnceFunc(func() { close(decide) }), sync.OnceFunc(func() { close(returnReader) })
	t.Cleanup(allowDecision)
	t.Cleanup(allowReturn)
	c.checkpoint = func(point string) {
		switch point {
		case "reader-before-decision":
			close(before)
			<-decide
		case "reader-admitted":
			close(admitted)
			<-returnReader
		}
	}
	type result struct {
		ctx     context.Context
		release func()
		err     error
	}
	reader := make(chan result, 1)
	go func() {
		ctx, release, err := c.Enter(context.Background())
		reader <- result{ctx, release, err}
	}()
	awaitBarrier(t, before)
	// This exact writer lock must be excluded until the reader's decision.
	f, err := peer.openLock("intent.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("writer crossed undecided reader: %v", err)
	}
	closed := make(chan struct{})
	peer.checkpoint = func(point string) {
		if point == "maintenance-closed" {
			close(closed)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	writer := make(chan result, 1)
	go func() {
		release, err := peer.Maintenance(ctx)
		writer <- result{release: release, err: err}
	}()
	allowDecision()
	awaitBarrier(t, admitted)
	awaitBarrier(t, closed)
	// The reader has committed but has not returned. Maintenance must drain it.
	select {
	case r := <-writer:
		if r.release != nil {
			r.release()
		}
		t.Fatalf("maintenance did not drain admitted reader: %v", r.err)
	default:
	}
	allowReturn()
	r := <-reader
	if r.err != nil {
		t.Fatal(r.err)
	}
	defer r.release()
	// Nested work keeps the admitted-before-close entitlement without intent.
	_, nested, err := peer.Enter(r.ctx)
	if err != nil {
		t.Fatalf("nested drain denied: %v", err)
	}
	r.release()
	select {
	case w := <-writer:
		if w.release != nil {
			w.release()
		}
		t.Fatalf("maintenance crossed retained nested admission: %v", w.err)
	default:
	}
	nested()
	w := <-writer
	if w.err != nil {
		t.Fatal(w.err)
	}
	w.release()
}

func TestAdmissionWriterLinearizesBeforeReaders(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bootstrapAdmissionFixture(t, c)
	ctx, original, err := c.Enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer original()
	peer, err := New(c.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	closed, drain := make(chan struct{}), make(chan struct{})
	allowDrain := sync.OnceFunc(func() { close(drain) })
	t.Cleanup(allowDrain)
	peer.checkpoint = func(point string) {
		if point == "maintenance-closed" {
			close(closed)
			<-drain
		}
	}
	wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		release func()
		err     error
	}
	writer := make(chan result, 1)
	go func() { release, err := peer.Maintenance(wctx); writer <- result{release, err} }()
	awaitBarrier(t, closed)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, release, err := c.Enter(context.Background())
			if release != nil {
				release()
			}
			if !errors.Is(err, ErrMaintenance) {
				t.Errorf("fresh admission after close: %v", err)
			}
		}()
	}
	wg.Wait()
	// A separate process also uses the intent fence; no in-memory state suffices.
	cmd := exec.CommandContext(wctx, os.Args[0], "-test.run=^TestAdmissionClosedProcess$")
	cmd.Env = append(os.Environ(), "FREEFSM_ADMISSION_ORDER_CHILD="+c.StateDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh process: %v: %s", err, output)
	}
	_, nested, err := c.Enter(ctx)
	if err != nil {
		t.Fatalf("nested drain blocked on writer intent: %v", err)
	}
	original()
	nested()
	allowDrain()
	w := <-writer
	if w.err != nil {
		t.Fatal(w.err)
	}
	w.release()
}

func TestAdmissionClosedProcess(t *testing.T) {
	dir := os.Getenv("FREEFSM_ADMISSION_ORDER_CHILD")
	if dir == "" {
		return
	}
	c, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, release, err := c.Enter(context.Background())
	if release != nil {
		release()
	}
	if !errors.Is(err, ErrMaintenance) {
		t.Fatalf("process admitted after closed publication: %v", err)
	}
}
