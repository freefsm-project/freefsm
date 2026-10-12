package ent

// Handwritten transaction adapter for domain invariants and shared immutable
// audit. Both methods use the same transaction as generated Ent mutations.
import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (tx *Tx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	var result sql.Result
	err := tx.config.driver.Exec(ctx, query, args, &result)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	n, err := result.RowsAffected()
	command := ""
	if words := strings.Fields(query); len(words) > 0 {
		command = strings.ToUpper(words[0])
	}
	if command == "INSERT" {
		command += " 0"
	}
	return pgconn.NewCommandTag(fmt.Sprintf("%s %d", command, n)), err
}

func (tx *Tx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	var rows entsql.Rows
	err := tx.config.driver.Query(ctx, query, args, &rows)
	return transactionRow{rows: rows, err: err}
}

type transactionRow struct {
	rows entsql.Rows
	err  error
}

func (r transactionRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	return r.rows.Scan(dest...)
}
