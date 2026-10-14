package exports

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// Download is a verified, seekable, private transfer preparation. Close always
// removes temporary bytes; no filesystem path is exposed to the requester.
type Download struct{ *os.File }

func (d *Download) Close() error {
	name := d.Name()
	err := d.File.Close()
	_ = os.Remove(name)
	return err
}

// PrepareDownload checks every stored authorization reference under the shared
// authority fence and verifies all bytes before committing download audit. A
// successful audit means authorized transfer preparation, not proof of receipt.
func (s *Service) PrepareDownload(ctx context.Context, a authorization.Actor, id int64) (*Download, error) {
	var result *Download
	var denied bool
	err := authorization.New(s.Pool).WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		var state, kind string
		var options json.RawMessage
		var expired, blocked bool
		var expected []byte
		var count int64
		err := tx.QueryRow(ctx, `SELECT state,kind,coalesce(clock_timestamp()>=ready_at+interval '24 hours',false),blocked,coalesce(digest,''::bytea),coalesce(row_count,0),options FROM export_jobs WHERE id=$1 AND requester_id=$2 AND company_id=$3 FOR UPDATE`, id, a.UserID, p.CompanyID).Scan(&state, &kind, &expired, &blocked, &expected, &count, &options)
		if errors.Is(err, pgx.ErrNoRows) {
			return authorization.ErrNotFound
		}
		if err != nil {
			return err
		}
		if state != "Ready" {
			return ErrNotReady
		}
		if expired || blocked {
			return ErrUnavailable
		}
		d, ok := s.definitions[kind]
		if !ok {
			return ErrUnavailable
		}
		block := func() error {
			denied = true
			_, err := tx.Exec(ctx, `UPDATE export_jobs SET blocked=true WHERE id=$1`, id)
			return err
		}
		if !p.Allows(d.Permission(), d.Scope()) {
			return block()
		}
		if err := authorizeDefinition(ctx, tx, p, d, options); err != nil {
			if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) {
				return block()
			}
			return err
		}
		file, err := os.CreateTemp("", "freefsm-export-*")
		if err != nil {
			return err
		}
		result = &Download{file}
		hash := sha256.New()
		sink := io.MultiWriter(file, hash)
		for ordinal := int64(0); ordinal <= count; ordinal++ {
			var ref json.RawMessage
			var data []byte
			if err := tx.QueryRow(ctx, `SELECT reference,csv FROM export_records WHERE export_id=$1 AND ordinal=$2`, id, ordinal).Scan(&ref, &data); err != nil {
				return err
			}
			if ordinal > 0 {
				if err := d.Recheck(ctx, tx, p, ref); err != nil {
					if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) {
						return block()
					}
					return err
				}
			}
			if _, err := sink.Write(data); err != nil {
				return err
			}
		}
		if !bytes.Equal(hash.Sum(nil), expected) {
			return errors.New("export integrity check failed")
		}
		if err := file.Sync(); err != nil {
			return err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := exportAudit(ctx, tx, a, p, d, id, "downloaded"); err != nil {
			return err
		}
		// Preparation, including audit, might cross the boundary. Reject and roll
		// back its audit rather than extend expiry by starting the request early.
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()>=ready_at+interval '24 hours' FROM export_jobs WHERE id=$1`, id).Scan(&expired); err != nil {
			return err
		}
		if expired {
			return ErrUnavailable
		}
		return nil
	})
	if denied && err == nil {
		err = ErrUnavailable
	}
	if err != nil && result != nil {
		result.Close()
		result = nil
	}
	return result, err
}
