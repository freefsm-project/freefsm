package authorization

import (
	"sort"
	"time"
)

// LeaseCoverageEvent is an effective membership action. Revisions retain its
// identity; withdrawing an action removes it from the projection, not history.
type LeaseCoverageEvent struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	EffectiveOn string `json:"effective_on"`
	OutgoingID  int64  `json:"outgoing_id"`
	IncomingID  int64  `json:"incoming_id"`
}

// projectLeaseCoverage replays the complete known membership timeline. Each
// action must be valid independently; replacing the last Asset is one action.
// The resulting intervals are inclusive, while a removal ends the previous day.
func projectLeaseCoverage(initial []LeaseAssetDependency, start string, end *string, events []LeaseCoverageEvent) ([]LeaseAssetDependency, error) {
	invalid := func(message string) ([]LeaseAssetDependency, error) {
		return nil, &LeaseValidationError{Message: message}
	}
	if _, err := time.Parse(time.DateOnly, start); err != nil {
		return invalid("A valid commencement date is required")
	}
	if end != nil {
		if _, err := time.Parse(time.DateOnly, *end); err != nil || *end < start {
			return invalid("A valid inclusive termination date is required")
		}
	}
	rows := make([]LeaseAssetDependency, 0, len(initial)+len(events))
	active := map[int64]int{}
	for _, row := range initial {
		if row.AssetID < 1 || row.StartsOn != start {
			return invalid("Initial coverage must begin at commencement")
		}
		if _, exists := active[row.AssetID]; exists {
			return invalid("Duplicate initial Asset")
		}
		active[row.AssetID] = len(rows)
		rows = append(rows, LeaseAssetDependency{AssetID: row.AssetID, StartsOn: start, EndsOn: end})
	}
	if len(active) == 0 {
		return invalid("At least one Asset must remain covered throughout the agreement")
	}
	ordered := append([]LeaseCoverageEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].EffectiveOn == ordered[j].EffectiveOn {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].EffectiveOn < ordered[j].EffectiveOn
	})
	for _, event := range ordered {
		day, err := time.Parse(time.DateOnly, event.EffectiveOn)
		if err != nil || event.EffectiveOn < start || (end != nil && event.EffectiveOn > *end) {
			return invalid("Coverage changes must fall within the agreement's in-force dates")
		}
		if event.Kind != "add" && event.Kind != "remove" && event.Kind != "replace" {
			return invalid("Select Add, Remove or Replace")
		}
		if event.Kind == "add" && event.OutgoingID != 0 || event.Kind == "remove" && event.IncomingID != 0 {
			return invalid("Unexpected Asset selection")
		}
		if event.Kind != "remove" {
			if event.IncomingID < 1 {
				return invalid("Select an incoming Asset")
			}
			if _, exists := active[event.IncomingID]; exists {
				return invalid("Incoming Asset is already covered on the effective date")
			}
		}
		if event.Kind != "add" {
			index, exists := active[event.OutgoingID]
			if !exists {
				return invalid("Outgoing Asset is not covered on the effective date; review later changes as well")
			}
			delete(active, event.OutgoingID)
			if rows[index].StartsOn == event.EffectiveOn {
				// The event history retains both facts, while a zero-day
				// interval has no place in the inclusive coverage projection.
				rows = append(rows[:index], rows[index+1:]...)
				for asset, position := range active {
					if position > index {
						active[asset] = position - 1
					}
				}
			} else {
				last := day.AddDate(0, 0, -1).Format(time.DateOnly)
				rows[index].EndsOn = &last
			}
		}
		if event.Kind != "remove" {
			active[event.IncomingID] = len(rows)
			rows = append(rows, LeaseAssetDependency{AssetID: event.IncomingID, StartsOn: event.EffectiveOn, EndsOn: end})
		}
		if len(active) == 0 {
			return invalid("At least one Asset must remain covered throughout the agreement; use atomic Replace")
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].AssetID == rows[j].AssetID {
			return rows[i].StartsOn < rows[j].StartsOn
		}
		return rows[i].AssetID < rows[j].AssetID
	})
	return rows, nil
}
