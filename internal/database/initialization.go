package database

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// freshInitialization records provenance only in an empty installation with the
// workflow cutover available. A truncated historical migration set is not a
// current installer. Never infer provenance from user counts or familiar labels.
// The checkpoint commits with each migration, so a crash preserves both or neither.
func (db *DB) freshInitialization(ctx context.Context, files []string) (bool, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var exists, empty bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('schema_initialization') IS NOT NULL,
	 NOT EXISTS(SELECT 1 FROM schema_migrations) AND NOT EXISTS(
	 SELECT 1 FROM pg_class WHERE relnamespace=current_schema()::regnamespace
	 AND relkind IN ('r','p','v','m','f','S') AND relname <> 'schema_migrations'
	 AND relname <> 'schema_migrations_id_seq')`).Scan(&exists, &empty); err != nil {
		return false, err
	}
	if !exists {
		if !empty || !slices.Contains(files, "044_category_status_workflows.up.sql") {
			// Preserve same-run seed handling for historical migration fixtures.
			return empty, nil
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE schema_initialization (
		 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), checkpoint text NOT NULL);
		 INSERT INTO schema_initialization(checkpoint) VALUES('')`); err != nil {
			return false, err
		}
		if err := checkpointInitialization(ctx, tx); err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	var recorded string
	if err := tx.QueryRow(ctx, `SELECT checkpoint FROM schema_initialization WHERE singleton`).Scan(&recorded); err != nil {
		return false, err
	}
	current, err := initializationFingerprint(ctx, tx)
	return err == nil && current == recorded, err
}

func checkpointInitialization(ctx context.Context, tx pgx.Tx) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('schema_initialization') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return err
	}
	fingerprint, err := initializationFingerprint(ctx, tx)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE schema_initialization SET checkpoint=$1 WHERE singleton`, fingerprint)
	return err
}

// Initial seeds are small. Hash all recorded rows (including migration history
// and grants), rather than guessing whether customized rows still look fresh.
// The temporary provenance table is removed atomically at workflow cutover.
func initializationFingerprint(ctx context.Context, tx pgx.Tx) (string, error) {
	rows, err := tx.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema()
	 AND tablename <> 'schema_initialization' ORDER BY tablename`)
	if err != nil {
		return "", err
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return "", err
		}
		tables = append(tables, table)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, table := range tables {
		var data string
		if err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(row ORDER BY row::text)::text,'[]') FROM
		 (SELECT to_jsonb(t) AS row FROM `+pgx.Identifier{table}.Sanitize()+` t) data`).Scan(&data); err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "%s\x00%s\x00", table, data)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
