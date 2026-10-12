package database

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/freefsm-project/freefsm/internal/credentials"
	"github.com/jackc/pgx/v5"
)

// IntegrationPreflight inventories older formats without modifying them or
// contacting providers. Missing keys for recognized encryption block startup;
// legacy/ambiguous formats remain intact for explicit product review.
func (db *DB) IntegrationPreflight(ctx context.Context) ([]string, error) {
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var settings, outbox bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('company_settings') IS NOT NULL,to_regclass('document_deliveries') IS NOT NULL`).Scan(&settings, &outbox); err != nil {
		return nil, err
	}
	var warnings []string
	if settings {
		rows, err := tx.Query(ctx, `SELECT id,coalesce(to_jsonb(s)->>'smtp_password',''),coalesce(to_jsonb(s)->>'map_tile_url',''),coalesce(to_jsonb(s)->>'geocoder_url','') FROM company_settings s ORDER BY id`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var password, tile, geocoder string
			if err := rows.Scan(&id, &password, &tile, &geocoder); err != nil {
				rows.Close()
				return nil, err
			}
			if password != "" {
				if strings.HasPrefix(password, "aesgcm:") {
					if _, err := credentials.Open(password); err != nil {
						rows.Close()
						return nil, err
					}
				} else {
					warnings = append(warnings, fmt.Sprintf("mail settings %d: legacy credential format requires explicit review", id))
				}
			}
			for _, raw := range []string{tile, geocoder} {
				if raw == "" {
					continue
				}
				u, err := url.Parse(strings.NewReplacer("{s}", "a", "{z}", "0", "{x}", "0", "{y}", "0", "{r}", "").Replace(raw))
				if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
					warnings = append(warnings, fmt.Sprintf("map settings %d: provider format requires explicit review", id))
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if outbox {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM document_deliveries d WHERE state IN ('queued','sending') AND coalesce(to_jsonb(d)->>'sender_address','')=''`).Scan(&count); err != nil {
			return nil, err
		}
		if count > 0 {
			warnings = append(warnings, fmt.Sprintf("%d historical queued deliveries have no recorded sender and require explicit review", count))
		}
	}
	return warnings, tx.Commit(ctx)
}
