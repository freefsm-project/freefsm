package services

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

// DecodeJobPricing accepts only the captured pricing contract, never catalog
// Cost or arbitrary planning fields hidden inside the line payload.
func DecodeJobPricing(raw string) ([]LineItem, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var lines []LineItem
	if err := decoder.Decode(&lines); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedLineItems, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, ErrMalformedLineItems
	}
	return lines, nil
}

// JobPricingLines projects identifiers for legacy captures without rewriting
// their recorded JSON or inventing a source Item, selection date or author.
// The identifier becomes stored only on the next explicit pricing save.
func JobPricingLines(jobID int64, raw string) ([]LineItem, error) {
	lines, err := DecodeLineItems(raw)
	if err != nil {
		return nil, err
	}
	for n := range lines {
		if lines[n].SnapshotID == "" {
			lines[n].SnapshotID = authorization.LegacyJobPricingLineID(jobID, n)
		}
	}
	return lines, nil
}

func encodeJobPricing(jobID int64, previous string, input []LineItem) (string, error) {
	old := map[string]int64{}
	if jobID > 0 {
		lines, err := JobPricingLines(jobID, previous)
		if err != nil {
			return "", err
		}
		for _, line := range lines {
			old[line.SnapshotID] = line.ItemID
		}
	}
	lines := append([]LineItem(nil), input...)
	seen := map[string]bool{}
	for n := range lines {
		line := &lines[n]
		if line.ItemID < 0 {
			return "", fmt.Errorf("%w: invalid Item provenance", ErrInvalidLineItem)
		}
		if line.TaxRate != "" {
			if _, err := parsePercentage(line.TaxRate); err != nil {
				return "", err
			}
		}
		if jobID == 0 || line.SnapshotID == "" {
			line.SnapshotID = uuid.NewString()
		} else if itemID, ok := old[line.SnapshotID]; !ok || itemID != line.ItemID {
			return "", fmt.Errorf("%w: retained snapshot provenance cannot change", authorization.ErrConflict)
		}
		if seen[line.SnapshotID] {
			return "", fmt.Errorf("%w: duplicate snapshot identity", authorization.ErrConflict)
		}
		seen[line.SnapshotID] = true
	}
	return EncodeLineItems(lines)
}
