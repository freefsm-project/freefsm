package templates

import (
	"context"

	"github.com/freefsm-project/freefsm/internal/middleware"
)

func itemPermission(ctx context.Context, permission string) bool {
	u, ok := middleware.UserFromContext(ctx)
	return ok && u != nil && u.ItemPermissions[permission]
}
