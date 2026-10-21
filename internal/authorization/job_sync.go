package authorization

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrSyncResnapshot = errors.New("synchronization requires a new snapshot")

var errSyncAuthorityChanged = errors.New("synchronization authority changed")

type SyncRequest struct {
	Cursor, Page, Scope string
	PageSize            int
}
type SyncPage struct {
	Records          []map[string]any `json:"records"`
	Removed          []string         `json:"removed"`
	NextPage         *string          `json:"next_page"`
	CompletionCursor *string          `json:"completion_cursor"`
	Watermark        time.Time        `json:"watermark"`
	Complete         bool             `json:"complete"`
	Purge            string           `json:"purge"`
}

// Keep existing Job callers compatible while the protocol serves independent
// authorized projections. The historical table/token identities stay unchanged.
type JobSyncRequest = SyncRequest
type JobSyncPage = SyncPage
type syncSnapshot struct {
	ID, Grants        string
	Baseline, Changes []syncEntry
	Removed           []string
	Watermark         time.Time
}

// SyncJobs materializes protocol state atomically with its tokens. There is no
// domain mutation or fabricated domain audit for these read-only snapshots.
func (s *Service) SyncJobs(ctx context.Context, a Actor, in JobSyncRequest) (JobSyncPage, error) {
	if in.PageSize < 1 || in.PageSize > 200 || (in.Scope != "assigned" && in.Scope != "all") || a.SessionHash == "" {
		return JobSyncPage{}, ErrSyncResnapshot
	}
	return s.syncRecords(ctx, a, in, "job.view", Assigned, syncReadModel)
}

// syncRecords shares durable pagination and continuity, not domain authority.
// Each projection supplies its own permission, scope namespace and closed reader.
func (s *Service) syncRecords(ctx context.Context, a Actor, in SyncRequest, permission string, scope Scope, read func(context.Context, pgx.Tx, Principal, Actor, string) ([]syncEntry, error)) (SyncPage, error) {
	out := SyncPage{Records: []map[string]any{}, Removed: []string{}, Purge: "none"}
	resnapshot := false
	err := s.WithinSnapshotPermission(ctx, a, permission, scope, func(tx pgx.Tx, p Principal) error {
		invalidate := func() error {
			resnapshot = true
			// Return nil on success so the invalidation COMMITS before the typed
			// recovery response. Returning ErrSyncResnapshot here would roll back.
			return syncInvalidate(ctx, tx, a)
		}
		var epoch string
		if err := tx.QueryRow(ctx, `SELECT epoch::text FROM session_instance WHERE singleton`).Scan(&epoch); err != nil {
			return err
		}
		grants, err := json.Marshal(p.Grants)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(grants)
		fingerprint := hex.EncodeToString(sum[:])
		current, err := read(ctx, tx, p, a, in.Scope)
		if err != nil {
			return err
		}
		var snapshot syncSnapshot
		position := 0
		if in.Page != "" {
			snapshot, position, err = syncLoad(ctx, tx, a, in, epoch, fingerprint, in.Page, "page")
			if errors.Is(err, errSyncAuthorityChanged) {
				return invalidate()
			}
			if err != nil {
				return err
			}
		} else {
			var previous *syncSnapshot
			if in.Cursor != "" {
				base, _, err := syncLoad(ctx, tx, a, in, epoch, fingerprint, in.Cursor, "cursor")
				if errors.Is(err, errSyncAuthorityChanged) {
					return invalidate()
				}
				if err != nil {
					return err
				}
				previous = &base
				if !syncFileParentsRetained(base.Baseline, current) {
					return invalidate()
				}
				// A new delta must not let a client keep a now-inaccessible facet
				// from its completed baseline while later pages are outstanding.
				anchors := make(map[string]string, len(current))
				for _, entry := range current {
					anchors[entry.ID] = entry.Anchor
				}
				for _, entry := range base.Baseline {
					if anchor, visible := anchors[entry.ID]; !visible || anchor != entry.Anchor {
						return invalidate()
					}
				}
				// A cursor has exactly one durable successor. Lost responses and retries
				// never promote it to a newer baseline, even after later domain edits.
				var first string
				err = tx.QueryRow(ctx, `SELECT t.token FROM job_sync_snapshots s JOIN job_sync_tokens t ON t.snapshot_id=s.id AND t.kind='page' AND t.position=0 WHERE s.previous_cursor=$1`, in.Cursor).Scan(&first)
				if err == nil {
					snapshot, position, err = syncLoad(ctx, tx, a, in, epoch, fingerprint, first, "page")
					if errors.Is(err, errSyncAuthorityChanged) {
						return invalidate()
					}
					if err != nil {
						return err
					}
				}
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			}
			if snapshot.ID == "" {
				snapshot, err = syncCreate(ctx, tx, a, in, epoch, fingerprint, current, previous)
				if err != nil {
					return err
				}
			}
		}
		visible := map[string]syncEntry{}
		for _, e := range current {
			visible[e.ID] = e
		}
		if len(snapshot.Removed) != 0 {
			return invalidate()
		}
		if !syncFileParentsRetained(snapshot.Baseline, current) {
			return invalidate()
		}
		// Recheck the entire baseline, including earlier pages. A relationship loss
		// must not leave already-delivered sensitive facets in a conforming cache.
		for _, e := range snapshot.Baseline {
			now, ok := visible[e.ID]
			if !ok || now.Anchor != e.Anchor {
				return invalidate()
			}
		}
		end := position + in.PageSize
		if end > len(snapshot.Changes) {
			end = len(snapshot.Changes)
		}
		if position < 0 || position > len(snapshot.Changes) {
			return ErrSyncResnapshot
		}
		for _, e := range snapshot.Changes[position:end] {
			if _, ok := visible[e.ID]; ok {
				out.Records = append(out.Records, e.Data)
			}
		}
		out.Watermark = snapshot.Watermark
		out.Complete = end == len(snapshot.Changes)
		var token string
		kind := "page"
		nextPosition := end
		if out.Complete {
			kind = "cursor"
			nextPosition = 0
		}
		if err := tx.QueryRow(ctx, `SELECT token FROM job_sync_tokens WHERE snapshot_id=$1 AND kind=$2 AND position=$3`, snapshot.ID, kind, nextPosition).Scan(&token); err != nil {
			return err
		}
		if out.Complete {
			out.CompletionCursor = &token
		} else {
			out.NextPage = &token
		}
		return nil
	})
	if errors.Is(err, ErrForbidden) {
		// Missing projection View or a denied requested scope also instructs
		// cache purge. Recheck current identity/session under the existing fence
		// before recording that observation; denial grants no domain access.
		if invalidateErr := s.WithinActor(ctx, a, func(tx pgx.Tx, _ Principal) error {
			return syncInvalidate(ctx, tx, a)
		}); invalidateErr != nil {
			return JobSyncPage{}, invalidateErr
		}
	}
	if err != nil {
		return JobSyncPage{}, err
	}
	if resnapshot {
		return JobSyncPage{}, ErrSyncResnapshot
	}
	return out, nil
}

// A purge affects the client's cache, not merely this page's baseline. Invalidate
// every existing snapshot for this client, including already materialized and
// nested successors, under the shared authority fence. Baselines remain intact.
// Invalid-token replay never calls this function, so fresh recovery stays valid.
func syncInvalidate(ctx context.Context, tx pgx.Tx, a Actor) error {
	_, err := tx.Exec(ctx, `UPDATE job_sync_snapshots SET invalidated=true WHERE user_id=$1 AND session_hash=$2 AND NOT invalidated`, a.UserID, a.SessionHash)
	return err
}

func syncLoad(ctx context.Context, tx pgx.Tx, a Actor, in JobSyncRequest, epoch, grants, token, kind string) (syncSnapshot, int, error) {
	var s syncSnapshot
	var baseline, changes, removed []byte
	var position int
	err := tx.QueryRow(ctx, `SELECT s.id,s.grants_hash,s.baseline,s.changes,s.removed,s.watermark,t.position FROM job_sync_tokens t JOIN job_sync_snapshots s ON s.id=t.snapshot_id WHERE t.token=$1 AND t.kind=$2 AND s.user_id=$3 AND s.session_hash=$4 AND s.epoch=$5 AND s.scope=$6 AND s.page_size=$7 AND s.expires_at>clock_timestamp() AND NOT s.invalidated`, token, kind, a.UserID, a.SessionHash, epoch, in.Scope, in.PageSize).Scan(&s.ID, &s.Grants, &baseline, &changes, &removed, &s.Watermark, &position)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, 0, ErrSyncResnapshot
	}
	if err != nil {
		return s, 0, err
	}
	if s.Grants != grants {
		return s, 0, errSyncAuthorityChanged
	}
	decoder := json.NewDecoder(bytes.NewReader(baseline))
	decoder.UseNumber()
	if err = decoder.Decode(&s.Baseline); err != nil {
		return s, 0, err
	}
	decoder = json.NewDecoder(bytes.NewReader(changes))
	decoder.UseNumber()
	if err = decoder.Decode(&s.Changes); err != nil {
		return s, 0, err
	}
	if err = json.Unmarshal(removed, &s.Removed); err != nil {
		return s, 0, err
	}
	return s, position, nil
}

func syncCreate(ctx context.Context, tx pgx.Tx, a Actor, in JobSyncRequest, epoch, grants string, current []syncEntry, previous *syncSnapshot) (syncSnapshot, error) {
	s := syncSnapshot{ID: uuid.NewString(), Grants: grants, Baseline: current, Changes: []syncEntry{}, Removed: []string{}}
	old := map[string]syncEntry{}
	if previous != nil {
		for _, e := range previous.Baseline {
			old[e.ID] = e
		}
	}
	for _, e := range current {
		prior, ok := old[e.ID]
		before, err := json.Marshal(prior.Data)
		if err != nil {
			return s, err
		}
		after, err := json.Marshal(e.Data)
		if err != nil {
			return s, err
		}
		if !ok || !bytes.Equal(before, after) {
			s.Changes = append(s.Changes, e)
		}
		delete(old, e.ID)
	}
	for id := range old {
		s.Removed = append(s.Removed, id)
	}
	sort.Strings(s.Removed)
	baseline, err := json.Marshal(s.Baseline)
	if err != nil {
		return s, err
	}
	changes, err := json.Marshal(s.Changes)
	if err != nil {
		return s, err
	}
	removed, err := json.Marshal(s.Removed)
	if err != nil {
		return s, err
	}
	var cursor any
	if in.Cursor != "" {
		cursor = in.Cursor
	}
	err = tx.QueryRow(ctx, `INSERT INTO job_sync_snapshots(id,user_id,session_hash,epoch,scope,page_size,grants_hash,baseline,changes,removed,previous_cursor) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING watermark`, s.ID, a.UserID, a.SessionHash, epoch, in.Scope, in.PageSize, grants, baseline, changes, removed, cursor).Scan(&s.Watermark)
	if err != nil {
		return s, err
	}
	for pos := 0; pos == 0 || pos < len(s.Changes); pos += in.PageSize {
		if _, err = tx.Exec(ctx, `INSERT INTO job_sync_tokens(token,snapshot_id,kind,position) VALUES($1,$2,'page',$3)`, uuid.NewString(), s.ID, pos); err != nil {
			return s, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO job_sync_tokens(token,snapshot_id,kind,position) VALUES($1,$2,'cursor',0)`, uuid.NewString(), s.ID)
	return s, err
}
