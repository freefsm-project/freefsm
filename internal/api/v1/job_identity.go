package v1

import (
	"context"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

// resolveJobRoute translates only Job identity. The caller must still enter the
// actor-bound domain service to check current authority and relationships.
// Installed numeric keys remain compatible with existing field routes.
func resolveJobRoute(ctx context.Context, service *authorization.Service, key string) (int64, error) {
	id, err := strconv.ParseInt(key, 10, 64)
	if err != nil {
		return service.ResolveSyncedJob(ctx, key)
	}
	if id <= 0 {
		return 0, authorization.ErrNotFound
	}
	return id, nil
}
