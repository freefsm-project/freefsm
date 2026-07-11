package handlers

import (
	"context"
	"time"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
)

func displayDate(ctx context.Context, t time.Time) string {
	return services.FormatCompanyDate(t, middleware.CompanyLocation(ctx), middleware.CompanyFromContext(ctx))
}

func displayDateTime(ctx context.Context, t time.Time) string {
	return services.FormatCompanyDateTime(t, middleware.CompanyLocation(ctx), middleware.CompanyFromContext(ctx))
}

func displayTime(ctx context.Context, t time.Time) string {
	return services.FormatCompanyTime(t, middleware.CompanyLocation(ctx), middleware.CompanyFromContext(ctx))
}

func displayStoredDate(ctx context.Context, value string) string {
	if value == "" {
		return ""
	}
	loc := middleware.CompanyLocation(ctx)
	if t, err := time.ParseInLocation("2006-01-02", value, loc); err == nil {
		return services.FormatCompanyDate(t, loc, middleware.CompanyFromContext(ctx))
	}
	return value
}
