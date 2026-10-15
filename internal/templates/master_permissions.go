package templates

import (
	"context"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func canMaster(ctx context.Context, permission string) bool {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return false
	}
	p, err := r.Service.Principal(ctx, r.Actor)
	if err != nil {
		return false
	}
	return p.Allows(permission, authorization.All) || p.Allows(permission, authorization.Assigned)
}

// Customer-only creation cannot borrow a Job-scoped action grant.
func canAllRecords(ctx context.Context, permission string) bool {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return false
	}
	p, err := r.Service.Principal(ctx, r.Actor)
	return err == nil && p.Allows(permission, authorization.All)
}

func canCustomerRelatedWork(ctx context.Context) bool {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return false
	}
	p, err := r.Service.Principal(ctx, r.Actor)
	if err != nil {
		return false
	}
	for _, kind := range []string{"project", "job", "estimate", "invoice"} {
		if p.Allows(kind+".view", authorization.All) || p.Allows(kind+".view", authorization.Assigned) {
			return true
		}
	}
	return false
}
