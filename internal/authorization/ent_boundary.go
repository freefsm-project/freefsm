package authorization

import (
	"context"
	"sync"

	baseent "entgo.io/ent"
	"entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/activitylog"
	"github.com/freefsm-project/freefsm/internal/ent/customer"
)

type requestKey struct{}
type Request struct {
	Service *Service
	Actor   Actor
}

func WithActor(ctx context.Context, s *Service, a Actor) context.Context {
	return context.WithValue(ctx, requestKey{}, Request{s, a})
}
func FromContext(ctx context.Context) (Request, bool) {
	r, ok := ctx.Value(requestKey{}).(Request)
	return r, ok
}

var installed sync.Map

// InstallCustomerBoundary covers all existing Ent consumers, including counts,
// search, lookups, PDF source construction and related-record projections.
// Closed Context is available only through JobCustomer, never a master query.
func InstallCustomerBoundary(client *ent.Client) {
	if _, ok := installed.LoadOrStore(client, true); ok {
		return
	}
	client.Customer.Intercept(ent.TraverseFunc(func(ctx context.Context, q ent.Query) error {
		r, ok := FromContext(ctx)
		if !ok {
			return nil
		}
		query := q.(*ent.CustomerQuery)
		query.Where(func(s *sql.Selector) {
			s.Where(sql.P(func(b *sql.Builder) {
				b.WriteString(`EXISTS(SELECT 1 FROM users au JOIN authorization_grants ag ON ag.role_id=au.authorization_role_id CROSS JOIN authorization_state ax WHERE au.id=`).Arg(r.Actor.UserID).WriteString(` AND au.is_active AND ax.ready AND ag.permission='customer.view' AND ag.scope='A' AND au.company_id=`).WriteString(s.C("company_id")).WriteString(`)`)
			}))
		})
		p, err := r.Service.Principal(ctx, r.Actor)
		if err != nil {
			query.Where(customer.IDEQ(-1))
			return nil
		}
		if !p.Allows("customer.archived.view", All) {
			query.Where(customer.DeletedAtIsNil())
		}
		if !p.Allows("customer.financial.view", All) {
			if qc := baseent.QueryFromContext(ctx); qc != nil {
				for _, f := range qc.Fields {
					switch f {
					case "billing_address_1", "billing_address_2", "billing_city", "billing_state", "billing_zip_code", "tax_exempt":
						return ErrForbidden
					}
				}
			}
		}
		return nil
	}))
	client.Customer.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
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
			if err != nil || !p.Allows("customer.view", All) {
				switch out := v.(type) {
				case []*ent.Customer:
					return []*ent.Customer{}, nil
				case int:
					return 0, nil
				case *[]int64:
					*out = nil
					return out, nil
				case []int64:
					return []int64{}, nil
				default:
					return nil, ErrNotFound
				}
			}
			if !p.Allows("customer.financial.view", All) {
				if rows, ok := v.([]*ent.Customer); ok {
					for _, c := range rows {
						redactCustomerFinancial(c)
					}
				}
			}
			return v, nil
		})
	}))
	client.Customer.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if _, ok := FromContext(ctx); ok {
				return nil, ErrForbidden
			}
			return next.Mutate(ctx, m)
		})
	})
	client.ActivityLog.Intercept(ent.TraverseFunc(func(ctx context.Context, q ent.Query) error {
		r, ok := FromContext(ctx)
		if !ok {
			return nil
		}
		p, err := r.Service.Principal(ctx, r.Actor)
		if err != nil || !p.Allows("customer.view", All) {
			q.(*ent.ActivityLogQuery).Where(activitylog.ObjectTypeNEQ("customer"))
		} else {
			q.(*ent.ActivityLogQuery).Where(activitylog.Or(activitylog.ObjectTypeNEQ("customer"), func(s *sql.Selector) {
				s.Where(sql.P(func(b *sql.Builder) {
					b.WriteString(`EXISTS(SELECT 1 FROM customers ac WHERE ac.id=`).WriteString(s.C("object_id")).WriteString(` AND ac.company_id=`).Arg(p.CompanyID)
					if !p.Allows("customer.archived.view", All) {
						b.WriteString(` AND ac.deleted_at IS NULL`)
					}
					b.WriteString(`)`)
				}))
			}))
			if !p.Allows("customer.financial.view", All) {
				q.(*ent.ActivityLogQuery).Where(activitylog.Or(activitylog.ObjectTypeNEQ("customer"), activitylog.ActionIn("created", "updated", "archived", "restored")))
			}
		}
		return nil
	}))
}
