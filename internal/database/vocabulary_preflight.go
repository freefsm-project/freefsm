package database

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/vocabulary"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// VocabularyPreflight is a repeatable read-only inventory. Findings identify
// records for explicit review; no definitions, references or history are inferred.
func (db *DB) VocabularyPreflight(ctx context.Context) ([]string, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	findings := []string{}
	for _, table := range []string{"tags", "asset_types", "asset_statuses"} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=$1)`, table).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		var defaultCompany int64
		if table == "tags" {
			var companies bool
			if err := tx.QueryRow(ctx, `SELECT to_regclass(current_schema()||'.companies') IS NOT NULL`).Scan(&companies); err != nil {
				return nil, err
			}
			if companies {
				if err := tx.QueryRow(ctx, `SELECT CASE WHEN count(*)=1 THEN max(id) ELSE 0 END FROM companies`).Scan(&defaultCompany); err != nil {
					return nil, err
				}
			}
		}
		var hasCompany bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name='company_id')`, table).Scan(&hasCompany); err != nil {
			return nil, err
		}
		companyExpression := "$1::bigint"
		if hasCompany {
			companyExpression = "coalesce(company_id,$1)"
		}
		rows, err := tx.Query(ctx, `SELECT id,name,`+companyExpression+` FROM `+table+` ORDER BY id`, defaultCompany)
		if err != nil {
			return nil, err
		}
		type identity struct {
			Company int64
			Key     string
		}
		identities := map[identity][]int64{}
		for rows.Next() {
			var id, company int64
			var name string
			if err := rows.Scan(&id, &name, &company); err != nil {
				rows.Close()
				return nil, err
			}
			if table != "tags" {
				company = 0
			}
			if !utf8.ValidString(name) {
				findings = append(findings, fmt.Sprintf("%s unmapped legacy name encoding ID %d", table, id))
				continue
			}
			key := identity{company, vocabulary.NameKey(name)}
			identities[key] = append(identities[key], id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for key, ids := range identities {
			if key.Key == "" || len(ids) > 1 {
				findings = append(findings, fmt.Sprintf("%s blank or normalized collision IDs %v", table, ids))
			}
		}
	}
	var hasLinks bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass(current_schema()||'.tag_links') IS NOT NULL`).Scan(&hasLinks); err != nil {
		return nil, err
	}
	if hasLinks {
		// The polymorphic target inventory uses only tables present at that release.
		targets := []string{}
		for _, kind := range []string{"customer", "project", "job", "asset", "estimate", "invoice"} {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT to_regclass(current_schema()||'.'||$1) IS NOT NULL`, kind+"s").Scan(&exists); err != nil {
				return nil, err
			}
			if exists {
				targets = append(targets, `SELECT '`+kind+`' kind,id FROM `+kind+`s`)
			}
		}
		if len(targets) > 0 {
			rows, err := tx.Query(ctx, `SELECT l.id FROM tag_links l LEFT JOIN tags t ON t.id=l.tag_id LEFT JOIN (`+strings.Join(targets, " UNION ALL ")+`) p ON p.kind=l.object_type AND p.id=l.object_id WHERE t.id IS NULL OR p.id IS NULL ORDER BY l.id`)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				findings = append(findings, fmt.Sprintf("unmapped legacy tag link %d", id))
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(findings)
	return findings, tx.Commit(ctx)
}
