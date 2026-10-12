package templates

import (
	"context"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func vocabularyPermission(ctx context.Context, domain, action string) bool {
	req, ok := authorization.FromContext(ctx)
	if !ok {
		return false
	}
	p, err := req.Service.Principal(ctx, req.Actor)
	return err == nil && p.Allows("configuration."+domain+"."+action, authorization.Configuration)
}

func tagPermission(ctx context.Context, base, action string) bool {
	req, ok := authorization.FromContext(ctx)
	if !ok {
		return false
	}
	parts := strings.Split(strings.Trim(base, "/"), "/")
	if len(parts) != 2 {
		return false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	return req.Service.TagActionAllowed(ctx, req.Actor, strings.TrimSuffix(parts[0], "s"), id, action)
}

func retirementAction(retired bool) string {
	if retired {
		return "reactivate"
	}
	return "retire"
}
func retirementLabel(retired bool) string {
	if retired {
		return "Reactivate"
	}
	return "Retire"
}
