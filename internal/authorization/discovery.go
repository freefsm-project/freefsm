package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type DiscoveryQuery struct {
	Search        string
	Types         []string
	State, Sort   string
	Page, PerPage int
	Lease         LeaseWorkspaceQuery
}

type DiscoveryResult struct {
	Type                           string
	ID                             int64
	Identifier, Title, Description string
	URL, Match                     string
	Archived                       bool
	Status                         string
}

type DiscoveryType struct {
	Type, Label     string
	CanViewArchived bool
}

type DiscoveryPage struct {
	Query             DiscoveryQuery
	Results           []DiscoveryResult
	Types             []DiscoveryType
	Total             int
	HasVisibleRecords bool
	UpdatedAt         time.Time
}

var discoveryTypes = []DiscoveryType{
	{Type: "asset", Label: "Assets"}, {Type: "contact", Label: "Contacts"},
	{Type: "customer", Label: "Customers"}, {Type: "estimate", Label: "Estimates"},
	{Type: "invoice", Label: "Invoices"}, {Type: "item", Label: "Items"},
	{Type: "job", Label: "Jobs"}, {Type: "lease", Label: "Leases"},
	{Type: "location", Label: "Locations"}, {Type: "project", Label: "Projects"},
}

func discoveryTypeAllowed(p Principal, typ string) bool {
	if typ == "lease" {
		return p.Allows("lease.view", All) || p.Allows("lease.billing.view", All)
	}
	return CollectionAllowed(p, typ)
}

// DiscoveryAllowed is search eligibility, not operational activity eligibility.
// Context projections and Time Entry authority do not expose master catalogs.
func DiscoveryAllowed(p Principal) bool {
	for _, typ := range discoveryTypes {
		if discoveryTypeAllowed(p, typ.Type) {
			return true
		}
	}
	return false
}

func (q *DiscoveryQuery) normalize() error {
	q.Types = slices.Clone(q.Types)
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PerPage == 0 {
		q.PerPage = 25
	}
	if q.State == "" {
		q.State = "active"
	}
	if q.Sort == "" {
		q.Sort = "relevance"
	}
	if !utf8.ValidString(q.Search) || strings.ContainsRune(q.Search, 0) || utf8.RuneCountInString(q.Search) > 500 ||
		q.Page < 1 || q.Page > 1000000 || (q.PerPage != 25 && q.PerPage != 50 && q.PerPage != 100) ||
		(q.State != "active" && q.State != "archived" && q.State != "all") ||
		(q.Sort != "relevance" && q.Sort != "name" && q.Sort != "name_desc") {
		return fmt.Errorf("invalid discovery query: %w", ErrConflict)
	}
	slices.Sort(q.Types)
	for i, typ := range q.Types {
		if !slices.ContainsFunc(discoveryTypes, func(t DiscoveryType) bool { return t.Type == typ }) || i > 0 && typ == q.Types[i-1] {
			return fmt.Errorf("invalid discovery type: %w", ErrConflict)
		}
	}
	if q.Lease != (LeaseWorkspaceQuery{}) && (len(q.Types) != 1 || q.Types[0] != "lease") {
		return fmt.Errorf("Lease filters require the Lease type: %w", ErrConflict)
	}
	return nil
}

// ValidateDiscoveryQuery validates discovery state within the caller's fenced
// transaction. The caller must supply the current WithinActor principal, enforce
// the browser channel, and keep the fence through its effect/commit. This opens
// no transaction and performs only relation-target visibility reads, never a
// result query. Query normalization works on a copy and does not mutate q.
func ValidateDiscoveryQuery(ctx context.Context, tx pgx.Tx, p Principal, q DiscoveryQuery) error {
	if err := q.normalize(); err != nil {
		return err
	}
	if !DiscoveryAllowed(p) {
		return ErrForbidden
	}
	selected := discoverySelectedTypes(p, q)
	for _, typ := range selected {
		if !discoveryTypeAllowed(p, typ) || q.State != "active" && !p.Allows(typ+".archived.view", All) {
			return ErrForbidden
		}
	}
	if !slices.Contains(selected, "lease") {
		return nil
	}
	if err := q.Lease.validate(!p.Allows("lease.view", All), p.Allows("lease.billing.view", All)); err != nil {
		return err
	}
	// A relation filter is itself an existence oracle. Require the
	// independently visible, same-company target before using its ID.
	for _, rel := range []struct {
		typ, table string
		id         int64
	}{{"customer", "customers", q.Lease.Customer}, {"asset", "assets", q.Lease.Asset}, {"location", "locations", q.Lease.Premises}} {
		if rel.id == 0 {
			continue
		}
		if !p.Allows(rel.typ+".view", All) {
			return ErrForbidden
		}
		var visible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+rel.table+` WHERE id=$1 AND company_id=$2 AND (deleted_at IS NULL OR $3))`, rel.id, p.CompanyID, p.Allows(rel.typ+".archived.view", All)).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return ErrForbidden
		}
	}
	return nil
}

func discoverySelectedTypes(p Principal, q DiscoveryQuery) []string {
	if len(q.Types) != 0 {
		return q.Types
	}
	var selected []string
	for _, typ := range discoveryTypes {
		if discoveryTypeAllowed(p, typ.Type) {
			selected = append(selected, typ.Type)
		}
	}
	return selected
}

// Discover authorizes the current actor under the installation's authority
// fence. One SQL statement supplies candidates, matches, totals and the page:
// PostgreSQL's statement snapshot is the read linearization point, including
// relationship/assignment checks. The fence holds grants and session validity
// through commit, with the existing second principal check before returning.
func (s *Service) Discover(ctx context.Context, a Actor, q DiscoveryQuery) (DiscoveryPage, error) {
	if err := q.normalize(); err != nil {
		return DiscoveryPage{}, err
	}
	if a.Channel == "api" {
		return DiscoveryPage{}, ErrForbidden
	}
	out := DiscoveryPage{Query: q, Results: []DiscoveryResult{}}
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if err := ValidateDiscoveryQuery(ctx, tx, p, q); err != nil {
			return err
		}
		for _, typ := range discoveryTypes {
			if discoveryTypeAllowed(p, typ.Type) {
				typ.CanViewArchived = p.Allows(typ.Type+".archived.view", All)
				out.Types = append(out.Types, typ)
			}
		}
		selected := discoverySelectedTypes(p, q)
		// initdb --no-locale is supported; its default C collation only folds
		// ASCII. Prefer the built-in Unicode locale, then supported PG16
		// libc/ICU Unicode collations. No extension or new database locale is
		// required. Absence is unavailable, not silently case-sensitive search.
		var collation string
		if err := tx.QueryRow(ctx, `SELECT collname FROM pg_catalog.pg_collation
 WHERE collnamespace='pg_catalog'::regnamespace
 AND collname IN ('pg_c_utf8','C.utf8','C.UTF-8','und-x-icu')
 ORDER BY CASE collname WHEN 'pg_c_utf8' THEN 0 WHEN 'C.utf8' THEN 1 WHEN 'C.UTF-8' THEN 2 ELSE 3 END LIMIT 1`).Scan(&collation); err != nil {
			return fmt.Errorf("discovery Unicode collation unavailable: %w", err)
		}
		builder := discoverySQL{p: p, q: q, collation: collation}
		query, args := builder.build(selected)
		var raw []byte
		if err := tx.QueryRow(ctx, query, args...).Scan(&out.Total, &out.HasVisibleRecords, &raw, &out.UpdatedAt); err != nil {
			return err
		}
		return json.Unmarshal(raw, &out.Results)
	})
	if err != nil {
		// Never make an incomplete page available to an unwary caller.
		return DiscoveryPage{}, err
	}
	return out, nil
}
