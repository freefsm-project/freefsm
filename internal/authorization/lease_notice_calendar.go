package authorization

import "time"

// leaseOrdinaryNoticeEnd selects the first inclusive fixed/renewal period end
// that provides thirty civil days' notice. Every renewal is calculated from the
// original anchor, including when the intervening month has fewer days.
func leaseOrdinaryNoticeEnd(fixedEnd, noticeOn string) (string, error) {
	end, err := time.Parse(time.DateOnly, fixedEnd)
	if err != nil {
		return "", err
	}
	notice, err := time.Parse(time.DateOnly, noticeOn)
	if err != nil {
		return "", err
	}
	earliest := notice.AddDate(0, 0, 30)
	if !end.Before(earliest) {
		return end.Format(time.DateOnly), nil
	}
	anchor := end.AddDate(0, 0, 1)
	months := (earliest.Year()-anchor.Year())*12 + int(earliest.Month()-anchor.Month())
	for {
		candidate := leaseRenewalBoundary(anchor, months).AddDate(0, 0, -1)
		if !candidate.Before(earliest) {
			return candidate.Format(time.DateOnly), nil
		}
		months++
	}
}
