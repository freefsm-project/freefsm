package services

import (
	"testing"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
)

func TestNormalizeDateFormatFallsBackToDefault(t *testing.T) {
	t.Parallel()

	if got := NormalizeDateFormat("not-a-layout"); got != DefaultDateFormat {
		t.Fatalf("NormalizeDateFormat() = %q, want %q", got, DefaultDateFormat)
	}
}

func TestFormatCompanyDateTimeUsesSelectedTimeStyle(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, time.June, 29, 14, 0, 0, 0, time.UTC)
	cs := &ent.CompanySettings{DateFormat: "02/01/2006"}
	if got, want := FormatCompanyDateTime(when, time.UTC, cs), "29/06/2026 14:00"; got != want {
		t.Fatalf("FormatCompanyDateTime() = %q, want %q", got, want)
	}

	cs.DateFormat = "Jan 2, 2006"
	if got, want := FormatCompanyDateTime(when, time.UTC, cs), "Jun 29, 2026 2:00 PM"; got != want {
		t.Fatalf("FormatCompanyDateTime() = %q, want %q", got, want)
	}
}
