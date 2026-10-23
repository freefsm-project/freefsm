package handlers

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/google/uuid"
)

func TestConversionLegacyIdentityUpgradeReservesUnknownFactsHTTP(t *testing.T) {
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	historyCheck(t, err)
	for _, e := range entries {
		if e.Name() == "094_conversion_operation_admission.up.sql" {
			continue
		}
		raw, err := fs.ReadFile(database.MigrationFS(), e.Name())
		historyCheck(t, err)
		prior[e.Name()] = &fstest.MapFile{Data: raw}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, prior)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	key, ambiguous := newOperationKey(), newOperationKey()
	_, err = pool.Exec(ctx, `INSERT INTO estimate_invoice_conversion_operations(company_id,operation,idempotency_key,actor_id,request_fingerprint) VALUES($1,'convert',$2,$4,'recorded-unknown'),($1,'convert',$3,$4,'recorded-a'),($1,'revert',$3,$4,'recorded-b')`, company, key, ambiguous, admin)
	historyCheck(t, err)
	inventory := func() string {
		var raw string
		historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(o) ORDER BY operation,idempotency_key) FROM estimate_invoice_conversion_operations o),(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission) FROM authorization_grants g))::text`).Scan(&raw))
		return raw
	}
	before := inventory()
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	if inventory() != before {
		t.Fatal("upgrade rewrote original operations or grants")
	}
	for id, want := range map[string]string{key: "conversion.legacy", ambiguous: "legacy.identity_conflict"} {
		var kind, clientKey string
		var unknown bool
		historyCheck(t, pool.QueryRow(ctx, `SELECT operation_type,client_key,intent IS NULL FROM operation_admissions WHERE operation_id=$1`, id).Scan(&kind, &clientKey, &unknown))
		if kind != want || clientKey != "legacy.unknown" || !unknown {
			t.Fatalf("invented historical binding: %s %s %v", kind, clientKey, unknown)
		}
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Legacy identity reuse").SaveX(ctx)
	fieldPost(t, web, cookie, "/estimates", url.Values{"title": {"Fresh reviewed intent"}, "customer_id": {fmt.Sprint(c.ID)}, "line_items": {"[]"}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&id))
	fieldPost(t, web, cookie, fmt.Sprintf("/estimates/%d/convert", id), url.Values{"idempotency_key": {key}}, 409)
	expectStatus(t, web, cookie, "GET", "/commercial-operations/"+key, 409)
	var cycles int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM estimate_invoice_conversion_cycles`).Scan(&cycles))
	if cycles != 0 || inventory() != before {
		t.Fatal("unknown legacy intent was applied or rewritten")
	}
}

func TestConversionCompatibilityWriterUsesCurrentGrantsAndReservesUUID(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Compatibility writer").SaveX(ctx)
	e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Compatibility source").SetTerms("Recorded terms").SaveX(ctx)
	auth := authorization.New(pool)
	empty, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Revoked compatibility writer", nil)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("old-role-label@test").SetName("Old admin label").SetRole("admin").SetPasswordHash("hash").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, empty))
	svc := conversion.New(pool)
	_, err = svc.Convert(ctx, conversion.Actor{ID: u.ID, CompanyID: company, Role: "admin"}, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: e.ID})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("legacy Role label bypassed current grants: %v", err)
	}
	key := uuid.New()
	result, err := svc.Convert(ctx, conversion.Actor{ID: admin, CompanyID: company, Role: "admin"}, conversion.ConvertRequest{Operation: conversion.Operation{Key: key}, EstimateID: e.ID})
	historyCheck(t, err)
	body := requestBody(t, web, cookie, "GET", "/commercial-operations/"+key.String(), 200)
	assertContains(t, body, result.CycleID.String())
	assertContains(t, body, `"outcome":"accepted"`)
	assertNotContains(t, body, `"revision"`)
	if _, err := pool.Exec(ctx, `UPDATE estimate_invoice_conversion_operations SET request_fingerprint='tampered' WHERE idempotency_key=$1`, key); err == nil {
		t.Fatal("completed legacy operation provenance is mutable")
	}
	path := fmt.Sprintf("/invoices/%d/revert-to-estimate", result.InvoiceID)
	values := conversionReview(t, web, cookie, path)
	values.Set("idempotency_key", key.String())
	fieldPost(t, web, cookie, path, values, 409)
	otherCookie := sessionCookie(t, ctx, sessions, u.ID)
	expectStatus(t, web, otherCookie, "GET", "/commercial-operations/"+key.String(), 404)
}
