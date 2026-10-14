package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestFilePublicationDependencyPresentationIsNonterminal(t *testing.T) {
	pool, _, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	if _, err := sessions.CreateMobile(ctx, admin, "Validation device"); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: admin, Channel: "api"}
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM sessions WHERE user_id=$1 AND kind='mobile'`, admin).Scan(&actor.SessionHash); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"invalid storage page", "file extension service unavailable", "file size index unavailable"} {
		t.Run(message, func(t *testing.T) {
			op := newOperationKey()
			cause := errors.New(message)
			if err := auth.RejectFilePublication(ctx, actor, op, map[string]string{"name": "sample.txt"}, cause); !errors.Is(err, cause) {
				t.Fatal(err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_admissions WHERE operation_id=$1)+(SELECT count(*) FROM file_command_receipts WHERE operation_id=$1)`, op).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Errorf("presentation text made dependency terminal: %d", count)
			}
		})
	}
	// Typed attribution rejection remains terminal even through wrapping.
	op := newOperationKey()
	cause := fmt.Errorf("wrapped: %w", authorization.ErrFileOperation)
	if err := auth.RejectFilePublication(ctx, actor, op, map[string]string{"name": "sample.txt"}, cause); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	var code string
	if err := pool.QueryRow(ctx, `SELECT code FROM file_command_receipts WHERE operation_id=$1`, op).Scan(&code); err != nil || code != "invalid_file" {
		t.Fatalf("typed rejection %q: %v", code, err)
	}
	op = newOperationKey()
	cause = fmt.Errorf("wrapped: %w", &authorization.FileValidationError{Message: "content rejected"})
	if err := auth.RejectFilePublication(ctx, actor, op, map[string]string{"name": "sample.txt"}, cause); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT code FROM file_command_receipts WHERE operation_id=$1`, op).Scan(&code); err != nil || code != "invalid_file" {
		t.Fatalf("wrapped content rejection %q: %v", code, err)
	}
}

func TestFileContentValidationHasTypedBoundary(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("File Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("File Job").SaveX(ctx)
	svc := services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 10)
	ctx = authorization.WithActor(ctx, authorization.New(pool), authorization.Actor{UserID: admin})
	for _, tc := range []struct {
		name string
		size int64
		data string
	}{
		{"note.txt", -1, "x"}, {"note.txt", 11, "hello world"}, {"note.txt", 2, "x"},
		{"payload.exe", 1, "x"}, {"broken.zip", 1, "x"}, {"broken.doc", 1, "x"},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.name, tc.size), func(t *testing.T) {
			_, err := svc.Create(ctx, company, objectref.New(objectref.TypeJob, job.ID), tc.name, "", tc.size, strings.NewReader(tc.data), admin)
			var validation *authorization.FileValidationError
			if !errors.As(fmt.Errorf("wrapped: %w", err), &validation) {
				t.Fatalf("expected typed content validation, got %v", err)
			}
		})
	}
}
