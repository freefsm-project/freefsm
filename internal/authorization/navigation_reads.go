package authorization

import (
	"context"
	"sync"

	"entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/ent"
)

type navigationReadKey struct{}
type activityProvenanceKey struct{}

// WithActivityProvenance is used only after the current visible document's
// independent provenance View has authorized this exact conversion lineage.
func WithActivityProvenance(ctx context.Context, estimateID int64) context.Context {
	return context.WithValue(ctx, activityProvenanceKey{}, estimateID)
}

// WithNavigationReads activates the bounded read adapter on installed collection
// pages. It is never attached to command requests; legacy mutation policy remains
// independent. The principal is freshly loaded for each HTTP request.
func WithNavigationReads(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, navigationReadKey{}, p)
}

func NavigationPrincipal(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(navigationReadKey{}).(Principal)
	return p, ok
}

// NavigationFacetAllowed intersects a narrower Job-bound facet with the actual
// row even when the parent collection itself is All Records.
func NavigationFacetAllowed(ctx context.Context, permission string, jobID int64) bool {
	p, ok := NavigationPrincipal(ctx)
	if !ok {
		return false
	}
	if p.Allows(permission, All) {
		return true
	}
	if jobID <= 0 || !p.Allows(permission, Assigned) {
		return false
	}
	r, ok := FromContext(ctx)
	if !ok {
		return false
	}
	allowed, err := assignedJobAccess(ctx, r.Service.Pool, p.UserID, jobID, false)
	return err == nil && allowed
}

// CollectionAllowed describes only installed full collections. Closed master
// Context is intentionally excluded; it remains a Job-bound projection.
func CollectionAllowed(p Principal, typ string) bool {
	switch typ {
	case "job", "estimate", "invoice":
		return p.Allows(typ+".view", All) || p.Allows(typ+".view", Assigned)
	case "time":
		return p.Allows("time.view", All) || p.Allows("time.view", Assigned) || p.Allows("time.view", Own)
	case "customer", "asset", "project", "item":
		return p.Allows(typ+".view", All)
	case "activity":
		for _, parent := range []string{"customer", "asset", "project", "item", "job", "estimate", "invoice", "time"} {
			if CollectionAllowed(p, parent) {
				return true
			}
		}
	}
	return false
}

func assignedReadPredicate(p Principal, jobColumn string) *sql.Predicate {
	return sql.P(func(b *sql.Builder) {
		b.WriteString(`EXISTS(SELECT 1 FROM jobs nj JOIN job_assignments na ON na.job_id=nj.id WHERE nj.id=`).WriteString(jobColumn).
			WriteString(` AND nj.company_id=`).Arg(p.CompanyID).
			WriteString(` AND nj.deleted_at IS NULL AND na.user_id=`).Arg(p.UserID).WriteString(`)`)
	})
}

func recordReadPredicate(p Principal, typ string, s *sql.Selector) *sql.Predicate {
	company := sql.EQ(s.C("company_id"), p.CompanyID)
	if typ == "item" {
		company = sql.Or(company, sql.IsNull(s.C("company_id")))
	}
	predicates := []*sql.Predicate{company}
	if typ != "time" && !p.Allows(typ+".archived.view", All) {
		predicates = append(predicates, sql.IsNull(s.C("deleted_at")))
	}
	if typ == "estimate" || typ == "invoice" {
		predicates = append(predicates, sql.IsNull(s.C("conversion_hidden_at")))
	}
	if !p.Allows(typ+".view", All) {
		var alternatives []*sql.Predicate
		jobColumn := s.C("job_id")
		if typ == "job" {
			jobColumn = s.C("id")
		}
		if (typ == "job" || typ == "estimate" || typ == "invoice" || typ == "time") && p.Allows(typ+".view", Assigned) {
			alternatives = append(alternatives, assignedReadPredicate(p, jobColumn))
		}
		if typ == "time" && p.Allows("time.view", Own) {
			alternatives = append(alternatives, sql.EQ(s.C("user_id"), p.UserID))
		}
		if len(alternatives) == 0 {
			return sql.EQ(s.C("id"), -1)
		}
		predicates = append(predicates, sql.Or(alternatives...))
	}
	return sql.And(predicates...)
}

var installedNavigationReads sync.Map

// InstallNavigationReadBoundary filters at SQL traversal, before count, order,
// pagination and eager loads. Existing Customer/Item boundaries independently
// retain their narrower facet rules. No grants are synthesized from Role names.
func InstallNavigationReadBoundary(client *ent.Client) {
	if _, loaded := installedNavigationReads.LoadOrStore(client, true); loaded {
		return
	}
	filter := ent.TraverseFunc(func(ctx context.Context, q ent.Query) error {
		p, ok := NavigationPrincipal(ctx)
		if !ok {
			return nil
		}
		where := func(typ string) func(*sql.Selector) {
			return func(s *sql.Selector) { s.Where(recordReadPredicate(p, typ, s)) }
		}
		tenant := func(s *sql.Selector) { s.Where(sql.EQ(s.C("company_id"), p.CompanyID)) }
		switch query := q.(type) {
		case *ent.JobQuery:
			query.Where(where("job"))
		case *ent.ProjectQuery:
			query.Where(where("project"))
		case *ent.AssetQuery:
			query.Where(where("asset"))
		case *ent.EstimateQuery:
			query.Where(where("estimate"))
		case *ent.InvoiceQuery:
			query.Where(where("invoice"))
		case *ent.TimeEntryQuery:
			query.Where(where("time"))
		case *ent.StatusQuery:
			query.Where(tenant)
		case *ent.StatusWorkflowQuery:
			query.Where(tenant)
		case *ent.AssetTypeQuery:
			query.Where(tenant)
		case *ent.AssetStatusQuery:
			query.Where(tenant)
		case *ent.UserQuery:
			query.Where(tenant)
			if !p.Allows("directory.view", All) {
				query.Where(func(s *sql.Selector) { s.Where(sql.EQ(s.C("id"), p.UserID)) })
			}
		case *ent.ActivityLogQuery:
			query.Where(tenant, func(s *sql.Selector) {
				var allowed []*sql.Predicate
				if root, ok := ctx.Value(activityProvenanceKey{}).(int64); ok && root > 0 {
					allowed = append(allowed, sql.P(func(b *sql.Builder) {
						b.WriteString(`((`).Ident(s.C("object_type")).WriteString(`='estimate' AND `).Ident(s.C("object_id")).WriteString(`=`).Arg(root).
							WriteString(`) OR (`).Ident(s.C("object_type")).WriteString(`='invoice' AND EXISTS(SELECT 1 FROM estimate_invoice_conversion_cycles nc WHERE nc.company_id=`).Arg(p.CompanyID).
							WriteString(` AND nc.estimate_id=`).Arg(root).WriteString(` AND nc.invoice_id=`).Ident(s.C("object_id")).WriteString(`)))`)
					}))
				}
				for _, typ := range []string{"customer", "asset", "project", "item", "job", "estimate", "invoice", "time"} {
					if !CollectionAllowed(p, typ) {
						continue
					}
					table, objectType := typ+"s", typ
					if typ == "time" {
						table, objectType = "time_entries", "time_entry"
					}
					parent := sql.Dialect("postgres").Table(table).As("nav_parent")
					sub := sql.Dialect("postgres").Select(parent.C("id")).From(parent)
					sub.Where(sql.ColumnsEQ(parent.C("id"), s.C("object_id")))
					sub.Where(recordReadPredicate(p, typ, sub))
					allowed = append(allowed, sql.And(sql.EQ(s.C("object_type"), objectType), sql.Exists(sub)))
				}
				if p.Protected { // sealed configuration activity retains its existing gate
					allowed = append(allowed, sql.In(s.C("object_type"), "company_settings", "user", "custom_field", "tag", "asset_type", "asset_status", "instance"))
				}
				if len(allowed) == 0 {
					s.Where(sql.EQ(s.C("id"), -1))
				} else {
					s.Where(sql.Or(allowed...))
				}
			})
		}
		return nil
	})
	client.Job.Intercept(filter)
	client.Project.Intercept(filter)
	client.Asset.Intercept(filter)
	client.Estimate.Intercept(filter)
	client.Invoice.Intercept(filter)
	client.TimeEntry.Intercept(filter)
	client.Status.Intercept(filter)
	client.StatusWorkflow.Intercept(filter)
	client.AssetType.Intercept(filter)
	client.AssetStatus.Intercept(filter)
	client.User.Intercept(filter)
	client.ActivityLog.Intercept(filter)
	pricing := ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			v, err := next.Query(ctx, q)
			if _, ok := NavigationPrincipal(ctx); !ok || err != nil {
				return v, err
			}
			switch rows := v.(type) {
			case []*ent.Job:
				for _, row := range rows {
					if !NavigationFacetAllowed(ctx, "pricing.view", row.ID) {
						row.LineItems, row.BillingType = "[]", ""
					}
				}
			case []*ent.Estimate:
				for _, row := range rows {
					var jobID int64
					if row.JobID != nil {
						jobID = *row.JobID
					}
					if !NavigationFacetAllowed(ctx, "pricing.view", jobID) {
						row.LineItems, row.TaxRate = "[]", ""
					}
				}
			case []*ent.Invoice:
				for _, row := range rows {
					var jobID int64
					if row.JobID != nil {
						jobID = *row.JobID
					}
					if !NavigationFacetAllowed(ctx, "pricing.view", jobID) {
						row.LineItems, row.TaxRate = "[]", ""
					}
				}
			}
			return v, err
		})
	})
	client.Job.Intercept(pricing)
	client.Estimate.Intercept(pricing)
	client.Invoice.Intercept(pricing)
	client.ActivityLog.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			v, err := next.Query(ctx, q)
			if p, ok := NavigationPrincipal(ctx); ok && !p.Protected {
				if rows, ok := v.([]*ent.ActivityLog); ok {
					// The legacy feed stores untyped diffs containing multiple facets.
					// Parent View permits event metadata, not arbitrary historical input.
					for _, row := range rows {
						row.Metadata = "{}"
					}
				}
			}
			return v, err
		})
	}))
}
