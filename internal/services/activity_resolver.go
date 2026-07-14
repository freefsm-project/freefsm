package services

import (
	"context"
	"errors"
	"fmt"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/asset"
	"github.com/freefsm-project/freefsm/internal/ent/assetstatus"
	"github.com/freefsm-project/freefsm/internal/ent/assettype"
	"github.com/freefsm-project/freefsm/internal/ent/companysettings"
	"github.com/freefsm-project/freefsm/internal/ent/customer"
	"github.com/freefsm-project/freefsm/internal/ent/customfielddefinition"
	"github.com/freefsm-project/freefsm/internal/ent/estimate"
	"github.com/freefsm-project/freefsm/internal/ent/invoice"
	"github.com/freefsm-project/freefsm/internal/ent/item"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/jobassignment"
	"github.com/freefsm-project/freefsm/internal/ent/predicate"
	"github.com/freefsm-project/freefsm/internal/ent/project"
	"github.com/freefsm-project/freefsm/internal/ent/status"
	"github.com/freefsm-project/freefsm/internal/ent/tag"
	"github.com/freefsm-project/freefsm/internal/ent/timeentry"
	"github.com/freefsm-project/freefsm/internal/ent/user"
	"github.com/freefsm-project/freefsm/internal/objectref"
)

type ActivityViewer struct {
	ID   int64
	Role string
}

type ActivityTargetResolution struct {
	DisplayName string
	Exists      bool
	Readable    bool
	URL         string
	Err         error
}

type ActivityResolution struct {
	ActorNames map[int64]string
	ActorErr   error
	Targets    map[objectref.Ref]ActivityTargetResolution
}

type ActivityResolver struct {
	store activityResolverStore
}

func NewActivityResolver(client *ent.Client) *ActivityResolver {
	return &ActivityResolver{store: entActivityResolverStore{client: client}}
}

func (r *ActivityResolver) Resolve(ctx context.Context, companyID int64, viewer ActivityViewer, entries []ActivityEntry) (ActivityResolution, error) {
	if companyID <= 0 {
		return ActivityResolution{}, fmt.Errorf("resolve activity: company id must be positive")
	}
	if viewer.ID <= 0 {
		return ActivityResolution{}, fmt.Errorf("resolve activity: viewer id must be positive")
	}
	result := ActivityResolution{
		ActorNames: make(map[int64]string),
		Targets:    make(map[objectref.Ref]ActivityTargetResolution),
	}
	if r == nil || r.store == nil {
		return result, fmt.Errorf("resolve activity: resolver store is required")
	}

	actorIDs := make([]int64, 0, len(entries))
	actorSeen := make(map[int64]struct{}, len(entries))
	targetIDs := make(map[objectref.Type][]int64)
	targetSeen := make(map[objectref.Ref]struct{}, len(entries))
	for _, entry := range entries {
		if entry.ActorID > 0 {
			if _, ok := actorSeen[entry.ActorID]; !ok {
				actorSeen[entry.ActorID] = struct{}{}
				actorIDs = append(actorIDs, entry.ActorID)
			}
		}
		if !entry.Target.Valid() {
			continue
		}
		if _, ok := targetSeen[entry.Target]; ok {
			continue
		}
		targetSeen[entry.Target] = struct{}{}
		targetIDs[entry.Target.Type] = append(targetIDs[entry.Target.Type], entry.Target.ID)
		result.Targets[entry.Target] = ActivityTargetResolution{}
	}

	if len(actorIDs) > 0 {
		result.ActorNames, result.ActorErr = r.store.actorNames(ctx, companyID, actorIDs)
	}

	var scope activityTechnicianScope
	var scopeErr error
	if isTechnicianRole(viewer.Role) && technicianScopeNeeded(targetIDs) {
		scope, scopeErr = r.store.technicianScope(ctx, companyID, viewer.ID, targetIDs)
	}

	var settings *ent.CompanySettings
	var settingsErr error
	if _, ok := targetIDs[objectref.TypeTimeEntry]; ok {
		settings, settingsErr = r.store.companySettings(ctx, companyID)
	}

	for typ, ids := range targetIDs {
		var records []activityTargetRecord
		var err error
		if typ == objectref.TypeCompanySettings {
			if _, timeEntriesRepresented := targetIDs[objectref.TypeTimeEntry]; timeEntriesRepresented {
				err = settingsErr
				if err == nil && settings != nil {
					for _, id := range ids {
						if id == settings.ID {
							name := settings.BusinessName
							if name == "" {
								name = "Company Settings"
							}
							records = append(records, activityTargetRecord{ref: objectref.New(typ, id), name: name})
						}
					}
				}
			} else {
				records, err = r.store.targets(ctx, companyID, typ, ids, settings)
			}
		} else {
			records, err = r.store.targets(ctx, companyID, typ, ids, settings)
		}
		if err == nil && typ == objectref.TypeTimeEntry && settingsErr != nil {
			err = fmt.Errorf("load time entry company settings: %w", settingsErr)
		}
		if err != nil {
			for _, id := range ids {
				ref := objectref.New(typ, id)
				result.Targets[ref] = ActivityTargetResolution{Err: err}
			}
			continue
		}
		for _, record := range records {
			resolution := ActivityTargetResolution{
				DisplayName: record.name,
				Exists:      true,
			}
			if scopeErr != nil && isTechnicianRole(viewer.Role) && technicianTypeNeedsScope(record.ref.Type) {
				resolution.Err = fmt.Errorf("resolve technician activity access: %w", scopeErr)
			} else {
				resolution.Readable = activityTargetReadable(viewer, record, scope)
				if resolution.Readable {
					resolution.URL, _ = objectref.URL(record.ref)
				}
			}
			result.Targets[record.ref] = resolution
		}
	}
	return result, nil
}

func technicianScopeNeeded(targets map[objectref.Type][]int64) bool {
	for typ := range targets {
		if technicianTypeNeedsScope(typ) {
			return true
		}
	}
	return false
}

func technicianTypeNeedsScope(typ objectref.Type) bool {
	switch typ {
	case objectref.TypeCustomer, objectref.TypeJob, objectref.TypeProject, objectref.TypeEstimate, objectref.TypeInvoice, objectref.TypeAsset:
		return true
	default:
		return false
	}
}

func activityTargetReadable(viewer ActivityViewer, target activityTargetRecord, scope activityTechnicianScope) bool {
	if !policyRoleAllows(viewer.Role, target.ref.Type, PolicyRead) {
		return false
	}
	if !isTechnicianRole(viewer.Role) {
		return true
	}
	switch target.ref.Type {
	case objectref.TypeJob:
		return !target.archived && scope.jobs[target.ref.ID]
	case objectref.TypeCustomer:
		return !target.archived && (scope.customers[target.ref.ID] || pointerEqual(target.assignedTo, viewer.ID))
	case objectref.TypeProject:
		return !target.archived && (scope.projects[target.ref.ID] || scope.directCustomers[target.customerID])
	case objectref.TypeAsset:
		return !target.archived && (scope.assets[target.ref.ID] || scope.directCustomers[target.customerID])
	case objectref.TypeEstimate, objectref.TypeInvoice:
		return target.jobID != nil && scope.jobs[*target.jobID]
	default:
		return false
	}
}

func pointerEqual(value *int64, want int64) bool {
	return value != nil && *value == want
}

type activityTargetRecord struct {
	ref        objectref.Ref
	name       string
	archived   bool
	jobID      *int64
	customerID int64
	assignedTo *int64
}

type activityTechnicianScope struct {
	jobs            map[int64]bool
	customers       map[int64]bool
	projects        map[int64]bool
	assets          map[int64]bool
	directCustomers map[int64]bool
}

type activityResolverStore interface {
	actorNames(context.Context, int64, []int64) (map[int64]string, error)
	targets(context.Context, int64, objectref.Type, []int64, *ent.CompanySettings) ([]activityTargetRecord, error)
	companySettings(context.Context, int64) (*ent.CompanySettings, error)
	technicianScope(context.Context, int64, int64, map[objectref.Type][]int64) (activityTechnicianScope, error)
}

type entActivityResolverStore struct {
	client *ent.Client
}

func (s entActivityResolverStore) actorNames(ctx context.Context, companyID int64, ids []int64) (map[int64]string, error) {
	if s.client == nil {
		return nil, errors.New("activity resolver client is required")
	}
	rows, err := s.client.User.Query().Where(user.CompanyIDEQ(companyID), user.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("batch activity actors: %w", err)
	}
	names := make(map[int64]string, len(rows))
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return names, nil
}

func (s entActivityResolverStore) companySettings(ctx context.Context, companyID int64) (*ent.CompanySettings, error) {
	if s.client == nil {
		return nil, errors.New("activity resolver client is required")
	}
	settings, err := s.client.CompanySettings.Query().Where(companysettings.CompanyIDEQ(companyID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return settings, err
}

func (s entActivityResolverStore) technicianScope(ctx context.Context, companyID, userID int64, targetIDs map[objectref.Type][]int64) (activityTechnicianScope, error) {
	scope := activityTechnicianScope{
		jobs:            make(map[int64]bool),
		customers:       make(map[int64]bool),
		projects:        make(map[int64]bool),
		assets:          make(map[int64]bool),
		directCustomers: make(map[int64]bool),
	}
	if s.client == nil {
		return scope, errors.New("activity resolver client is required")
	}
	jobs, err := s.client.Job.Query().Where(
		job.CompanyIDEQ(companyID),
		job.DeletedAtIsNil(),
		activityJobAssignedTo(userID),
		activityJobRelevantToTargets(companyID, targetIDs),
	).All(ctx)
	if err != nil {
		return scope, err
	}
	pageCustomers := activityIDSet(targetIDs[objectref.TypeCustomer])
	pageProjects := activityIDSet(targetIDs[objectref.TypeProject])
	pageAssets := activityIDSet(targetIDs[objectref.TypeAsset])
	for _, row := range jobs {
		scope.jobs[row.ID] = true
		if pageCustomers[row.CustomerID] {
			scope.customers[row.CustomerID] = true
		}
		if row.ProjectID != nil && pageProjects[*row.ProjectID] {
			scope.projects[*row.ProjectID] = true
		}
		if row.AssetID != nil && pageAssets[*row.AssetID] {
			scope.assets[*row.AssetID] = true
		}
	}
	if len(pageProjects) > 0 || len(pageAssets) > 0 {
		direct, err := s.client.Customer.Query().Where(
			customer.CompanyIDEQ(companyID),
			customer.AssignedToEQ(userID),
			customer.DeletedAtIsNil(),
			activityCustomerLinkedToTargets(companyID, targetIDs),
		).IDs(ctx)
		if err != nil {
			return scope, err
		}
		for _, id := range direct {
			scope.directCustomers[id] = true
		}
	}
	return scope, nil
}

func activityJobAssignedTo(userID int64) predicate.Job {
	return func(selector *entsql.Selector) {
		assignments := entsql.Table(jobassignment.Table)
		selector.Where(entsql.Exists(
			entsql.Select().From(assignments).Where(entsql.And(
				entsql.ColumnsEQ(assignments.C(jobassignment.FieldJobID), selector.C(job.FieldID)),
				entsql.EQ(assignments.C(jobassignment.FieldUserID), userID),
			)),
		))
	}
}

func activityJobRelevantToTargets(companyID int64, targetIDs map[objectref.Type][]int64) predicate.Job {
	return func(selector *entsql.Selector) {
		filters := make([]*entsql.Predicate, 0, 6)
		if ids := targetIDs[objectref.TypeJob]; len(ids) > 0 {
			filters = append(filters, entsql.In(selector.C(job.FieldID), activityIDArgs(ids)...))
		}
		if ids := targetIDs[objectref.TypeCustomer]; len(ids) > 0 {
			filters = append(filters, entsql.In(selector.C(job.FieldCustomerID), activityIDArgs(ids)...))
		}
		if ids := targetIDs[objectref.TypeProject]; len(ids) > 0 {
			filters = append(filters, entsql.In(selector.C(job.FieldProjectID), activityIDArgs(ids)...))
		}
		if ids := targetIDs[objectref.TypeAsset]; len(ids) > 0 {
			filters = append(filters, entsql.In(selector.C(job.FieldAssetID), activityIDArgs(ids)...))
		}
		if ids := targetIDs[objectref.TypeEstimate]; len(ids) > 0 {
			estimates := entsql.Table(estimate.Table).As("activity_scope_estimates")
			filters = append(filters, entsql.In(
				selector.C(job.FieldID),
				entsql.Select(estimates.C(estimate.FieldJobID)).From(estimates).Where(entsql.And(
					entsql.EQ(estimates.C(estimate.FieldCompanyID), companyID),
					entsql.In(estimates.C(estimate.FieldID), activityIDArgs(ids)...),
					entsql.IsNull(estimates.C(estimate.FieldConversionHiddenAt)),
				)),
			))
		}
		if ids := targetIDs[objectref.TypeInvoice]; len(ids) > 0 {
			invoices := entsql.Table(invoice.Table).As("activity_scope_invoices")
			filters = append(filters, entsql.In(
				selector.C(job.FieldID),
				entsql.Select(invoices.C(invoice.FieldJobID)).From(invoices).Where(entsql.And(
					entsql.EQ(invoices.C(invoice.FieldCompanyID), companyID),
					entsql.In(invoices.C(invoice.FieldID), activityIDArgs(ids)...),
					entsql.IsNull(invoices.C(invoice.FieldConversionHiddenAt)),
				)),
			))
		}
		selector.Where(entsql.Or(filters...))
	}
}

func activityCustomerLinkedToTargets(companyID int64, targetIDs map[objectref.Type][]int64) predicate.Customer {
	return func(selector *entsql.Selector) {
		filters := make([]*entsql.Predicate, 0, 2)
		if ids := targetIDs[objectref.TypeProject]; len(ids) > 0 {
			projects := entsql.Table(project.Table).As("activity_scope_projects")
			filters = append(filters, entsql.In(
				selector.C(customer.FieldID),
				entsql.Select(projects.C(project.FieldCustomerID)).From(projects).Where(entsql.And(
					entsql.EQ(projects.C(project.FieldCompanyID), companyID),
					entsql.In(projects.C(project.FieldID), activityIDArgs(ids)...),
					entsql.IsNull(projects.C(project.FieldDeletedAt)),
				)),
			))
		}
		if ids := targetIDs[objectref.TypeAsset]; len(ids) > 0 {
			assets := entsql.Table(asset.Table).As("activity_scope_assets")
			filters = append(filters, entsql.In(
				selector.C(customer.FieldID),
				entsql.Select(assets.C(asset.FieldCustomerID)).From(assets).Where(entsql.And(
					entsql.EQ(assets.C(asset.FieldCompanyID), companyID),
					entsql.In(assets.C(asset.FieldID), activityIDArgs(ids)...),
					entsql.IsNull(assets.C(asset.FieldDeletedAt)),
				)),
			))
		}
		selector.Where(entsql.Or(filters...))
	}
}

func activityIDArgs(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

func activityIDSet(ids []int64) map[int64]bool {
	set := make(map[int64]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func (s entActivityResolverStore) targets(ctx context.Context, companyID int64, typ objectref.Type, ids []int64, settings *ent.CompanySettings) ([]activityTargetRecord, error) {
	if s.client == nil {
		return nil, errors.New("activity resolver client is required")
	}
	records := make([]activityTargetRecord, 0, len(ids))
	switch typ {
	case objectref.TypeCustomer:
		rows, err := s.client.Customer.Query().Where(customer.CompanyIDEQ(companyID), customer.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.DisplayName, archived: row.DeletedAt != nil, assignedTo: row.AssignedTo})
		}
	case objectref.TypeJob:
		rows, err := s.client.Job.Query().Where(job.CompanyIDEQ(companyID), job.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.JobType, archived: row.DeletedAt != nil, customerID: row.CustomerID})
		}
	case objectref.TypeProject:
		rows, err := s.client.Project.Query().Where(project.CompanyIDEQ(companyID), project.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.Name, archived: row.DeletedAt != nil, customerID: row.CustomerID})
		}
	case objectref.TypeEstimate:
		rows, err := s.client.Estimate.Query().Where(estimate.CompanyIDEQ(companyID), estimate.IDIn(ids...), estimate.ConversionHiddenAtIsNil()).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.Title, archived: row.DeletedAt != nil, jobID: row.JobID})
		}
	case objectref.TypeInvoice:
		rows, err := s.client.Invoice.Query().Where(invoice.CompanyIDEQ(companyID), invoice.IDIn(ids...), invoice.ConversionHiddenAtIsNil()).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.Title, archived: row.DeletedAt != nil, jobID: row.JobID})
		}
	case objectref.TypeAsset:
		rows, err := s.client.Asset.Query().Where(asset.CompanyIDEQ(companyID), asset.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.Name, archived: row.DeletedAt != nil, customerID: row.CustomerID})
		}
	case objectref.TypeItem:
		rows, err := s.client.Item.Query().Where(item.CompanyIDEQ(companyID), item.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.Name, archived: row.DeletedAt != nil})
		}
	case objectref.TypeTimeEntry:
		rows, err := s.client.TimeEntry.Query().Where(timeentry.CompanyIDEQ(companyID), timeentry.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			name := objectref.TimeEntryDisplayName(row.ClockIn, row.ClockOut, settings)
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: name})
		}
	case objectref.TypeAssetType:
		rows, err := s.client.AssetType.Query().Where(assettype.CompanyIDEQ(companyID), assettype.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.Name})
		}
	case objectref.TypeAssetStatus:
		rows, err := s.client.AssetStatus.Query().Where(assetstatus.CompanyIDEQ(companyID), assetstatus.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.Name})
		}
	case objectref.TypeCompanySettings:
		rows, err := s.client.CompanySettings.Query().Where(companysettings.CompanyIDEQ(companyID), companysettings.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			name := row.BusinessName
			if name == "" {
				name = "Company Settings"
			}
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: name})
		}
	case objectref.TypeCustomField:
		rows, err := s.client.CustomFieldDefinition.Query().Where(customfielddefinition.CompanyIDEQ(companyID), customfielddefinition.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.Name})
		}
	case objectref.TypeJobStatus:
		rows, err := s.client.Status.Query().Where(status.CompanyIDEQ(companyID), status.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.Name})
		}
	case objectref.TypeTag:
		rows, err := s.client.Tag.Query().Where(tag.CompanyIDEQ(companyID), tag.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.Name})
		}
	case objectref.TypeUser:
		rows, err := s.client.User.Query().Where(user.CompanyIDEQ(companyID), user.IDIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			records = append(records, activityTargetRecord{ref: objectref.New(typ, row.ID), name: row.Name})
		}
	default:
		return nil, fmt.Errorf("unsupported activity target type: %s", typ)
	}
	return records, nil
}
