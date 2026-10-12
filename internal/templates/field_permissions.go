package templates

import (
	"context"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func canEditJobFields(ctx context.Context, id int64) bool {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return false
	}
	if id == 0 {
		p, err := r.Service.Principal(ctx, r.Actor)
		return err == nil && p.Allows("job.fields.edit", authorization.All)
	}
	return r.Service.ParentAccess(ctx, r.Actor, "job", id, "job.fields.edit") == nil
}

func canEditFields(ctx context.Context, typ string, id int64) bool {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return false
	}
	permission := typ + ".edit"
	if typ == "job" {
		permission = "job.fields.edit"
	}
	return r.Service.ParentAccess(ctx, r.Actor, typ, id, permission) == nil
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
