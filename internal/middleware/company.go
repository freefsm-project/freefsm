package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
)

type companyKeyType string

const companyKey companyKeyType = "company"

func Company(svc *services.CompanySettingsService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cs, _ := svc.Get(r.Context())
			ctx := context.WithValue(r.Context(), companyKey, cs)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func CompanyFromContext(ctx context.Context) *ent.CompanySettings {
	cs, _ := ctx.Value(companyKey).(*ent.CompanySettings)
	return cs
}

func CompanyLocation(ctx context.Context) *time.Location {
	cs := CompanyFromContext(ctx)
	if cs != nil && cs.Timezone != "" {
		loc, err := time.LoadLocation(cs.Timezone)
		if err == nil {
			return loc
		}
	}
	return time.UTC
}
