package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/enttest"
	"github.com/freefsm-project/freefsm/internal/ent/jobassignment"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestAPIMutationConcurrentSubtasksPreserveBothUpdatesIntegration(t *testing.T) {
	client, pool := openAPIMutationTestDatabase(t)
	ctx := context.Background()
	actor, jobID := createAPIMutationFixtures(t, client, 101, `[
		{"title":"Inspect","completed":false,"sort_order":0},
		{"title":"Repair","completed":false,"sort_order":1}
	]`)
	svc := NewAPIMutationService(pool)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for index := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.SetSubtaskCompletion(ctx, actor, jobID, index, true)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("SetSubtaskCompletion: %v", err)
		}
	}

	job := client.Job.GetX(ctx, jobID)
	subtasks := ParseSubtasks(job.Subtasks)
	if len(subtasks) != 2 || !subtasks[0].Completed || !subtasks[1].Completed {
		t.Fatalf("subtasks = %#v, want both completed", subtasks)
	}
	var activities int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='job' AND object_id=$1 AND action='subtask_completed'`, jobID).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if activities != 2 {
		t.Fatalf("activity count = %d, want 2", activities)
	}
}

func TestAPIMutationClockLifecycleIsAtomicAndAuditedIntegration(t *testing.T) {
	client, pool := openAPIMutationTestDatabase(t)
	ctx := context.Background()
	actor, jobID := createAPIMutationFixtures(t, client, 102, `[]`)
	svc := NewAPIMutationService(pool)

	entry, err := svc.ClockIn(ctx, actor, jobID, APIClockInParams{Notes: "Arrived"})
	if err != nil {
		t.Fatalf("ClockIn: %v", err)
	}
	if entry.UserID != actor.UserID || entry.JobID == nil || *entry.JobID != jobID || entry.Notes != "Arrived" {
		t.Fatalf("clock-in result = %+v", entry)
	}
	if _, err = svc.ClockIn(ctx, actor, jobID, APIClockInParams{}); !errors.Is(err, ErrActiveTimeEntry) {
		t.Fatalf("second ClockIn error = %v, want ErrActiveTimeEntry", err)
	}

	entry, err = svc.ClockOut(ctx, actor)
	if err != nil {
		t.Fatalf("ClockOut: %v", err)
	}
	if entry.ClockOut == nil {
		t.Fatal("ClockOut result has nil clock_out")
	}
	if _, err = svc.ClockOut(ctx, actor); !errors.Is(err, ErrTimeEntryNotActive) {
		t.Fatalf("second ClockOut error = %v, want ErrTimeEntryNotActive", err)
	}

	var activities int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE actor_id=$1 AND action IN ('clocked_in','clocked_out')`, actor.UserID).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if activities != 4 {
		t.Fatalf("activity count = %d, want 4 (time entry and job for each mutation)", activities)
	}
}

func TestAPIMutationConcurrentClockInAllowsOneActiveEntryIntegration(t *testing.T) {
	client, pool := openAPIMutationTestDatabase(t)
	ctx := context.Background()
	actor, jobID := createAPIMutationFixtures(t, client, 108, `[]`)
	svc := NewAPIMutationService(pool)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.ClockIn(ctx, actor, jobID, APIClockInParams{})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded, conflicted := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrActiveTimeEntry):
			conflicted++
		default:
			t.Fatalf("ClockIn error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("clock-in results = %d success, %d conflict; want 1 and 1", succeeded, conflicted)
	}
	if got := client.TimeEntry.Query().CountX(ctx); got != 1 {
		t.Fatalf("time entry count = %d, want 1", got)
	}
}

func TestAPIMutationClockInRejectsForeignJobIntegration(t *testing.T) {
	client, pool := openAPIMutationTestDatabase(t)
	ctx := context.Background()
	actor, _ := createAPIMutationFixtures(t, client, 103, `[]`)
	_, foreignJobID := createAPIMutationFixtures(t, client, 104, `[]`)

	_, err := NewAPIMutationService(pool).ClockIn(ctx, actor, foreignJobID, APIClockInParams{})
	if !errors.Is(err, ErrMutationJobNotFound) {
		t.Fatalf("ClockIn error = %v, want ErrMutationJobNotFound", err)
	}
	if got := client.TimeEntry.Query().CountX(ctx); got != 0 {
		t.Fatalf("time entry count = %d, want 0", got)
	}
}

func TestAPIMutationRejectsUnassignedTechnicianIntegration(t *testing.T) {
	client, pool := openAPIMutationTestDatabase(t)
	ctx := context.Background()
	actor, jobID := createAPIMutationFixtures(t, client, 109, `[{"title":"Inspect","completed":false}]`)
	client.JobAssignment.Delete().Where(jobassignment.JobIDEQ(jobID), jobassignment.UserIDEQ(actor.UserID)).ExecX(ctx)
	svc := NewAPIMutationService(pool)

	if _, err := svc.SetSubtaskCompletion(ctx, actor, jobID, 0, true); !errors.Is(err, ErrMutationForbidden) {
		t.Fatalf("SetSubtaskCompletion error = %v, want ErrMutationForbidden", err)
	}
	if _, err := svc.ClockIn(ctx, actor, jobID, APIClockInParams{}); !errors.Is(err, ErrMutationForbidden) {
		t.Fatalf("ClockIn error = %v, want ErrMutationForbidden", err)
	}
	client.JobAssignment.Create().SetJobID(jobID).SetUserID(actor.UserID).SaveX(ctx)
	actor.Role = "viewer"
	if _, err := svc.SetSubtaskCompletion(ctx, actor, jobID, 0, true); !errors.Is(err, ErrMutationForbidden) {
		t.Fatalf("SetSubtaskCompletion unknown-role error = %v, want ErrMutationForbidden", err)
	}
	if _, err := svc.ClockIn(ctx, actor, jobID, APIClockInParams{}); !errors.Is(err, ErrMutationForbidden) {
		t.Fatalf("ClockIn unknown-role error = %v, want ErrMutationForbidden", err)
	}
	if ParseSubtasks(client.Job.GetX(ctx, jobID).Subtasks)[0].Completed || client.TimeEntry.Query().CountX(ctx) != 0 {
		t.Fatal("forbidden mutation changed state")
	}
}

func TestAPIMutationRejectsClosedJobIntegration(t *testing.T) {
	client, pool := openAPIMutationTestDatabase(t)
	ctx := context.Background()
	actor, jobID := createAPIMutationFixtures(t, client, 110, `[{"title":"Inspect","completed":false}]`)
	workflow := client.StatusWorkflow.Create().SetCompanyID(actor.CompanyID).SetName("Jobs").SetObjectType("job").SaveX(ctx)
	status := client.Status.Create().SetCompanyID(actor.CompanyID).SetWorkflowID(workflow.ID).SetName("Completed").SetCategoryKey("job:completed").SetCategoryOrder(1).SetIsCategoryDefault(true).SaveX(ctx)
	client.Job.UpdateOneID(jobID).SetStatusID(status.ID).SaveX(ctx)
	svc := NewAPIMutationService(pool)

	if _, err := svc.SetSubtaskCompletion(ctx, actor, jobID, 0, true); !errors.Is(err, ErrMutationJobClosed) {
		t.Fatalf("SetSubtaskCompletion error = %v, want ErrMutationJobClosed", err)
	}
	if _, err := svc.ClockIn(ctx, actor, jobID, APIClockInParams{}); !errors.Is(err, ErrMutationJobClosed) {
		t.Fatalf("ClockIn error = %v, want ErrMutationJobClosed", err)
	}
	if ParseSubtasks(client.Job.GetX(ctx, jobID).Subtasks)[0].Completed || client.TimeEntry.Query().CountX(ctx) != 0 {
		t.Fatal("closed-job mutation changed state")
	}
}

func TestAPIMutationActivityFailureRollsBackStateIntegration(t *testing.T) {
	t.Run("subtask", func(t *testing.T) {
		client, pool := openAPIMutationTestDatabase(t)
		ctx := context.Background()
		actor, jobID := createAPIMutationFixtures(t, client, 105, `[{"title":"Inspect","completed":false,"sort_order":0}]`)
		breakAPIMutationActivityWrites(t, pool)

		if _, err := NewAPIMutationService(pool).SetSubtaskCompletion(ctx, actor, jobID, 0, true); err == nil {
			t.Fatal("SetSubtaskCompletion error = nil")
		}
		if ParseSubtasks(client.Job.GetX(ctx, jobID).Subtasks)[0].Completed {
			t.Fatal("subtask update was not rolled back")
		}
	})

	t.Run("clock in", func(t *testing.T) {
		client, pool := openAPIMutationTestDatabase(t)
		ctx := context.Background()
		actor, jobID := createAPIMutationFixtures(t, client, 106, `[]`)
		breakAPIMutationActivityWrites(t, pool)

		if _, err := NewAPIMutationService(pool).ClockIn(ctx, actor, jobID, APIClockInParams{}); err == nil {
			t.Fatal("ClockIn error = nil")
		}
		if got := client.TimeEntry.Query().CountX(ctx); got != 0 {
			t.Fatalf("time entry count = %d, want 0", got)
		}
	})

	t.Run("clock out", func(t *testing.T) {
		client, pool := openAPIMutationTestDatabase(t)
		ctx := context.Background()
		actor, jobID := createAPIMutationFixtures(t, client, 107, `[]`)
		entry := client.TimeEntry.Create().SetCompanyID(actor.CompanyID).SetUserID(actor.UserID).SetJobID(jobID).SetClockIn(time.Now()).SaveX(ctx)
		breakAPIMutationActivityWrites(t, pool)

		if _, err := NewAPIMutationService(pool).ClockOut(ctx, actor); err == nil {
			t.Fatal("ClockOut error = nil")
		}
		if got := client.TimeEntry.GetX(ctx, entry.ID).ClockOut; got != nil {
			t.Fatalf("clock_out = %v, want nil after rollback", got)
		}
	})
}

func createAPIMutationFixtures(t *testing.T, client *ent.Client, companyID int64, subtasks string) (MutationActor, int64) {
	t.Helper()
	ctx := context.Background()
	user := client.User.Create().SetCompanyID(companyID).SetName("API Tech").SetEmail(fmt.Sprintf("api-tech-%d@example.test", companyID)).SetPasswordHash("hash").SetRole("tech").SetIsActive(true).SaveX(ctx)
	customer := client.Customer.Create().SetCompanyID(companyID).SetDisplayName("Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(companyID).SetCustomerID(customer.ID).SetJobType("Repair").SetSubtasks(subtasks).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(user.ID).SaveX(ctx)
	return MutationActor{CompanyID: companyID, UserID: user.ID, Name: user.Name, Role: user.Role}, job.ID
}

func breakAPIMutationActivityWrites(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		CREATE FUNCTION reject_api_mutation_activity() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected activity failure'; END $$;
		CREATE TRIGGER reject_api_mutation_activity BEFORE INSERT ON activity_logs
		FOR EACH ROW EXECUTE FUNCTION reject_api_mutation_activity()`)
	if err != nil {
		t.Fatalf("install activity failure trigger: %v", err)
	}
}

func openAPIMutationTestDatabase(t *testing.T) (*ent.Client, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL API mutation tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("freefsm_api_mutation_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		admin.Close()
	})

	schemaDSN, err := dsnWithSearchPath(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	schemaDB, err := sql.Open("pgx", schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = schemaDB.Close() })
	client := enttest.NewClient(t, enttest.WithOptions(ent.Driver(entsql.OpenDB(dialect.Postgres, schemaDB))))
	t.Cleanup(func() { _ = client.Close() })

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `
		ALTER TABLE jobs ALTER COLUMN subtasks DROP DEFAULT;
		ALTER TABLE jobs ALTER COLUMN subtasks TYPE jsonb USING subtasks::jsonb;
		ALTER TABLE jobs ALTER COLUMN subtasks SET DEFAULT '[]'::jsonb;
		ALTER TABLE activity_logs ALTER COLUMN metadata DROP DEFAULT;
		ALTER TABLE activity_logs ALTER COLUMN metadata TYPE jsonb USING metadata::jsonb;
		ALTER TABLE activity_logs ALTER COLUMN metadata SET DEFAULT '{}'::jsonb`); err != nil {
		t.Fatalf("align API mutation test schema with migrations: %v", err)
	}
	return client, pool
}
