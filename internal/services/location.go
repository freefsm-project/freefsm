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

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/location"
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

func (s *LocationService) ListByCustomer(ctx context.Context, customerID int64) ([]*ent.Location, error) {
	return s.client.Location.Query().
		Where(location.ObjectTypeEQ("customer"), location.ObjectIDEQ(customerID)).
		Order(ent.Desc(location.FieldIsPrimary), ent.Asc(location.FieldTitle)).
		All(ctx)
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

func (s *LocationService) GetByCustomer(ctx context.Context, customerID, id int64) (*ent.Location, error) {
	l, err := s.client.Location.Query().
		Where(location.IDEQ(id), location.ObjectTypeEQ("customer"), location.ObjectIDEQ(customerID)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("get customer location %d: %w", id, err)
	}
	return l, nil
}

type CustomerLocationCreateParams struct {
	Title     string
	Address1  string
	Address2  string
	City      string
	State     string
	ZipCode   string
	Notes     string
	IsPrimary bool
}

type CustomerLocationUpdateParams struct {
	Title     *string
	Address1  *string
	Address2  *string
	City      *string
	State     *string
	ZipCode   *string
	Notes     *string
	IsPrimary *bool
}

func (s *LocationService) CreateForCustomer(ctx context.Context, customerID int64, params CustomerLocationCreateParams) (*ent.Location, error) {
	if err := validateActiveCustomer(ctx, s.client, customerID); err != nil {
		return nil, err
	}
	l, err := s.client.Location.Create().
		SetObjectType("customer").
		SetObjectID(customerID).
		SetTitle(params.Title).
		SetAddress1(params.Address1).
		SetAddress2(params.Address2).
		SetCity(params.City).
		SetState(params.State).
		SetZipCode(params.ZipCode).
		SetNotes(params.Notes).
		SetIsPrimary(params.IsPrimary).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create customer location: %w", err)
	}
	return l, nil
}

func (s *LocationService) UpdateCustomerLocation(ctx context.Context, customerID, id int64, params CustomerLocationUpdateParams) (*ent.Location, error) {
	if _, err := s.GetByCustomer(ctx, customerID, id); err != nil {
		return nil, err
	}
	u := s.client.Location.UpdateOneID(id)
	if params.Title != nil {
		u.SetTitle(*params.Title)
	}
	if params.Address1 != nil {
		u.SetAddress1(*params.Address1)
	}
	if params.Address2 != nil {
		u.SetAddress2(*params.Address2)
	}
	if params.City != nil {
		u.SetCity(*params.City)
	}
	if params.State != nil {
		u.SetState(*params.State)
	}
	if params.ZipCode != nil {
		u.SetZipCode(*params.ZipCode)
	}
	if params.Notes != nil {
		u.SetNotes(*params.Notes)
	}
	if params.IsPrimary != nil {
		u.SetIsPrimary(*params.IsPrimary)
	}
	l, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update customer location: %w", err)
	}
	return l, nil
}

func (s *LocationService) DeleteCustomerLocation(ctx context.Context, customerID, id int64) error {
	if _, err := s.GetByCustomer(ctx, customerID, id); err != nil {
		return err
	}
	if err := s.client.Location.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete customer location: %w", err)
	}
	return nil
}

func (s *LocationService) Geocode(ctx context.Context, l *ent.Location, geocoderURL string) (*ent.Location, error) {
	if l == nil || geocoderURL == "" || strings.TrimSpace(LocationAddress(l)) == "" {
		return l, nil
	}
	if l.Latitude != nil && l.Longitude != nil {
		return l, nil
	}

	reqURL, err := geocodeSearchURL(geocoderURL, LocationAddress(l))
	if err != nil {
		return l, fmt.Errorf("parse geocoder URL: %w", err)
	}

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

func geocodeSearchURL(geocoderURL, address string) (*url.URL, error) {
	base := strings.TrimRight(strings.TrimSpace(geocoderURL), "/")
	if !strings.HasSuffix(base, "/search") {
		base += "/search"
	}
	reqURL, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	q := reqURL.Query()
	q.Set("q", address)
	q.Set("format", "jsonv2")
	q.Set("limit", "1")
	reqURL.RawQuery = q.Encode()
	return reqURL, nil
}

func LocationAddress(l *ent.Location) string {
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
