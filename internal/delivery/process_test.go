package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type processObservation struct {
	ID      int64
	Message string
	PDFHash string
	Token   uuid.UUID
	Sent    bool
}
type processSender struct{ crash bool }

func (s processSender) Send(_ context.Context, d Delivery) (SendResult, error) {
	b, _ := json.Marshal(processObservation{d.ID, d.MessageID, fmt.Sprintf("%x", sha256.Sum256(d.PDF)), d.AttemptToken, true})
	fmt.Println("OBSERVATION " + string(b))
	if s.crash {
		os.Exit(0)
	} // external handoff observed, persistence never happens
	return SendResult{ProviderIdentifier: "owned-process", Evidence: map[string]any{"accepted": true}}, nil
}

func TestDeliveryStagedProcessHelper(t *testing.T) {
	dsn := os.Getenv("FREEFSM_DELIVERY_PROCESS_DSN")
	if dsn == "" {
		return
	} // helper is exercised by the owning parent test
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	worked, err := New(pool, "").ProcessOne(ctx, processSender{os.Getenv("FREEFSM_DELIVERY_PROCESS_MODE") == "crash"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !worked {
		fmt.Println(`OBSERVATION {"Sent":false}`)
	}
}

func TestDeliveryStagedCrashRestartAndConcurrentProcesses(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	svc := New(f.db.Pool, "")
	d, err := svc.Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"owned@example.test"}, Subject: "Process", PDF: []byte("pdf-v1 exact restart"), PDFFilename: "x.pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	command := func(mode string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDeliveryStagedProcessHelper$", "-test.count=1")
		cmd.Env = append(os.Environ(), "FREEFSM_DELIVERY_PROCESS_DSN="+f.db.Pool.Config().ConnString(), "FREEFSM_DELIVERY_PROCESS_MODE="+mode)
		return cmd
	}
	parse := func(raw []byte) processObservation {
		t.Helper()
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "OBSERVATION ") {
				var o processObservation
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "OBSERVATION ")), &o); err != nil {
					t.Fatal(err)
				}
				return o
			}
		}
		t.Fatalf("missing child evidence: %s", raw)
		return processObservation{}
	}
	raw, err := command("crash").CombinedOutput()
	if err != nil {
		t.Fatalf("crash stage: %s %v", raw, err)
	}
	first := parse(raw)
	if !first.Sent || first.ID != d.ID {
		t.Fatal("crash stage did not dispatch original")
	}
	var state string
	var attempts int
	if err = f.db.Pool.QueryRow(ctx, `SELECT state,lifetime_attempt_count FROM document_deliveries WHERE id=$1`, d.ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "sending" || attempts != 1 {
		t.Fatalf("crash lost durable claim: %s %d", state, attempts)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE document_deliveries SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		raw []byte
		err error
	}
	done := make(chan result, 2)
	for range 2 {
		go func() { raw, err := command("accept").CombinedOutput(); done <- result{raw, err} }()
	}
	sends := 0
	for range 2 {
		r := <-done
		if r.err != nil {
			t.Fatalf("restart stage: %s %v", r.raw, r.err)
		}
		o := parse(r.raw)
		if o.Sent {
			sends++
			if o.ID != first.ID || o.Message != first.Message || o.PDFHash != first.PDFHash || o.Token == first.Token {
				t.Fatal("restart changed intent/bytes or reused attempt")
			}
		}
	}
	if sends != 1 {
		t.Fatalf("concurrent workers sent %d times", sends)
	}
	if err = f.db.Pool.QueryRow(ctx, `SELECT state,lifetime_attempt_count FROM document_deliveries WHERE id=$1`, d.ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "accepted" || attempts != 2 {
		t.Fatalf("recovery=%s/%d", state, attempts)
	}
}
