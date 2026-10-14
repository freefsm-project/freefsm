package exports

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/jackc/pgx/v5"
)

// ProcessOne recovers Running work as well as Pending work. A session advisory
// lock elects one worker across processes; disconnect releases it automatically.
// Ready publication, all bytes/references, totals and generation audit are atomic.
func (s *Service) ProcessOne(ctx context.Context) (bool, error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(960096)`).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock(960096)`); err != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	var id, user int64
	var kind string
	var options json.RawMessage
	err = conn.QueryRow(ctx, `UPDATE export_jobs SET state='Running' WHERE id=(SELECT id FROM export_jobs WHERE state IN ('Pending','Running') ORDER BY id LIMIT 1) RETURNING id,requester_id,kind,options`).Scan(&id, &user, &kind, &options)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	d, ok := s.definitions[kind]
	if !ok {
		err = ErrUnavailable
	} else {
		actor := authorization.Actor{UserID: user, Channel: "worker"}
		err = authorization.New(s.Pool).WithinSnapshotPermission(ctx, actor, d.Permission(), d.Scope(), func(tx pgx.Tx, p authorization.Principal) error {
			if err := authorizeDefinition(ctx, tx, p, d, options); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE export_jobs SET as_of=transaction_timestamp() WHERE id=$1`, id); err != nil {
				return err
			}
			hash := sha256.New()
			var count int64
			write := func(ordinal int64, ref json.RawMessage, values []string) error {
				var b bytes.Buffer
				writer := csv.NewWriter(&b)
				if err := writer.Write(values); err != nil {
					return err
				}
				writer.Flush()
				if err := writer.Error(); err != nil {
					return err
				}
				hash.Write(b.Bytes())
				_, err := tx.Exec(ctx, `INSERT INTO export_records(export_id,ordinal,reference,csv) VALUES($1,$2,$3,$4)`, id, ordinal, ref, b.Bytes())
				return err
			}
			if err := write(0, json.RawMessage(`{}`), d.Columns()); err != nil {
				return err
			}
			if err := d.Snapshot(ctx, tx, p, options, func(row Record) error {
				if len(row.Values) != len(d.Columns()) || !json.Valid(row.Reference) {
					return errors.New("invalid export record")
				}
				values := make([]string, len(row.Values))
				for i, v := range row.Values {
					values[i] = safeCell(v)
				}
				count++
				return write(count, row.Reference, values)
			}); err != nil {
				return err
			}
			if err := exportAudit(ctx, tx, actor, p, d, id, "generated"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE export_jobs SET state='Ready',ready_at=clock_timestamp(),row_count=$2,digest=$3 WHERE id=$1`, id, count, hash.Sum(nil)); err != nil {
				return err
			}
			return nil
		})
	}
	if err != nil {
		// A disconnected worker leaves Running recoverable. A live worker records
		// failure without publishing partial bytes. Never expose raw dependency errors.
		if ctx.Err() != nil {
			return true, err
		}
		_, saveErr := conn.Exec(ctx, `UPDATE export_jobs SET state='Failed',failure='Preparation failed. Check service availability, then generate a new export.' WHERE id=$1 AND state='Running'`, id)
		if saveErr != nil {
			return true, saveErr
		}
		slog.Warn("export generation failed", "export_id", id)
	}
	return true, err
}

// Text escaping is explicit and reversible: prefix formula-leading cells (even
// behind whitespace/control characters), and double an existing leading quote.
// No spreadsheet formula wrappers, trimming, or hidden business-value changes.
func safeCell(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	if strings.HasPrefix(value, "'") || strings.ContainsAny(firstRune(trimmed), "=+-@") {
		return "'" + value
	}
	return value
}
func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// Run participates in the same maintenance admission as HTTP, backup and restore.
func (s *Service) Run(ctx context.Context, control *instancecontrol.Control, refresh func() error) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		workCtx := ctx
		release := func() {}
		var err error
		if control != nil {
			workCtx, release, err = control.Enter(ctx)
		}
		if err == nil {
			if refresh != nil {
				err = refresh()
			}
			if err == nil {
				_, err = s.ProcessOne(workCtx)
			}
			release()
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("export worker unavailable")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
