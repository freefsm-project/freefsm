package authorization

import (
	"context"
	"sync"

	baseent "entgo.io/ent"
	"entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/activitylog"
	"github.com/freefsm-project/freefsm/internal/ent/item"
)

// InstallItemBoundary filters every legacy catalog consumer before pagination
// and removes sensitive facets before an entity reaches presentation code.
var installedItems sync.Map

func InstallItemBoundary(client *ent.Client) {
	if _, ok := installedItems.LoadOrStore(client, true); ok {
		return
	}
	client.ActivityLog.Intercept(ent.TraverseFunc(func(ctx context.Context, q ent.Query) error {
		r, ok := FromContext(ctx)
		if !ok {
			return nil
		}
		p, err := r.Service.Principal(ctx, r.Actor)
		query := q.(*ent.ActivityLogQuery)
		if err != nil || !p.Allows("item.view", All) {
			query.Where(activitylog.ObjectTypeNEQ("item"))
			return nil
		}
		query.Where(activitylog.Or(activitylog.ObjectTypeNEQ("item"), func(selector *sql.Selector) {
			selector.Where(sql.P(func(b *sql.Builder) {
				b.WriteString(`EXISTS(SELECT 1 FROM items ai WHERE ai.id=`).WriteString(selector.C("object_id")).WriteString(` AND (ai.company_id=`).Arg(p.CompanyID).WriteString(` OR ai.company_id IS NULL)`)
				if !p.Allows("item.archived.view", All) {
					b.WriteString(` AND ai.deleted_at IS NULL`)
				}
				b.WriteString(`)`)
			}))
		}))
		return nil
	}))
	client.Item.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if _, ok := FromContext(ctx); ok {
				return nil, ErrForbidden
			}
			return next.Mutate(ctx, m)
		})
	})
	client.Item.Intercept(ent.TraverseFunc(func(ctx context.Context, q ent.Query) error {
		r, ok := FromContext(ctx)
		if !ok {
			return nil
		}
		p, err := r.Service.Principal(ctx, r.Actor)
		query := q.(*ent.ItemQuery)
		if err != nil || !p.Allows("item.view", All) {
			query.Where(item.IDEQ(-1))
			return nil
		}
		query.Where(item.Or(item.CompanyIDEQ(p.CompanyID), item.CompanyIDIsNil()))
		if !p.Allows("item.archived.view", All) {
			query.Where(item.DeletedAtIsNil())
		}
		if qc := baseent.QueryFromContext(ctx); qc != nil {
			for _, f := range qc.Fields {
				if f == "unit_cost" && !p.Allows("cost.view", All) {
					return ErrForbidden
				}
			}
		}
		return nil
	}))
	client.Item.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			v, err := next.Query(ctx, q)
			if err != nil {
				return v, err
			}
			r, ok := FromContext(ctx)
			if !ok {
				return v, nil
			}
			p, err := r.Service.Principal(ctx, r.Actor)
			if err != nil {
				return nil, err
			}
			if rows, ok := v.([]*ent.Item); ok {
				for _, i := range rows {
					if !p.Allows("cost.view", All) {
						i.UnitCost = 0
					}
					if !p.Allows("pricebook.view", All) {
						i.Sku = ""
						i.UnitPrice = 0
						i.Taxable = false
						i.TaxRate = ""
					}
				}
			}
			return v, nil
		})
	}))
}
