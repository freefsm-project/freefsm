package exports

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/google/uuid"
)

func TestEstimateCohortDurableCycleRecheck(t *testing.T) {
	pool, actor, company := operationalExportDB(t)
	ctx := context.Background()
	var customer, estimate int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,title) VALUES($1,$2,'Original estimate') RETURNING id`, company, customer).Scan(&estimate); err != nil {
		t.Fatal(err)
	}
	conversions := conversion.New(pool)
	ca := conversion.Actor{ID: actor.UserID, CompanyID: company, Role: "admin"}
	first, err := conversions.Convert(ctx, ca, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: estimate})
	if err != nil {
		t.Fatal(err)
	}
	svc := Product(pool)
	id, err := svc.Accept(ctx, actor, uuid.NewString(), "estimate-cohort-A-v1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	d, err := svc.PrepareDownload(ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(d).ReadAll()
	_ = d.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][7] != "true" || rows[1][8] != "true" || rows[1][9] == "" {
		t.Fatalf("active provenance: %v", rows)
	}
	if _, err := conversions.Revert(ctx, ca, conversion.RevertRequest{Operation: conversion.Operation{Key: uuid.New()}, InvoiceID: first.InvoiceID}); err != nil {
		t.Fatal(err)
	}
	if _, err := conversions.Convert(ctx, ca, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: estimate}); err != nil {
		t.Fatal(err)
	}
	if d, err := svc.PrepareDownload(ctx, actor, id); !errors.Is(err, ErrUnavailable) || d != nil {
		if d != nil {
			_ = d.Close()
		}
		t.Fatalf("reconversion revived original cycle: %v", err)
	}
	newID, err := svc.Accept(ctx, actor, uuid.NewString(), "estimate-cohort-A-v1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	d, err = svc.PrepareDownload(ctx, actor, newID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := csv.NewReader(d).ReadAll()
	_ = d.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 2 || current[1][2] != rows[1][2] || current[1][4] != rows[1][4] || current[1][9] == rows[1][9] {
		t.Fatalf("cohort identity/reconversion: old %v current %v", rows, current)
	}
}

func TestEstimateCohortEmptyIndependentInvoiceExportAndRevoke(t *testing.T) {
	pool, admin, company := operationalExportDB(t)
	ctx := context.Background()
	var reader int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'cohort98@test','Reader','hash','tech') RETURNING id`, company).Scan(&reader); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.export", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, admin, 0, "Cohort exporter", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, admin, reader, role); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: reader}
	svc := Product(pool)
	if _, err := svc.Accept(ctx, actor, uuid.NewString(), "estimate-cohort-A-v1", json.RawMessage(`{}`)); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("missing independent Invoice Export: %v", err)
	}
	grants = append(grants, authorization.Grant{Permission: "invoice.export", Scope: authorization.All})
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SaveRole(ctx, admin, role, "Cohort exporter", grants, revision); err != nil {
		t.Fatal(err)
	}
	id, err := svc.Accept(ctx, actor, uuid.NewString(), "estimate-cohort-A-v1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := svc.Status(ctx, actor, id)
	if err != nil || status.State != "Ready" || status.RowCount == nil || *status.RowCount != 0 {
		t.Fatalf("empty ready: %+v %v", status, err)
	}
	if _, err := auth.SaveRole(ctx, admin, role, "Cohort exporter", grants[:len(grants)-1], revision+1); err != nil {
		t.Fatal(err)
	}
	if d, err := svc.PrepareDownload(ctx, actor, id); !errors.Is(err, ErrUnavailable) || d != nil {
		if d != nil {
			_ = d.Close()
		}
		t.Fatalf("empty artifact retained revoked Invoice Export: %v", err)
	}
}
