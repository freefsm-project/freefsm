package services

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/jobassignment"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

type activityResolverFakeStore struct {
	actors        map[int64]string
	actorErr      error
	records       map[objectref.Type][]activityTargetRecord
	targetErrors  map[objectref.Type]error
	settings      *ent.CompanySettings
	settingsErr   error
	scope         activityTechnicianScope
	scopeErr      error
	actorCalls    [][]int64
	targetCalls   map[objectref.Type][][]int64
	settingsCalls int
	scopeCalls    int
	scopeTargets  []map[objectref.Type][]int64
}

func (s *activityResolverFakeStore) actorNames(_ context.Context, _ int64, ids []int64) (map[int64]string, error) {
	s.actorCalls = append(s.actorCalls, append([]int64(nil), ids...))
	return s.actors, s.actorErr
}

func (s *activityResolverFakeStore) targets(_ context.Context, _ int64, typ objectref.Type, ids []int64, _ *ent.CompanySettings) ([]activityTargetRecord, error) {
	if s.targetCalls == nil {
		s.targetCalls = make(map[objectref.Type][][]int64)
	}
	s.targetCalls[typ] = append(s.targetCalls[typ], append([]int64(nil), ids...))
	return s.records[typ], s.targetErrors[typ]
}

func (s *activityResolverFakeStore) companySettings(context.Context, int64) (*ent.CompanySettings, error) {
	s.settingsCalls++
	return s.settings, s.settingsErr
}

func (s *activityResolverFakeStore) technicianScope(_ context.Context, _, _ int64, targetIDs map[objectref.Type][]int64) (activityTechnicianScope, error) {
	s.scopeCalls++
	copied := make(map[objectref.Type][]int64, len(targetIDs))
	for typ, ids := range targetIDs {
		copied[typ] = append([]int64(nil), ids...)
	}
	s.scopeTargets = append(s.scopeTargets, copied)
	return s.scope, s.scopeErr
}

func TestActivityResolverBatchesAndDeduplicates(t *testing.T) {
	customer := objectref.New(objectref.TypeCustomer, 10)
	job := objectref.New(objectref.TypeJob, 20)
	store := &activityResolverFakeStore{
		actors: map[int64]string{1: "Current Actor"},
		records: map[objectref.Type][]activityTargetRecord{
			objectref.TypeCustomer: {{ref: customer, name: "Current Customer"}},
			objectref.TypeJob:      {{ref: job, name: "Current Job"}},
		},
	}
	resolver := &ActivityResolver{store: store}
	entries := []ActivityEntry{
		{ActorID: 1, Target: customer, Metadata: `{"actor_name":"Forged","entity_name":"Forged"}`},
		{ActorID: 1, Target: customer},
		{ActorID: 2, Target: job},
		{ActorID: 2, Target: job},
	}

	got, err := resolver.Resolve(context.Background(), 7, ActivityViewer{ID: 99, Role: "admin"}, entries)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(store.actorCalls) != 1 || !reflect.DeepEqual(store.actorCalls[0], []int64{1, 2}) {
		t.Fatalf("actor calls = %#v, want one deduplicated call", store.actorCalls)
	}
	if len(store.targetCalls[objectref.TypeCustomer]) != 1 || !reflect.DeepEqual(store.targetCalls[objectref.TypeCustomer][0], []int64{10}) {
		t.Fatalf("customer calls = %#v", store.targetCalls[objectref.TypeCustomer])
	}
	if len(store.targetCalls[objectref.TypeJob]) != 1 || !reflect.DeepEqual(store.targetCalls[objectref.TypeJob][0], []int64{20}) {
		t.Fatalf("job calls = %#v", store.targetCalls[objectref.TypeJob])
	}
	if got.ActorNames[1] != "Current Actor" || got.Targets[customer].DisplayName != "Current Customer" {
		t.Fatalf("resolution trusted metadata or lost current values: %#v", got)
	}
	if got.Targets[customer].URL != "/customers/10" || got.Targets[job].URL != "/jobs/20" {
		t.Fatalf("authorized URLs = %#v", got.Targets)
	}
}

func TestActivityResolverOmitsMissingAndCrossTenantRecords(t *testing.T) {
	local := objectref.New(objectref.TypeCustomer, 1)
	foreign := objectref.New(objectref.TypeCustomer, 2)
	missing := objectref.New(objectref.TypeCustomer, 3)
	store := &activityResolverFakeStore{
		actors: map[int64]string{1: "Local"},
		records: map[objectref.Type][]activityTargetRecord{
			objectref.TypeCustomer: {{ref: local, name: "Local Customer"}},
		},
	}
	got, err := (&ActivityResolver{store: store}).Resolve(context.Background(), 1, ActivityViewer{ID: 1, Role: "admin"}, []ActivityEntry{
		{ActorID: 1, Target: local},
		{ActorID: 2, Target: foreign},
		{ActorID: 3, Target: missing},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.ActorNames[2]; ok {
		t.Fatal("foreign actor was returned")
	}
	for _, ref := range []objectref.Ref{foreign, missing} {
		resolution := got.Targets[ref]
		if resolution.Exists || resolution.Readable || resolution.URL != "" || resolution.DisplayName != "" {
			t.Fatalf("unresolved target %v leaked data: %#v", ref, resolution)
		}
	}
}

func TestActivityResolverFailuresAreExplicitAndFailClosed(t *testing.T) {
	customer := objectref.New(objectref.TypeCustomer, 1)
	lookupErr := errors.New("database unavailable")
	store := &activityResolverFakeStore{
		actorErr:     lookupErr,
		targetErrors: map[objectref.Type]error{objectref.TypeCustomer: lookupErr},
	}
	got, err := (&ActivityResolver{store: store}).Resolve(context.Background(), 1, ActivityViewer{ID: 1, Role: "admin"}, []ActivityEntry{{ActorID: 1, Target: customer}})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(got.ActorErr, lookupErr) {
		t.Fatalf("ActorErr = %v", got.ActorErr)
	}
	target := got.Targets[customer]
	if !errors.Is(target.Err, lookupErr) || target.Readable || target.URL != "" {
		t.Fatalf("failed target did not fail closed: %#v", target)
	}
}

func TestActivityResolverOfficePolicyMatchesPolicyServiceTypes(t *testing.T) {
	for _, role := range []string{"admin", "dispatcher", "manager"} {
		for _, typ := range objectref.AllTypes() {
			t.Run(role+"/"+string(typ), func(t *testing.T) {
				target := activityTargetRecord{ref: objectref.New(typ, 1)}
				got := activityTargetReadable(ActivityViewer{ID: 1, Role: role}, target, activityTechnicianScope{})
				want := policyRoleAllows(role, typ, PolicyRead)
				if got != want {
					t.Fatalf("activity read = %v, policy type rule = %v", got, want)
				}
			})
		}
	}
}

func TestActivityResolverTechnicianPolicySemantics(t *testing.T) {
	assignedJobID := int64(10)
	otherJobID := int64(11)
	scope := activityTechnicianScope{
		jobs:            map[int64]bool{assignedJobID: true},
		customers:       map[int64]bool{20: true},
		projects:        map[int64]bool{30: true},
		assets:          map[int64]bool{40: true},
		directCustomers: map[int64]bool{21: true},
	}
	viewer := ActivityViewer{ID: 5, Role: "tech"}
	tests := []struct {
		name   string
		target activityTargetRecord
		want   bool
	}{
		{name: "assigned active job", target: activityTargetRecord{ref: objectref.New(objectref.TypeJob, assignedJobID)}, want: true},
		{name: "archived assigned job", target: activityTargetRecord{ref: objectref.New(objectref.TypeJob, assignedJobID), archived: true}},
		{name: "linked customer", target: activityTargetRecord{ref: objectref.New(objectref.TypeCustomer, 20)}, want: true},
		{name: "direct customer", target: activityTargetRecord{ref: objectref.New(objectref.TypeCustomer, 21), assignedTo: &viewer.ID}, want: true},
		{name: "archived direct customer", target: activityTargetRecord{ref: objectref.New(objectref.TypeCustomer, 21), assignedTo: &viewer.ID, archived: true}},
		{name: "linked project", target: activityTargetRecord{ref: objectref.New(objectref.TypeProject, 30)}, want: true},
		{name: "project through direct customer", target: activityTargetRecord{ref: objectref.New(objectref.TypeProject, 31), customerID: 21}, want: true},
		{name: "linked asset", target: activityTargetRecord{ref: objectref.New(objectref.TypeAsset, 40)}, want: true},
		{name: "asset through direct customer", target: activityTargetRecord{ref: objectref.New(objectref.TypeAsset, 41), customerID: 21}, want: true},
		{name: "estimate for assigned job", target: activityTargetRecord{ref: objectref.New(objectref.TypeEstimate, 50), jobID: &assignedJobID}, want: true},
		{name: "archived estimate for assigned job", target: activityTargetRecord{ref: objectref.New(objectref.TypeEstimate, 51), jobID: &assignedJobID, archived: true}, want: true},
		{name: "invoice for other job", target: activityTargetRecord{ref: objectref.New(objectref.TypeInvoice, 60), jobID: &otherJobID}},
		{name: "item unsupported", target: activityTargetRecord{ref: objectref.New(objectref.TypeItem, 70)}},
		{name: "time entry unsupported", target: activityTargetRecord{ref: objectref.New(objectref.TypeTimeEntry, 80)}},
		{name: "admin-only type unsupported", target: activityTargetRecord{ref: objectref.New(objectref.TypeTag, 90)}},
	}
	for _, role := range []string{"tech", "technician"} {
		viewer.Role = role
		for _, tt := range tests {
			t.Run(role+"/"+tt.name, func(t *testing.T) {
				if got := activityTargetReadable(viewer, tt.target, scope); got != tt.want {
					t.Fatalf("activityTargetReadable() = %v, want %v", got, tt.want)
				}
			})
		}
	}
}

func TestActivityResolverTechnicianScopeReceivesOnlyDeduplicatedPageTargets(t *testing.T) {
	job := objectref.New(objectref.TypeJob, 10)
	customer := objectref.New(objectref.TypeCustomer, 20)
	project := objectref.New(objectref.TypeProject, 30)
	estimate := objectref.New(objectref.TypeEstimate, 40)
	item := objectref.New(objectref.TypeItem, 50)
	store := &activityResolverFakeStore{
		scope: activityTechnicianScope{
			jobs:            map[int64]bool{},
			customers:       map[int64]bool{},
			projects:        map[int64]bool{},
			assets:          map[int64]bool{},
			directCustomers: map[int64]bool{},
		},
		records: map[objectref.Type][]activityTargetRecord{
			objectref.TypeJob:      {{ref: job, name: "Job"}},
			objectref.TypeCustomer: {{ref: customer, name: "Customer"}},
			objectref.TypeProject:  {{ref: project, name: "Project"}},
			objectref.TypeEstimate: {{ref: estimate, name: "Estimate"}},
			objectref.TypeItem:     {{ref: item, name: "Item"}},
		},
	}
	entries := []ActivityEntry{
		{Target: job}, {Target: job},
		{Target: customer}, {Target: customer},
		{Target: project}, {Target: estimate}, {Target: item},
	}

	_, err := (&ActivityResolver{store: store}).Resolve(context.Background(), 1, ActivityViewer{ID: 5, Role: "technician"}, entries)
	if err != nil {
		t.Fatal(err)
	}
	if store.scopeCalls != 1 {
		t.Fatalf("scope calls = %d, want 1", store.scopeCalls)
	}
	want := map[objectref.Type][]int64{
		objectref.TypeJob:      {10},
		objectref.TypeCustomer: {20},
		objectref.TypeProject:  {30},
		objectref.TypeEstimate: {40},
		objectref.TypeItem:     {50},
	}
	if !reflect.DeepEqual(store.scopeTargets[0], want) {
		t.Fatalf("scope targets = %#v, want %#v", store.scopeTargets[0], want)
	}
}

func TestActivityTechnicianJobScopeUsesCorrelatedPageBoundedSQL(t *testing.T) {
	table := entsql.Table(job.Table)
	selector := entsql.Select(table.C("*")).From(table)
	activityJobAssignedTo(7)(selector)
	activityJobRelevantToTargets(9, map[objectref.Type][]int64{
		objectref.TypeJob:      {10},
		objectref.TypeCustomer: {20},
		objectref.TypeProject:  {30},
		objectref.TypeEstimate: {40},
		objectref.TypeInvoice:  {50},
		objectref.TypeAsset:    {60},
	})(selector)
	query, args := selector.Query()
	lowerQuery := strings.ToLower(query)
	if !strings.Contains(lowerQuery, "exists") || !strings.Contains(lowerQuery, jobassignment.Table) {
		t.Fatalf("assignment predicate is not correlated EXISTS: %s", query)
	}
	if strings.Contains(lowerQuery, `"id" in (select "job_id" from "job_assignments"`) {
		t.Fatalf("assignment predicate materializes technician history: %s", query)
	}
	if len(args) != 9 {
		t.Fatalf("scope args = %v, want viewer plus bounded page/company IDs", args)
	}
}

func TestActivityResolverLoadsTimeSettingsOnce(t *testing.T) {
	clockIn := time.Date(2026, time.July, 14, 13, 5, 0, 0, time.UTC)
	clockOut := clockIn.Add(time.Hour)
	timeRef := objectref.New(objectref.TypeTimeEntry, 1)
	settingsRef := objectref.New(objectref.TypeCompanySettings, 9)
	settings := &ent.CompanySettings{ID: 9, BusinessName: "Acme", Timezone: "America/New_York", DateFormat: "2006-01-02"}
	store := &activityResolverFakeStore{
		settings: settings,
		records: map[objectref.Type][]activityTargetRecord{
			objectref.TypeTimeEntry: {{ref: timeRef, name: objectref.TimeEntryDisplayName(clockIn, &clockOut, settings)}},
		},
	}
	got, err := (&ActivityResolver{store: store}).Resolve(context.Background(), 1, ActivityViewer{ID: 1, Role: "admin"}, []ActivityEntry{
		{Target: timeRef}, {Target: timeRef}, {Target: settingsRef},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.settingsCalls != 1 {
		t.Fatalf("settings calls = %d, want 1", store.settingsCalls)
	}
	if len(store.targetCalls[objectref.TypeTimeEntry]) != 1 || len(store.targetCalls[objectref.TypeCompanySettings]) != 0 {
		t.Fatalf("target calls = %#v", store.targetCalls)
	}
	if got.Targets[timeRef].DisplayName != "2026-07-14 09:05 — 10:05" {
		t.Fatalf("time entry name = %q", got.Targets[timeRef].DisplayName)
	}
	if got.Targets[settingsRef].DisplayName != "Acme" {
		t.Fatalf("settings name = %q", got.Targets[settingsRef].DisplayName)
	}
}
