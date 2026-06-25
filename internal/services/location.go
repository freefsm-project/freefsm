package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/ent/location"
)

type LocationService struct {
	client *ent.Client
}

func NewLocationService(client *ent.Client) *LocationService {
	return &LocationService{client: client}
}

func (s *LocationService) ListAll(ctx context.Context) ([]*ent.Location, error) {
	return s.client.Location.Query().Order(ent.Asc(location.FieldTitle)).All(ctx)
}

func (s *LocationService) ListByIDs(ctx context.Context, ids []int64) ([]*ent.Location, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return s.client.Location.Query().Where(location.IDIn(ids...)).All(ctx)
}

func (s *LocationService) GetByID(ctx context.Context, id int64) (*ent.Location, error) {
	l, err := s.client.Location.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get location %d: %w", id, err)
	}
	return l, nil
}

func (s *LocationService) Geocode(ctx context.Context, l *ent.Location, geocoderURL string) (*ent.Location, error) {
	if l == nil || geocoderURL == "" || strings.TrimSpace(locationAddress(l)) == "" {
		return l, nil
	}
	if l.Latitude != nil && l.Longitude != nil {
		return l, nil
	}

	reqURL, err := url.Parse(geocoderURL + "/search")
	if err != nil {
		return l, fmt.Errorf("parse geocoder URL: %w", err)
	}
	q := reqURL.Query()
	q.Set("q", locationAddress(l))
	q.Set("format", "jsonv2")
	q.Set("limit", "1")
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return l, fmt.Errorf("create geocode request: %w", err)
	}
	req.Header.Set("User-Agent", "freefsm/1.0")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return l, fmt.Errorf("geocode location: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return l, fmt.Errorf("geocode location: status %d", resp.StatusCode)
	}

	var results []struct {
		Lat string `json:"lat"`
		Lon string `json:"lon"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return l, fmt.Errorf("decode geocode response: %w", err)
	}
	if len(results) == 0 {
		return l, nil
	}
	lat, err := strconv.ParseFloat(results[0].Lat, 64)
	if err != nil {
		return l, fmt.Errorf("parse geocode latitude: %w", err)
	}
	lng, err := strconv.ParseFloat(results[0].Lon, 64)
	if err != nil {
		return l, fmt.Errorf("parse geocode longitude: %w", err)
	}

	updated, err := s.client.Location.UpdateOneID(l.ID).
		SetLatitude(lat).
		SetLongitude(lng).
		SetGeocodedAt(time.Now()).
		SetGeocodeSource(geocoderURL).
		Save(ctx)
	if err != nil {
		return l, fmt.Errorf("save geocode result: %w", err)
	}
	return updated, nil
}

func locationAddress(l *ent.Location) string {
	parts := []string{l.Address1, l.Address2, l.City, l.State, l.ZipCode}
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, ", ")
}
