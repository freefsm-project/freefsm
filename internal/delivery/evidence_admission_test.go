package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/google/uuid"
)

func TestDeliveryEvidenceDrainsAndRetainsDuringEmailDisable(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "")
	control, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.SetInstanceControl(control)
	d, err := svc.Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"owned@example.test"}, Subject: "Evidence", HTMLBody: "<body>Signal</body>", PDF: []byte("pdf-v1"), PDFFilename: "x.pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ProcessOne(ctx, providerSender{"owned-evidence"}, nil); err != nil {
		t.Fatal(err)
	}
	token := strings.Split(strings.Split(d.HTMLBody, "/delivery/open/")[1], `"`)[0]
	if err = control.DisableEmail(); err != nil {
		t.Fatal(err)
	}
	p := ProviderEvent{CompanyID: f.company, DeliveryID: d.ID, ProviderIdentifier: "owned-evidence", EventID: "delivered", State: "delivered", Evidence: map[string]any{"provider": "delivered"}}
	if err = svc.RecordProviderEvidence(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordOpen(ctx, token); err != nil {
		t.Fatal(err)
	}
	release, err := control.Maintenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	p.State = "bounced"
	p.EventID = "bounce"
	if err = svc.RecordProviderEvidence(ctx, p); !errors.Is(err, instancecontrol.ErrMaintenance) {
		t.Fatalf("provider bypassed maintenance: %v", err)
	}
	if err = svc.RecordOpen(ctx, token); !errors.Is(err, instancecontrol.ErrMaintenance) {
		t.Fatalf("open bypassed maintenance: %v", err)
	}
}
