package settlement

import (
	"errors"
	"sort"
	"time"
)

type State string

const (
	Unpaid        State = "unpaid"
	PartiallyPaid State = "partially_paid"
	Paid          State = "paid"
)

var ErrInvalidAmount = errors.New("amount must be positive")

func StateFor(totalCents, settledCents int64) (State, error) {
	if totalCents < 0 {
		return "", errors.New("invoice total must not be negative")
	}
	if totalCents == 0 || settledCents >= totalCents {
		return Paid, nil
	}
	if settledCents <= 0 {
		return Unpaid, nil
	}
	return PartiallyPaid, nil
}

func SplitPayment(paymentCents, dueCents int64) (applied, credit int64, err error) {
	if paymentCents <= 0 {
		return 0, 0, ErrInvalidAmount
	}
	if dueCents < 0 {
		dueCents = 0
	}
	applied = min(paymentCents, dueCents)
	return applied, paymentCents - applied, nil
}

type AvailableSource struct {
	ID    string
	Date  time.Time
	Cents int64
}

type Allocation struct {
	SourceID string
	Cents    int64
}

func AllocateFIFO(requested int64, sources []AvailableSource) ([]Allocation, error) {
	if requested <= 0 {
		return nil, ErrInvalidAmount
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Date.Equal(sources[j].Date) {
			return sources[i].ID < sources[j].ID
		}
		return sources[i].Date.Before(sources[j].Date)
	})
	remaining := requested
	allocations := make([]Allocation, 0, len(sources))
	for _, source := range sources {
		if source.Cents <= 0 || remaining == 0 {
			continue
		}
		amount := min(source.Cents, remaining)
		allocations = append(allocations, Allocation{SourceID: source.ID, Cents: amount})
		remaining -= amount
	}
	if remaining != 0 {
		return nil, errors.New("insufficient available customer credit")
	}
	return allocations, nil
}

func ValidateEffectiveDate(date, today, notBefore time.Time) error {
	date, today = dateOnly(date), dateOnly(today)
	if date.After(today) {
		return errors.New("effective date must not be in the future")
	}
	if !notBefore.IsZero() && date.Before(dateOnly(notBefore)) {
		return errors.New("effective date precedes its credit source")
	}
	return nil
}

func dateOnly(value time.Time) time.Time {
	y, m, d := value.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, value.Location())
}
