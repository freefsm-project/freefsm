package services

import (
	"context"
	"database/sql"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/jackc/pgx/v5"
)

type detailsTxKey struct{}

func createDocument(ctx context.Context, request authorization.Request, kind string, in authorization.DocumentCreation, prepare func(string) (string, error)) (int64, error) {
	if tx, ok := ctx.Value(detailsTxKey{}).(pgx.Tx); ok {
		return request.Service.CreateDocumentInTransaction(ctx, tx, request.Actor, kind, in, prepare)
	}
	return request.Service.CreateDocument(ctx, request.Actor, kind, in, prepare)
}

func needsDetailsTransaction(ctx context.Context) bool {
	_, ok := authorization.FromContext(ctx)
	return ok && ctx.Value(detailsTxKey{}) == nil
}

// saveDetails adapts the existing domain services to the shared committing
// boundary without splitting field persistence from the rest of the record.
func saveDetails[T any](ctx context.Context, typ string, id, jobID int64, input *string, save func(context.Context, *ent.Client, string) (*T, int64, error)) (*T, error) {
	request, ok := authorization.FromContext(ctx)
	if !ok || request.Service == nil {
		return nil, authorization.ErrForbidden
	}
	var result *T
	write := func(tx pgx.Tx, fields string) (int64, error) {
		ctx = context.WithValue(ctx, detailsTxKey{}, tx)
		ctx = request.Service.WithTransactionReads(ctx, tx)
		client := ent.NewClient(ent.Driver(&fieldTxDriver{tx: tx}))
		var resultID int64
		var err error
		result, resultID, err = save(ctx, client, fields)
		return resultID, err
	}
	var err error
	if tx, ok := ctx.Value(detailsTxKey{}).(pgx.Tx); ok {
		err = request.Service.DetailsInTransaction(ctx, tx, request.Actor, typ, id, jobID, input, write)
	} else {
		err = request.Service.DetailsTransaction(ctx, request.Actor, typ, id, jobID, input, write)
	}
	return result, err
}

// fieldTxDriver bridges Ent's existing domain builders to the *same* pgx
// transaction that owns authorization, definition validation and immutable audit.
// Nested domain transactions use savepoints; they never independently commit.
type fieldTxDriver struct{ tx pgx.Tx }

func (d *fieldTxDriver) Dialect() string { return dialect.Postgres }
func (d *fieldTxDriver) Close() error    { return nil }
func (d *fieldTxDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &fieldTxSavepoint{fieldTxDriver: fieldTxDriver{tx}, ctx: ctx}, nil
}
func (d *fieldTxDriver) Exec(ctx context.Context, query string, args, out any) error {
	values, _ := args.([]any)
	tag, err := d.tx.Exec(ctx, query, values...)
	if err != nil {
		return err
	}
	if result, ok := out.(*sql.Result); ok {
		*result = fieldResult(tag.RowsAffected())
	}
	return nil
}
func (d *fieldTxDriver) Query(ctx context.Context, query string, args, out any) error {
	values, _ := args.([]any)
	rows, err := d.tx.Query(ctx, query, values...)
	if err != nil {
		return err
	}
	// pgx can report a statement/trigger error only when the result is consumed.
	// Propagate it before Ent mistakes a missing RowDescription for a scan error.
	if len(rows.FieldDescriptions()) == 0 {
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	result, ok := out.(*entsql.Rows)
	if !ok {
		rows.Close()
		return fmt.Errorf("unsupported Ent row receiver %T", out)
	}
	result.ColumnScanner = &fieldRows{Rows: rows}
	return nil
}

type fieldTxSavepoint struct {
	fieldTxDriver
	ctx context.Context
}

func (d *fieldTxSavepoint) Commit() error   { return d.tx.Commit(d.ctx) }
func (d *fieldTxSavepoint) Rollback() error { return d.tx.Rollback(d.ctx) }

type fieldResult int64

func (r fieldResult) LastInsertId() (int64, error) { return 0, fmt.Errorf("PostgreSQL uses RETURNING") }
func (r fieldResult) RowsAffected() (int64, error) { return int64(r), nil }

type fieldRows struct{ pgx.Rows }

func (r *fieldRows) Close() error { r.Rows.Close(); return nil }
func (r *fieldRows) Columns() ([]string, error) {
	fields := r.FieldDescriptions()
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names, nil
}
func (r *fieldRows) ColumnTypes() ([]*sql.ColumnType, error) { return nil, nil }
func (r *fieldRows) NextResultSet() bool                     { return false }
