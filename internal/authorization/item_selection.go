package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/jackc/pgx/v5"
)

type SelectionTransaction interface {
	protectedaudit.Executor
	QueryRow(context.Context, string, ...any) pgx.Row
}

type capturedItemSourceKey struct{}
type capturedItemSource struct {
	kind string
	id   int64
}

// WithCapturedItemSource is for server-owned copy commands, never request input.
// Copying an already-authorized commercial snapshot is not catalog selection.
func WithCapturedItemSource(ctx context.Context, kind string, id int64) context.Context {
	return context.WithValue(ctx, capturedItemSourceKey{}, capturedItemSource{kind, id})
}

// CheckItemSelections runs inside the commercial effect transaction. The shared
// mutex orders grant/session changes and Item maintenance before any selected
// input is accepted. Existing captures are counted, never repriced or compared
// with today's catalog fields; appending a second line is a new selection.
func (s *Service) CheckItemSelections(ctx context.Context, a Actor, tx SelectionTransaction, kind string, id int64, next string) error {
	table := map[string]string{"job": "jobs", "estimate": "estimates", "invoice": "invoices"}[kind]
	if table == "" {
		return errors.New("unsupported selection target")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	p, err := s.Principal(ctx, a)
	if err != nil {
		return err
	}
	previous := "[]"
	if source, ok := ctx.Value(capturedItemSourceKey{}).(capturedItemSource); ok && id == 0 {
		sourceTable := map[string]string{"job": "jobs", "estimate": "estimates", "invoice": "invoices"}[source.kind]
		if sourceTable == "" || source.id <= 0 {
			return ErrConflict
		}
		if err := tx.QueryRow(ctx, `SELECT line_items::text FROM `+sourceTable+` WHERE id=$1 FOR SHARE`, source.id).Scan(&previous); err != nil {
			return err
		}
	}
	if id > 0 {
		if err := tx.QueryRow(ctx, `SELECT line_items::text FROM `+table+` WHERE id=$1 FOR UPDATE`, id).Scan(&previous); err != nil {
			return err
		}
	}
	counts := func(raw string) (map[int64]int, error) {
		var lines []struct {
			ItemID int64 `json:"item_id"`
		}
		if err := json.Unmarshal([]byte(raw), &lines); err != nil {
			return nil, err
		}
		out := map[int64]int{}
		for _, line := range lines {
			if line.ItemID < 0 {
				return nil, errors.New("invalid Item provenance")
			}
			if line.ItemID > 0 {
				out[line.ItemID]++
			}
		}
		return out, nil
	}
	old, err := counts(previous)
	if err != nil {
		return err
	}
	current, err := counts(next)
	if err != nil {
		return err
	}
	ids := []int64{}
	for itemID, n := range current {
		if n > old[itemID] {
			ids = append(ids, itemID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, itemID := range ids {
		if !p.Allows("item.view", All) || !p.Allows("pricebook.view", All) {
			return ErrForbidden
		}
		var active bool
		err := tx.QueryRow(ctx, `SELECT is_active AND deleted_at IS NULL FROM items WHERE id=$1 AND (company_id=$2 OR company_id IS NULL) FOR SHARE`, itemID, p.CompanyID).Scan(&active)
		if err == pgx.ErrNoRows || err == nil && !active {
			return fmt.Errorf("%w: Item selection no longer available", ErrConflict)
		}
		if err != nil {
			return err
		}
		channel := a.Channel
		if channel == "" {
			channel = "web"
		}
		if err := protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: a.UserID, RoleID: p.RoleID, Action: "pricebook.view", Scope: "A", TargetType: "item", TargetID: itemID, Channel: channel, SessionHash: a.SessionHash, Detail: map[string]any{"selection_target": kind, "existing_target_id": id}}); err != nil {
			return err
		}
	}
	return nil
}
