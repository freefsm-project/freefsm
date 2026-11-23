package authorization_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestEstimateCohortNormalizer(t *testing.T) {
	for _, f := range []authorization.EstimateCohortFilter{{CreatedFrom: "2024-02-30"}, {CreatedFrom: "2025-01-01", CreatedTo: "2024-01-01"}, {Category: "Accepted"}, {Conversion: "reverted"}, {Sort: "random"}} {
		if _, err := authorization.NormalizeEstimateCohortFilter(f); err == nil {
			t.Fatalf("accepted %+v", f)
		}
	}
}

func TestEstimateCohortRetainedSourceCustomerDoesNotReplaceProvenanceAnchor(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	id := f.estimate(t, f.job, "Retained source", time.Now())
	c, err := conversion.New(f.pool).Convert(ctx, conversion.Actor{ID: f.actor.UserID, CompanyID: f.company, Role: "admin"}, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: id})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.EstimateCohort(ctx, f.actor, authorization.EstimateCohortFilter{}, authorization.Assigned)
	if err != nil || r.Totals.Total != 1 {
		t.Fatalf("initial provenance: %+v %v", r, err)
	}
	ref := r.Rows[0].Reference
	var customer int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'New Job Customer') RETURNING id`, f.company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	// The active Invoice moves to another same-Customer Job; the hidden
	// Estimate retains its original facts when its own Job changes Customer.
	if _, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.otherJob, f.actor.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE invoices SET job_id=$1 WHERE id=$2`, f.otherJob, c.InvoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET customer_id=$1 WHERE id=$2`, customer, f.job); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.EstimateCohort(ctx, f.actor, authorization.EstimateCohortFilter{}, authorization.Assigned)
	if err != nil || r.Totals.Total != 1 || r.Totals.ActiveConversions != 1 || r.Rows[0].EstimateID != id {
		t.Fatalf("retained provenance lost: %+v %v", r, err)
	}
	err = f.svc.WithinActor(ctx, f.actor, func(tx pgx.Tx, p authorization.Principal) error {
		return authorization.CheckEstimateCohortReference(ctx, tx, p, ref, authorization.Assigned, true)
	})
	if err != nil {
		t.Fatalf("retained provenance recheck disagrees: %v", err)
	}
}

func TestEstimateCohortConversionIdentityScopeAndCycleRecheck(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE company_settings SET timezone='America/New_York' WHERE company_id=$1`, f.company); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2024, 3, 10, 5, 0, 0, 0, time.UTC)
	id := f.estimate(t, f.job, "Cohort identity", created)
	f.estimate(t, f.job, "Before local date", created.Add(-time.Nanosecond*1000))
	f.estimate(t, f.job, "After 23-hour day", created.Add(23*time.Hour))
	filter := authorization.EstimateCohortFilter{CreatedFrom: "2024-03-10", CreatedTo: "2024-03-10"}
	read := func() authorization.EstimateCohortReport {
		t.Helper()
		r, err := f.svc.EstimateCohort(ctx, f.actor, filter, authorization.Assigned)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := read()
	if r.Totals.Total != 1 || r.Rows[0].EstimateID != id || r.Totals.ActiveConversions != 0 {
		t.Fatalf("creation boundary: %+v", r)
	}
	conv := conversion.New(f.pool)
	actor := conversion.Actor{ID: f.actor.UserID, CompanyID: f.company, Role: "admin"}
	cycle, err := conv.Convert(ctx, actor, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: id})
	if err != nil {
		t.Fatal(err)
	}
	r = read()
	if r.Totals.Total != 1 || r.Totals.ActiveConversions != 1 || r.Totals.Categories["estimate:accepted"] != 1 || r.Rows[0].EstimateURL != "" || !r.Rows[0].Hidden {
		t.Fatalf("provenance: %+v", r)
	}
	if err := f.svc.ParentAccess(ctx, f.actor, "estimate", id, ""); !errors.Is(err, authorization.ErrNotFound) {
		t.Fatalf("ordinary hidden access: %v", err)
	}
	ref := r.Rows[0].Reference
	check := func(ref authorization.EstimateCohortReference) error {
		return f.svc.WithinActor(ctx, f.actor, func(tx pgx.Tx, p authorization.Principal) error {
			return authorization.CheckEstimateCohortReference(ctx, tx, p, ref, authorization.Assigned, true)
		})
	}
	if err := check(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE invoices SET job_id=$1 WHERE id=$2`, f.otherJob, cycle.InvoiceID); err != nil {
		t.Fatal(err)
	}
	r = read()
	if r.Totals.Total != 0 || r.Totals.ActiveConversions != 0 || len(r.Totals.Categories) != 0 {
		t.Fatalf("unauthorized counterpart affected totals: %+v", r)
	}
	if err := check(ref); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("counterpart anchor loss: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE invoices SET job_id=$1 WHERE id=$2`, f.job, cycle.InvoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Revert(ctx, actor, conversion.RevertRequest{Operation: conversion.Operation{Key: uuid.New()}, InvoiceID: cycle.InvoiceID}); err != nil {
		t.Fatal(err)
	}
	r = read()
	if r.Totals.Total != 1 || r.Totals.ActiveConversions != 0 || r.Rows[0].Hidden || !r.Rows[0].CreatedAt.Equal(created) || r.Totals.Categories["estimate:draft"] != 1 {
		t.Fatalf("reverted cohort: %+v", r)
	}
	if err := check(ref); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("reverted artifact: %v", err)
	}
	second, err := conv.Convert(ctx, actor, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: id})
	if err != nil {
		t.Fatal(err)
	}
	r = read()
	if r.Totals.Total != 1 || r.Totals.ActiveConversions != 1 || r.Rows[0].InvoiceID != second.InvoiceID || second.InvoiceID == cycle.InvoiceID {
		t.Fatalf("reconverted cohort: %+v", r)
	}
	if err := check(ref); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("old cycle revived: %v", err)
	}
	// Losing Invoice Export must reject even a deliberately empty export.
	role, err := f.svc.SaveRole(ctx, f.actor, 0, "Cohort estimate exporter", []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.export", Scope: authorization.Assigned}, {Permission: "invoice.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	var user int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,'cohort-export@test','Exporter','hash','tech',$2) RETURNING id`, f.company, role).Scan(&user); err != nil {
		t.Fatal(err)
	}
	err = f.svc.WithinActor(ctx, authorization.Actor{UserID: user}, func(tx pgx.Tx, p authorization.Principal) error {
		_, err := authorization.EstimateCohortSnapshot(ctx, tx, p, authorization.EstimateCohortFilter{CreatedFrom: "1900-01-01", CreatedTo: "1900-01-02"}, authorization.Assigned, true)
		return err
	})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("empty Invoice Export bypass: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, user); err != nil {
		t.Fatal(err)
	}
	viewer := authorization.Actor{UserID: user}
	r, err = f.svc.EstimateCohort(ctx, viewer, filter, authorization.Assigned)
	if err != nil || r.Totals.Total != 1 {
		t.Fatalf("independent View: %+v %v", r, err)
	}
	viewerRef := r.Rows[0].Reference
	if _, err := f.pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='invoice.view'`, role); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.EstimateCohort(ctx, viewer, filter, authorization.Assigned)
	if err != nil || r.Totals.Total != 0 || r.Totals.ActiveConversions != 0 {
		t.Fatalf("revoked counterpart included: %+v %v", r, err)
	}
	err = f.svc.WithinActor(ctx, viewer, func(tx pgx.Tx, p authorization.Principal) error {
		return authorization.CheckEstimateCohortReference(ctx, tx, p, viewerRef, authorization.Assigned, false)
	})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("revoked counterpart reference: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE statuses SET name='Renamed retained Draft' WHERE id=(SELECT status_id FROM estimates WHERE id=$1)`, id); err != nil {
		t.Fatal(err)
	}
	r = read()
	if r.Totals.Categories["estimate:draft"] != 1 || r.Rows[0].StatusName != "Renamed retained Draft" {
		t.Fatalf("label split semantic category: %+v", r)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET deleted_at=now() WHERE id=$1`, f.job); err != nil {
		t.Fatal(err)
	}
	r = read()
	if r.Totals.Total != 0 {
		t.Fatalf("archived source anchor counted: %+v", r)
	}
	if err := check(viewerRef); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("archived source reference: %v", err)
	}
}
