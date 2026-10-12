package authorization

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type viewRule struct {
	base   *Grant
	groups map[string][]Grant
}
type catalog map[Grant]viewRule

type GrantOption struct {
	Grant
	Requires string `json:"requires"`
}

func grantOptions(ctx context.Context, q queryer) ([]GrantOption, error) {
	c, err := loadCatalog(ctx, q)
	if err != nil {
		return nil, err
	}
	options := []GrantOption{}
	for g, rule := range c {
		if g.Scope == Sealed {
			continue
		}
		requirements := []string{}
		if rule.base != nil {
			requirements = append(requirements, rule.base.Permission+"/"+string(rule.base.Scope))
		}
		for _, alternatives := range rule.groups {
			group := []string{}
			for _, v := range alternatives {
				group = append(group, v.Permission+"/"+string(v.Scope))
			}
			sort.Strings(group)
			requirements = append(requirements, "("+strings.Join(group, " or ")+")")
		}
		sort.Strings(requirements)
		options = append(options, GrantOption{Grant: g, Requires: strings.Join(requirements, " and ")})
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Permission == options[j].Permission {
			return options[i].Scope < options[j].Scope
		}
		return options[i].Permission < options[j].Permission
	})
	return options, nil
}

func loadCatalog(ctx context.Context, q queryer) (catalog, error) {
	result := catalog{}
	rows, err := q.Query(ctx, `SELECT permission,scope,prerequisite FROM authorization_permissions`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g Grant
		var prerequisite *string
		if err := rows.Scan(&g.Permission, &g.Scope, &prerequisite); err != nil {
			rows.Close()
			return nil, err
		}
		rule := viewRule{groups: map[string][]Grant{}}
		if prerequisite != nil {
			scope := g.Scope
			if scope == Context && *prerequisite == "job.view" {
				scope = Assigned
			}
			rule.base = &Grant{Permission: *prerequisite, Scope: scope}
		}
		result[g] = rule
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT permission,scope,requirement,view_permission,view_scope FROM authorization_view_requirements ORDER BY permission,scope,requirement,view_permission,view_scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var action, view Grant
		var group string
		if err := rows.Scan(&action.Permission, &action.Scope, &group, &view.Permission, &view.Scope); err != nil {
			return nil, err
		}
		rule, ok := result[action]
		if !ok {
			return nil, fmt.Errorf("unknown catalog action")
		}
		rule.groups[group] = append(rule.groups[group], view)
	}
	return result, rows.Err()
}

func hasView(grants map[Grant]bool, view Grant) bool {
	return grants[view] || grants[Grant{Permission: view.Permission, Scope: All}] && (view.Scope == Assigned || view.Scope == Context || view.Scope == Own)
}

func (c catalog) missingView(g Grant, grants map[Grant]bool) error {
	rule, ok := c[g]
	if !ok {
		return fmt.Errorf("unsupported grant: %s/%s", g.Permission, g.Scope)
	}
	if rule.base != nil && !hasView(grants, *rule.base) {
		return fmt.Errorf("%s/%s requires compatible %s View", g.Permission, g.Scope, rule.base.Permission)
	}
	groups := make([]string, 0, len(rule.groups))
	for group := range rule.groups {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		alternatives := rule.groups[group]
		matched := false
		for _, view := range alternatives {
			if hasView(grants, view) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s/%s requires compatible %s View", g.Permission, g.Scope, group)
		}
	}
	return nil
}

// effective retains recorded facts while denying invalid legacy combinations.
// Iterate to a fixed point: removing base View must also remove every dependent
// facet/action, regardless of row order. No prerequisite is implicitly granted.
func (c catalog) effective(raw []Grant) []Grant {
	set := map[Grant]bool{}
	for _, g := range raw {
		if _, ok := c[g]; ok {
			set[g] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for g := range set {
			if c.missingView(g, set) != nil {
				delete(set, g)
				changed = true
			}
		}
	}
	result := []Grant{}
	for _, g := range raw {
		if set[g] {
			result = append(result, g)
		}
	}
	return result
}

func validateGrants(ctx context.Context, q queryer, grants []Grant) error {
	c, err := loadCatalog(ctx, q)
	if err != nil {
		return err
	}
	seen := map[Grant]bool{}
	for _, g := range grants {
		if _, ok := c[g]; !ok || g.Scope == Sealed {
			return fmt.Errorf("unsupported or sealed grant: %s/%s", g.Permission, g.Scope)
		}
		if seen[g] {
			return fmt.Errorf("duplicate permission/scope")
		}
		seen[g] = true
	}
	for _, g := range grants {
		if err := c.missingView(g, seen); err != nil {
			return err
		}
	}
	return nil
}
