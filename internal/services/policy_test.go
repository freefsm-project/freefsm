package services

import (
	"context"
	"testing"
)

func TestPolicyServiceCanAccessObjectRoleActionGate(t *testing.T) {
	svc := NewPolicyService(nil)
	ctx := context.Background()

	tests := []struct {
		name       string
		role       string
		objectType string
		action     string
		want       bool
	}{
		{name: "admin can read any object type", role: "admin", objectType: "anything", action: "read", want: true},
		{name: "dispatcher can update operational object", role: "dispatcher", objectType: "job", action: "update", want: true},
		{name: "dispatcher cannot read unsupported object", role: "dispatcher", objectType: "tag", action: "read", want: false},
		{name: "tech cannot update readable object type", role: "tech", objectType: "job", action: "update", want: false},
		{name: "tech cannot delete readable object type", role: "tech", objectType: "customer", action: "delete", want: false},
		{name: "unknown role denied", role: "manager", objectType: "job", action: "read", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := svc.CanAccessObject(ctx, 1, tt.role, tt.objectType, 1, tt.action)
			if got != tt.want {
				t.Fatalf("CanAccessObject() = %v, want %v", got, tt.want)
			}
		})
	}
}
