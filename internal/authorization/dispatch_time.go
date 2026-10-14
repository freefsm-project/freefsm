package authorization

import (
	"fmt"
	"strings"
	"time"
)

// ParseDispatchInstant resolves a wall time against the company's IANA zone.
// Go's ParseInLocation alone silently picks a side of a fold and normalizes a
// gap. Enumerate the surrounding offsets and round-trip each candidate instead.
func ParseDispatchInstant(local, offset, zone string) (*time.Time, error) {
	if local == "" {
		if offset != "" {
			return nil, fmt.Errorf("%w: offset requires a local time", ErrDispatchInput)
		}
		return nil, nil
	}
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "" {
		return nil, fmt.Errorf("%w: company timezone needs review", ErrDispatchInput)
	}
	layout := "2006-01-02T15:04"
	if len(local) > 16 {
		layout = "2006-01-02T15:04:05.999999999"
	}
	wall, err := time.Parse(layout, local)
	if err != nil {
		return nil, fmt.Errorf("%w: enter a local date and time", ErrDispatchInput)
	}
	if wall.Nanosecond()%1000 != 0 {
		return nil, fmt.Errorf("%w: use at most six fractional-second digits", ErrDispatchInput)
	}
	offsets := map[int]bool{}
	for h := -48; h <= 48; h++ {
		_, seconds := wall.Add(time.Duration(h) * time.Hour).In(loc).Zone()
		offsets[seconds] = true
	}
	var candidates []time.Time
	for seconds := range offsets {
		candidate := wall.Add(-time.Duration(seconds) * time.Second).In(loc)
		if candidate.Format(layout) == wall.Format(layout) {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: nonexistent local time in %s", ErrDispatchInput, zone)
	}
	if offset != "" {
		for _, candidate := range candidates {
			if candidate.Format("-07:00") == offset {
				return &candidate, nil
			}
		}
		return nil, fmt.Errorf("%w: offset does not match this company local time", ErrDispatchInput)
	}
	if len(candidates) > 1 {
		choices := []string{}
		for _, c := range candidates {
			choices = append(choices, c.Format("-07:00"))
		}
		return nil, fmt.Errorf("%w: ambiguous local time; explicitly choose offset %s", ErrDispatchInput, strings.Join(choices, " or "))
	}
	return &candidates[0], nil
}
