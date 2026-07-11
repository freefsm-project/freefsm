package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
}

func Connect(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{Pool: pool}, nil
}

func (db *DB) Close() {
	db.Pool.Close()
}

func (db *DB) Migrate(ctx context.Context, migrationsFS fs.FS) error {
	entries, err := fs.ReadDir(migrationsFS, ".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	if err := db.ensureMigrationsTable(ctx); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}

	for _, f := range files {
		name := strings.TrimSuffix(f, ".up.sql")
		var applied bool
		err := db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name = $1)", name).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		content, err := fs.ReadFile(migrationsFS, f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}

		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}

		if f == "042_normalized_invoice_settlement.up.sql" {
			// Maintenance cutover: materialize legacy tenancy before reading settlement data.
			if err := db.materializeLegacyTenancy(ctx, tx); err != nil {
				tx.Rollback(ctx)
				return fmt.Errorf("settlement migration tenancy: %w", err)
			}
			if err := db.preflightSettlement(ctx, tx); err != nil {
				tx.Rollback(ctx)
				return fmt.Errorf("settlement migration preflight: %w", err)
			}
		}

		if _, err := tx.Exec(ctx, string(content)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("execute migration %s: %w", f, err)
		}

		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (name) VALUES ($1)", name); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", name, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", f, err)
		}
	}

	return nil
}

func (db *DB) materializeLegacyTenancy(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `LOCK TABLE companies IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock companies: %w", err)
	}

	rows, err := tx.Query(ctx, `SELECT c.table_name
		FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema=c.table_schema AND t.table_name=c.table_name
		WHERE c.table_schema=current_schema() AND c.column_name='company_id' AND t.table_type='BASE TABLE'
		ORDER BY c.table_name`)
	if err != nil {
		return fmt.Errorf("discover tenant-owned tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return fmt.Errorf("scan tenant-owned table: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("discover tenant-owned tables: %w", err)
	}
	rows.Close()
	for _, table := range tables {
		if _, err := tx.Exec(ctx, `LOCK TABLE `+pgx.Identifier{table}.Sanitize()+` IN ACCESS EXCLUSIVE MODE`); err != nil {
			return fmt.Errorf("lock tenant-owned table %s: %w", table, err)
		}
	}

	var companyCount int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM companies`).Scan(&companyCount); err != nil {
		return fmt.Errorf("count companies: %w", err)
	}
	nullCounts, err := tenantNullCompanyCounts(ctx, tx, tables)
	if err != nil {
		return err
	}
	if companyCount > 1 && len(nullCounts) > 0 {
		return fmt.Errorf("ambiguous tenant ownership with %d companies: %s", companyCount, strings.Join(nullCounts, ", "))
	}

	var companyID int64
	if companyCount == 0 {
		var name string
		if err := tx.QueryRow(ctx, `SELECT coalesce(
			CASE WHEN count(*)=1 THEN max(nullif(trim(business_name),'')) END,
			'FreeFSM') FROM company_settings`).Scan(&name); err != nil {
			return fmt.Errorf("derive legacy company name: %w", err)
		}
		// The companies table is locked and empty, making this stable slug collision-free.
		if err := tx.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES($1,'legacy') RETURNING id`, name).Scan(&companyID); err != nil {
			return fmt.Errorf("create legacy company: %w", err)
		}
	} else if companyCount == 1 {
		if err := tx.QueryRow(ctx, `SELECT id FROM companies`).Scan(&companyID); err != nil {
			return fmt.Errorf("select company: %w", err)
		}
	}

	if companyCount <= 1 {
		for _, table := range tables {
			query := `UPDATE ` + pgx.Identifier{table}.Sanitize() + ` SET company_id=$1 WHERE company_id IS NULL`
			if _, err := tx.Exec(ctx, query, companyID); err != nil {
				return fmt.Errorf("backfill %s.company_id: %w", table, err)
			}
		}
	}
	remaining, err := tenantNullCompanyCounts(ctx, tx, tables)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return fmt.Errorf("tenant ownership remains missing: %s", strings.Join(remaining, ", "))
	}
	return nil
}

func tenantNullCompanyCounts(ctx context.Context, tx pgx.Tx, tables []string) ([]string, error) {
	var diagnostics []string
	for _, table := range tables {
		var count int64
		query := `SELECT count(*) FROM ` + pgx.Identifier{table}.Sanitize() + ` WHERE company_id IS NULL`
		if err := tx.QueryRow(ctx, query).Scan(&count); err != nil {
			return nil, fmt.Errorf("count missing ownership in %s: %w", table, err)
		}
		if count > 0 {
			diagnostics = append(diagnostics, fmt.Sprintf("%s=%d", table, count))
		}
	}
	return diagnostics, nil
}

// preflightSettlement computes totals with the same canonical Money implementation
// used by invoice writes. SQL must not approximate line-item rounding or tax rules.
func (db *DB) preflightSettlement(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS settlement_migration_invoice_totals (
		invoice_id BIGINT PRIMARY KEY REFERENCES invoices(id) ON DELETE CASCADE,
		total_cents BIGINT NOT NULL CHECK(total_cents >= 0))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS settlement_migration_payments (
		invoice_id BIGINT NOT NULL REFERENCES invoices(id) ON DELETE CASCADE, ordinal INTEGER NOT NULL,
		amount_cents BIGINT NOT NULL CHECK(amount_cents > 0), method TEXT NOT NULL, received_date DATE NOT NULL,
		reference TEXT NOT NULL, notes TEXT NOT NULL, PRIMARY KEY(invoice_id,ordinal))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `TRUNCATE settlement_migration_invoice_totals`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `TRUNCATE settlement_migration_payments`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id, line_items::text, tax_rate::text, payments::text FROM invoices ORDER BY id`)
	if err != nil {
		return err
	}
	type legacyInvoice struct {
		id                           int64
		lineItems, taxRate, payments string
	}
	var invoices []legacyInvoice
	for rows.Next() {
		var invoice legacyInvoice
		if err := rows.Scan(&invoice.id, &invoice.lineItems, &invoice.taxRate, &invoice.payments); err != nil {
			return err
		}
		invoices = append(invoices, invoice)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, invoice := range invoices {
		items, err := services.DecodeLineItems(invoice.lineItems)
		if err != nil {
			return fmt.Errorf("invoice %d line items: %w", invoice.id, err)
		}
		totals, err := services.CalculateTotals(items, invoice.taxRate)
		if err != nil {
			return fmt.Errorf("invoice %d total: %w", invoice.id, err)
		}
		if totals.Total.MinorUnits() < 0 {
			return fmt.Errorf("invoice %d has negative total", invoice.id)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO settlement_migration_invoice_totals(invoice_id,total_cents) VALUES($1,$2)`, invoice.id, totals.Total.MinorUnits()); err != nil {
			return err
		}
		payments, err := decodeLegacyPayments(invoice.payments)
		if err != nil {
			return fmt.Errorf("invoice %d payments: %w", invoice.id, err)
		}
		for ordinal, payment := range payments {
			if payment.amountCents <= 0 {
				return fmt.Errorf("invoice %d payment %d has invalid amount", invoice.id, ordinal+1)
			}
			if !contains([]string{"cash", "check", "credit_card", "transfer", "other"}, payment.method) {
				return fmt.Errorf("invoice %d payment %d has unknown method", invoice.id, ordinal+1)
			}
			date, dateErr := time.Parse("2006-01-02", payment.date)
			if dateErr != nil {
				return fmt.Errorf("invoice %d payment %d date: %w", invoice.id, ordinal+1, dateErr)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO settlement_migration_payments VALUES($1,$2,$3,$4,$5,$6,$7)`, invoice.id, ordinal+1, payment.amountCents, payment.method, date, payment.reference, payment.notes); err != nil {
				return err
			}
		}
	}
	return nil
}

type legacyPayment struct {
	id, method, date, reference, notes string
	amountCents                        int64
}

func decodeLegacyPayments(encoded string) ([]legacyPayment, error) {
	if strings.TrimSpace(encoded) == "null" {
		return nil, errors.New("payments JSON must not be null")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &entries); err != nil || entries == nil {
		return nil, errors.New("payments JSON must be an array")
	}
	payments := make([]legacyPayment, 0, len(entries))
	seen := map[string]struct{}{}
	for _, entry := range entries {
		var raw struct {
			ID        string      `json:"id"`
			Amount    json.Number `json:"amount"`
			Method    string      `json:"method"`
			Date      string      `json:"date"`
			Reference string      `json:"reference"`
			Notes     string      `json:"notes"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(entry)))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil || raw.Amount == "" || raw.Method == "" || raw.Date == "" {
			return nil, errors.New("malformed payment object")
		}
		rat, ok := new(big.Rat).SetString(string(raw.Amount))
		if !ok {
			return nil, errors.New("invalid payment amount")
		}
		rat.Mul(rat, big.NewRat(100, 1))
		if !rat.IsInt() || !rat.Num().IsInt64() {
			return nil, errors.New("payment amount must be exact cents")
		}
		if raw.ID != "" {
			if _, exists := seen[raw.ID]; exists {
				return nil, errors.New("duplicate payment id")
			}
			seen[raw.ID] = struct{}{}
		}
		payments = append(payments, legacyPayment{id: raw.ID, method: raw.Method, date: raw.Date, reference: raw.Reference, notes: raw.Notes, amountCents: rat.Num().Int64()})
	}
	return payments, nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (db *DB) ensureMigrationsTable(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`)
	return err
}

func RunMigrationsDir(ctx context.Context, dsn, dir string) error {
	db, err := Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	migrationsFS := os.DirFS(dir)
	return db.Migrate(ctx, migrationsFS)
}

func RunMigrations(ctx context.Context, dsn string, migrationsFS fs.FS) error {
	db, err := Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	return db.Migrate(ctx, migrationsFS)
}

func FindMigrations(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".up.sql") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}
