package authorization

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type ClockGPSFact struct {
	Event               string
	Latitude, Longitude float64
	CapturedAt          time.Time
	Accuracy            *float64
}

func (s *Service) ClockGPS(ctx context.Context, a Actor, id int64) ([]ClockGPSFact, error) {
	facts := []ClockGPSFact{}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := readTimeEntryAccess(ctx, tx, p, id, "time.gps.view", false); err != nil {
			return err
		}
		var err error
		facts, err = readClockGPS(ctx, tx, id)
		return err
	})
	return facts, err
}

// The caller must establish compatible GPS View authority in the same snapshot.
func readClockGPS(ctx context.Context, tx queryer, id int64) ([]ClockGPSFact, error) {
	facts := []ClockGPSFact{}
	rows, err := tx.Query(ctx, `SELECT event,latitude,longitude,captured_at,accuracy FROM time_clock_gps WHERE entry_id=$1 ORDER BY event`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f ClockGPSFact
		if err := rows.Scan(&f.Event, &f.Latitude, &f.Longitude, &f.CapturedAt, &f.Accuracy); err != nil {
			return nil, err
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}
