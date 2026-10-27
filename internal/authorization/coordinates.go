package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Coordinates struct {
	ID                  int64
	Revision, Address   string
	Latitude, Longitude *float64
	Proposal            bool
	Archived, ReadOnly  bool
}

func (s *Service) LocationCoordinates(ctx context.Context, a Actor, id int64) (Coordinates, error) {
	v := Coordinates{ID: id}
	p, err := s.Principal(ctx, a)
	if err != nil {
		return v, err
	}
	if !p.Allows("location.view", All) {
		return v, ErrForbidden
	}
	err = s.Pool.QueryRow(ctx, `SELECT l.xmin::text,concat_ws(', ',nullif(l.address_1,''),nullif(l.address_2,''),nullif(l.city,''),nullif(l.state,''),nullif(l.zip_code,'')),l.latitude,l.longitude,l.deleted_at IS NOT NULL,l.deleted_at IS NOT NULL OR c.deleted_at IS NOT NULL FROM locations l JOIN customers c ON l.object_type='customer' AND c.id=l.object_id AND c.company_id=l.company_id WHERE l.id=$1 AND l.company_id=$2 AND (l.deleted_at IS NULL OR $3)`, id, p.CompanyID, p.Allows("location.archived.view", All)).Scan(&v.Revision, &v.Address, &v.Latitude, &v.Longitude, &v.Archived, &v.ReadOnly)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return v, err
}

var ErrGeocoder = errors.New("geocoder unavailable; saved address and coordinates are unchanged; manual entry remains available")

// LookupCoordinates returns a proposal only. The reviewed revision travels with
// it, so even a slow response cannot overwrite a later address or coordinate edit.
func (s *Service) LookupCoordinates(ctx context.Context, a Actor, id int64, revision string, operatorDefault ...string) (Coordinates, error) {
	v, err := s.LocationCoordinates(ctx, a, id)
	if err != nil {
		return v, err
	}
	p, err := s.Principal(ctx, a)
	if err != nil {
		return v, err
	}
	if !p.Allows("location.edit", All) {
		return v, ErrForbidden
	}
	if v.ReadOnly {
		return v, ErrConflict
	}
	if revision != v.Revision {
		return v, ErrConflict
	}
	var provider string
	if err := s.Pool.QueryRow(ctx, `SELECT geocoder_url FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&provider); err != nil {
		return v, err
	}
	if provider == "" && len(operatorDefault) > 0 {
		provider = operatorDefault[0]
	}
	u, err := url.Parse(provider)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || (u.Scheme != "https" && u.Scheme != "http") || v.Address == "" {
		return v, ErrGeocoder
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/search") {
		u.Path += "/search"
	}
	u.RawQuery = url.Values{"q": {v.Address}, "format": {"jsonv2"}, "limit": {"1"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return v, ErrGeocoder
	}
	req.Header.Set("User-Agent", "FreeFSM/1")
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return v, ErrGeocoder
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return v, ErrGeocoder
	}
	var results []struct {
		Lat string `json:"lat"`
		Lon string `json:"lon"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&results); err != nil || len(results) == 0 {
		return v, ErrGeocoder
	}
	lat, e1 := strconv.ParseFloat(results[0].Lat, 64)
	lon, e2 := strconv.ParseFloat(results[0].Lon, 64)
	if e1 != nil || e2 != nil || !validCoordinates(lat, lon) {
		return v, ErrGeocoder
	}
	// Recheck visibility and address after provider I/O, before disclosing a result.
	current, err := s.LocationCoordinates(ctx, a, id)
	if err != nil {
		return v, err
	}
	p, err = s.Principal(ctx, a)
	if err != nil {
		return v, err
	}
	if !p.Allows("location.edit", All) {
		return v, ErrForbidden
	}
	if current.ReadOnly || current.Revision != revision {
		return v, ErrConflict
	}
	v.Latitude = &lat
	v.Longitude = &lon
	v.Proposal = true
	return v, nil
}

func validCoordinates(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

func (s *Service) SaveCoordinates(ctx context.Context, a Actor, id int64, revision string, lat, lon float64) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows("location.edit", All) {
			return ErrForbidden
		}
		if !validCoordinates(lat, lon) {
			return ErrIntegrationInvalid
		}
		var current string
		if err := tx.QueryRow(ctx, `SELECT l.xmin::text FROM locations l JOIN customers c ON l.object_type='customer' AND c.id=l.object_id AND c.company_id=l.company_id WHERE l.id=$1 AND l.company_id=$2 AND l.deleted_at IS NULL AND c.deleted_at IS NULL FOR UPDATE OF l FOR SHARE OF c`, id, p.CompanyID).Scan(&current); err != nil {
			return ErrNotFound
		}
		if current != revision {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE locations SET latitude=$2,longitude=$3,geocoded_at=NULL,geocode_source=NULL WHERE id=$1`, id, lat, lon); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "location.edit", All, "location", id, map[string]any{"action": "reviewed_coordinates", "latitude": lat, "longitude": lon})
	})
}
