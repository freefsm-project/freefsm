package v1

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type activityAPICursor struct {
	Time   time.Time `json:"time"`
	ID     int64     `json:"id"`
	Filter string    `json:"filter"`
}

func (h *handler) activity(typ string) http.HandlerFunc {
	return h.activityHistory(typ, false)
}

func (h *handler) activityHistory(typ string, provenance bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authorization.FromContext(r.Context())
		if !ok {
			writeError(w, 503, "unavailable", "authorization unavailable")
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 404, "not_found", "not found")
			return
		}
		f := authorization.ActivityFilter{Types: []string{typ}, RecordID: id}
		if provenance {
			p, err := auth.Service.Principal(r.Context(), auth.Actor)
			if err != nil {
				commentAPIError(w, err)
				return
			}
			root, err := conversion.New(auth.Service.Pool).ActivityEstimateID(r.Context(), conversion.Actor{ID: p.UserID, CompanyID: p.CompanyID}, objectref.New(objectref.Type(typ), id))
			if err != nil {
				commercialError(w, err)
				return
			}
			f = authorization.ActivityFilter{ProvenanceEstimateID: root}
		}
		limit := 50
		for key, values := range r.URL.Query() {
			if len(values) != 1 {
				writeError(w, 400, "invalid_filter", "each filter must have one value")
				return
			}
			value := values[0]
			switch key {
			case "limit":
				limit, err = strconv.Atoi(value)
				if err != nil || limit < 1 || limit > 200 {
					writeError(w, 400, "invalid_filter", "limit must be 1 to 200")
					return
				}
			case "actor":
				f.ActorID, err = strconv.ParseInt(value, 10, 64)
				if err != nil || f.ActorID <= 0 {
					writeError(w, 400, "invalid_filter", "invalid actor")
					return
				}
			case "action":
				f.Actions = []string{value}
			case "from", "until":
				stamp, err := time.Parse(time.RFC3339Nano, value)
				if err != nil {
					writeError(w, 400, "invalid_filter", "dates require offset instants")
					return
				}
				if key == "from" {
					f.From = &stamp
				} else {
					f.Until = &stamp
				}
			case "cursor":
			default:
				writeError(w, 400, "invalid_filter", "unknown filter")
				return
			}
		}
		if err := authorization.ValidateActivityFilter(f); err != nil {
			writeError(w, 400, "invalid_filter", "invalid activity filters")
			return
		}
		raw, _ := json.Marshal(f)
		sum := sha256.Sum256(raw)
		fingerprint := hex.EncodeToString(sum[:])
		var cursor activityAPICursor
		if value := r.URL.Query().Get("cursor"); value != "" {
			if len(value) > 1024 {
				writeError(w, 400, "invalid_cursor", "invalid activity cursor")
				return
			}
			b, err := base64.RawURLEncoding.DecodeString(value)
			if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.ID <= 0 || cursor.Time.IsZero() || cursor.Filter != fingerprint {
				writeError(w, 400, "invalid_cursor", "invalid activity cursor")
				return
			}
		}
		events := []authorization.ActivityRecord{}
		err = auth.Service.WithinActor(r.Context(), auth.Actor, func(tx pgx.Tx, p authorization.Principal) error {
			_, job, _, archived, err := authorization.ActivityTarget(r.Context(), tx, p, typ, id)
			if err != nil {
				return err
			}
			if archived || job == 0 {
				return authorization.ErrNotFound
			}
			var active bool
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM jobs WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL)`, job, p.CompanyID).Scan(&active); err != nil {
				return err
			}
			if !active {
				return authorization.ErrNotFound
			}
			return authorization.WalkActivity(r.Context(), tx, p, f, false, func(e authorization.ActivityRecord) (bool, error) {
				if cursor.ID > 0 && !(e.Accepted.Before(cursor.Time) || e.Accepted.Equal(cursor.Time) && e.ID < cursor.ID) {
					return true, nil
				}
				events = append(events, e)
				return len(events) <= limit, nil
			})
		})
		if err != nil {
			commentAPIError(w, err)
			return
		}
		next := ""
		if len(events) > limit {
			events = events[:limit]
			last := events[len(events)-1]
			b, _ := json.Marshal(activityAPICursor{last.Accepted, last.ID, fingerprint})
			next = base64.RawURLEncoding.EncodeToString(b)
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, struct {
			Records []authorization.ActivityRecord `json:"records"`
			Next    string                         `json:"next_cursor,omitempty"`
		}{events, next})
	}
}
