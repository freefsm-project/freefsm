package templates

import (
	"context"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

type detailAccessKey struct{}

// DetailAccess contains presentation decisions for one currently authorized
// record. Commands still enforce their own independent authority at submission.
type DetailAccess map[string]bool

func WithDetailAccess(ctx context.Context, access DetailAccess) context.Context {
	return context.WithValue(ctx, detailAccessKey{}, access)
}

func detailCan(ctx context.Context, permission string) bool {
	access, _ := ctx.Value(detailAccessKey{}).(DetailAccess)
	return access[permission]
}

func DetailCan(ctx context.Context, permission string) bool { return detailCan(ctx, permission) }

func capturedPricingVisible(ctx context.Context, typ string, id int64, isNew bool) bool {
	if isNew {
		return true
	}
	req, ok := authorization.FromContext(ctx)
	return ok && req.Service.ParentFacet(ctx, req.Actor, typ, id, "pricing.view") == nil
}

func visibleCustomerContext(ctx context.Context, jobID int64) bool {
	req, ok := authorization.FromContext(ctx)
	if !ok || jobID <= 0 {
		return false
	}
	_, err := req.Service.JobCustomer(ctx, req.Actor, jobID)
	return err == nil
}
