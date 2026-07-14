package services

import (
	"context"
	"errors"
	"testing"

	"github.com/freefsm-project/freefsm/internal/objectref"
)

func TestPolicyServiceCanAccessObjectDirectoryGate(t *testing.T) {
	activeJob := objectref.New(objectref.TypeJob, 1)
	archivedJob := objectref.New(objectref.TypeJob, 2)
	timeEntry := objectref.New(objectref.TypeTimeEntry, 3)
	item := objectref.New(objectref.TypeItem, 4)
	errorJob := objectref.New(objectref.TypeJob, 5)
	tag := objectref.New(objectref.TypeTag, 6)
	objects := &objectref.FakeDirectory{
		Any: map[objectref.Ref]bool{
			activeJob: true, archivedJob: true, timeEntry: true, item: true, errorJob: true, tag: true,
		},
		Active: map[objectref.Ref]bool{
			activeJob: true, archivedJob: false, item: true, errorJob: true,
		},
		Errors: map[objectref.Ref]error{errorJob: errors.New("directory unavailable")},
	}
	svc := NewPolicyService(nil, objects)
	ctx := context.Background()

	tests := []struct {
		name   string
		role   string
		ref    objectref.Ref
		action PolicyAction
		want   bool
	}{
		{name: "admin reads active target", role: "admin", ref: activeJob, action: PolicyRead, want: true},
		{name: "admin reads archived target", role: "admin", ref: archivedJob, action: PolicyRead, want: true},
		{name: "admin cannot mutate archived target", role: "admin", ref: archivedJob, action: PolicyUpdate, want: false},
		{name: "admin can add related content to active target", role: "admin", ref: activeJob, action: PolicyCreate, want: true},
		{name: "admin can mutate known non-archive target", role: "admin", ref: timeEntry, action: PolicyDelete, want: true},
		{name: "dispatcher reads archived operational target", role: "dispatcher", ref: archivedJob, action: PolicyRead, want: true},
		{name: "dispatcher cannot mutate archived target", role: "dispatcher", ref: archivedJob, action: PolicyCreate, want: false},
		{name: "dispatcher cannot access non-operational target", role: "dispatcher", ref: tag, action: PolicyRead, want: false},
		{name: "attach file allowed when target supports files", role: "admin", ref: activeJob, action: PolicyAttachFile, want: true},
		{name: "attach file denied without capability", role: "admin", ref: item, action: PolicyAttachFile, want: false},
		{name: "missing target denied", role: "admin", ref: objectref.New(objectref.TypeJob, 999), action: PolicyRead, want: false},
		{name: "invalid ID denied", role: "admin", ref: objectref.New(objectref.TypeJob, 0), action: PolicyRead, want: false},
		{name: "unknown type denied", role: "admin", ref: objectref.New(objectref.Type("unknown"), 1), action: PolicyRead, want: false},
		{name: "directory error denied", role: "admin", ref: errorJob, action: PolicyRead, want: false},
		{name: "unknown action denied", role: "admin", ref: activeJob, action: PolicyAction("publish"), want: false},
		{name: "tech cannot update", role: "tech", ref: activeJob, action: PolicyUpdate, want: false},
		{name: "unknown role denied", role: "manager", ref: activeJob, action: PolicyRead, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := svc.CanAccessObject(ctx, 1, tt.role, tt.ref, tt.action); got != tt.want {
				t.Fatalf("CanAccessObject() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPolicyServiceWithoutDirectoryFailsClosed(t *testing.T) {
	svc := NewPolicyService(nil, nil)
	if svc.CanAccessObject(context.Background(), 1, "admin", objectref.New(objectref.TypeJob, 1), PolicyRead) {
		t.Fatal("CanAccessObject() = true without directory, want false")
	}
}

func TestPolicyRoleAllowsTechnicianAliases(t *testing.T) {
	for _, role := range []string{"tech", "technician"} {
		if !policyRoleAllows(role, objectref.TypeJob, PolicyRead) {
			t.Fatalf("policyRoleAllows(%q, job, read) = false", role)
		}
		if policyRoleAllows(role, objectref.TypeJob, PolicyDelete) {
			t.Fatalf("policyRoleAllows(%q, job, delete) = true", role)
		}
	}
}
