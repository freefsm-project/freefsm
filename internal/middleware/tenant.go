package middleware

import (
	"context"
	"net/http"
)

type tenantKey string

const TenantKey tenantKey = "tenant"

// TenantFromContext returns the tenant/company ID from the context.
// In single-tenant mode this returns (0, false). Future multi-tenant
// middleware will populate this value from the request subdomain.
func TenantFromContext(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(TenantKey).(int64)
	return id, ok
}

// Tenant is a no-op middleware in single-tenant mode.
// It simply passes through to the next handler.
// In a future multi-tenant branch this will:
//  1. Parse the Host header to extract a subdomain
//  2. Look up the company_id in a companies table
//  3. Set the tenant ID in the request context
func Tenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Single-tenant: no-op
		next.ServeHTTP(w, r)
	})
}
