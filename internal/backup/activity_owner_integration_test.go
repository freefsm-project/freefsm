package backup

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/services"
)

func TestBackupActivityOwnerLifecycle(t *testing.T) {
	fixture := newCrashFixture(t)
	ctx := context.Background()
	for _, interrupted := range []bool{false, true} {
		name := "active"
		if interrupted {
			name = "interrupted"
		}
		t.Run(name, func(t *testing.T) {
			m := fixture.manager(t, "source")
			actor := seedActivityActor(t, m, 1, "Administrator")
			m.cfg.ActivitySink = func(ctx context.Context, event ActivityEvent) error {
				return services.WriteBackupActivity(ctx, m.cfg.DSN, event)
			}
			var pending pendingActivity
			readPending := func() {
				b, err := os.ReadFile(m.pendingPath())
				if err != nil || json.Unmarshal(b, &pending) != nil {
					t.Fatalf("active owner lost fallback: %v", err)
				}
			}
			if interrupted {
				runCrashChild(t, m, "", "backup", "admission-opened")
				readPending()
			} else {
				reached, proceed := make(chan struct{}), make(chan struct{})
				m.checkpoint = func(point string) error {
					if point == "admission-opened" {
						close(reached)
						<-proceed
					}
					return nil
				}
				op, err := m.StartBackupFor(actor, "source", crashArchivePassword)
				if err != nil {
					t.Fatal(err)
				}
				func() {
					defer close(proceed)
					select {
					case <-reached:
					case <-time.After(30 * time.Second):
						t.Fatal("backup did not reach admission release")
					}
					readPending()
					c, err := m.connect(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close(ctx)
					var count int
					if err := c.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE event_key=$1`, pending.Event.Key).Scan(&count); err != nil || count != 0 {
						t.Fatalf("active fallback published: count=%d err=%v", count, err)
					}
					if unlock, err := m.cfg.Control.OperationLock(); err == nil {
						unlock()
						t.Fatal("active owner lost operation lock")
					}
				}()
				op = awaitOperation(t, m, actor.ID, op.ID)
				if op.Phase != "complete" {
					t.Fatalf("backup: %+v", op)
				}
				m.checkpoint = nil
			}
			for i := 0; i < 2; i++ {
				runCrashChild(t, m, "", "recover", "")
			}
			c, err := m.connect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close(ctx)
			var action string
			var count int
			if err := c.QueryRow(ctx, `SELECT min(action),count(*) FROM activity_logs WHERE event_key=$1`, pending.Event.Key).Scan(&action, &count); err != nil {
				t.Fatal(err)
			}
			want := "backup_created"
			if interrupted {
				want = "backup_failed"
				s, err := m.loadStatusDigest(pending.Operation)
				if err != nil || s.ActivityPending || s.Phase == "complete" {
					t.Fatalf("interrupted status fabricated completion: %+v %v", s, err)
				}
			}
			if count != 1 || action != want {
				t.Fatalf("immutable outcome: count=%d action=%s want=%s", count, action, want)
			}
			if _, err := os.Stat(m.pendingPath()); !os.IsNotExist(err) {
				t.Fatalf("replay evidence not drained: %v", err)
			}
		})
	}
}
