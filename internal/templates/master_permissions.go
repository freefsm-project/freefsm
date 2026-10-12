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
