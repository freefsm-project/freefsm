package templates

import (
	"context"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func canEditJobFields(ctx context.Context) bool {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return false
	}
	p, err := r.Service.Principal(ctx, r.Actor)
	return err == nil && p.Allows("job.fields.edit", authorization.Assigned)
}

func canEditFields(ctx context.Context, typ string) bool {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return false
	}
	p, err := r.Service.Principal(ctx, r.Actor)
	if err != nil {
		return false
	}
	permission := typ + ".edit"
	if typ == "job" {
		permission = "job.fields.edit"
	}
	return p.Allows(permission, authorization.All) || p.Allows(permission, authorization.Assigned)
}

func canManageFieldDefinitions(ctx context.Context) bool {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return false
	}
	p, err := r.Service.Principal(ctx, r.Actor)
	return err == nil && p.Allows("configuration.fields.manage", authorization.Configuration)
}

func canViewFieldDefinitions(ctx context.Context) bool {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return false
	}
	p, err := r.Service.Principal(ctx, r.Actor)
	return err == nil && p.Allows("configuration.fields.view", authorization.Configuration)
}
