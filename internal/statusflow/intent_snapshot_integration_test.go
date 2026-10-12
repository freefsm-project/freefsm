package statusflow

import (
	"context"
	"testing"
	"time"
)

func TestWorkflowIntentSnapshotWaitsForDefinitionEdit(t *testing.T) {
	f := integrationDelivery(t, "estimate")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target, err := f.svc.Create(ctx, f.a, CreateRequest{Type: Estimate, Name: "Waiting for decision", Category: EstimateEstimate})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blocker int
	mustScan(t, tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`), &blocker)
	if _, err = tx.Exec(ctx, `SELECT id FROM statuses WHERE id=$1 FOR UPDATE`, target.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		choices []IntentChoice
		err     error
	}
	done := make(chan result, 1)
	go func() {
		choices, err := f.svc.DiscoverIntents(ctx, f.a, Estimate, f.d.DocumentID)
		done <- result{choices, err}
	}()
	for {
		var waiting bool
		mustScan(t, f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%pg_advisory_xact_lock(190019)%')`, blocker), &waiting)
		if waiting {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("snapshot bypassed definition fence: %v %+v", r.err, r.choices)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// The request must take the fence before the parent row as all workflow
	// writers do, or this NOWAIT lock would expose a lock inversion.
	if _, err = tx.Exec(ctx, `SELECT id FROM estimates WHERE id=$1 FOR UPDATE NOWAIT`, f.d.DocumentID); err != nil {
		t.Fatal(err)
	}
	var meaning string
	mustScan(t, tx.QueryRow(ctx, `UPDATE statuses SET name='Accepted locally',color='#123456',category_key='estimate:accepted',category_order=99 WHERE id=$1 RETURNING meaning_revision::text`, target.ID), &meaning)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		found := false
		revision := ""
		for _, choice := range r.choices {
			if revision == "" {
				revision = choice.Intent.Revision
			}
			if revision != choice.Intent.Revision {
				t.Fatal("choices have different record revisions")
			}
			if choice.Status.ID == target.ID {
				found = true
				if choice.Status.Category != EstimateAccepted || choice.Status.Name != "Accepted locally" || choice.Status.Color != "#123456" || choice.Status.Order != 99 || choice.Intent.MeaningRevision != meaning {
					t.Fatalf("incoherent choice: %+v", choice)
				}
			}
		}
		if !found {
			t.Fatal("missing edited choice")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
