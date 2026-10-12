package services

import (
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
)

const DefaultDateFormat = "Jan 2, 2006"

type DateFormatOption struct {
	Value       string
	Label       string
	DatePreview string
	TimePreview string
	TimeLayout  string
}

func DateFormatOptions() []DateFormatOption {
	example := time.Date(2026, time.June, 29, 14, 0, 0, 0, time.UTC)
	options := []DateFormatOption{
		{Value: "Jan 2, 2006", Label: "Jun 29, 2026", TimeLayout: "3:04 PM"},
		{Value: "January 2, 2006", Label: "June 29, 2026", TimeLayout: "3:04 PM"},
		{Value: "01/02/2006", Label: "06/29/2026", TimeLayout: "3:04 PM"},
		{Value: "02/01/2006", Label: "29/06/2026", TimeLayout: "15:04"},
		{Value: "2006-01-02", Label: "2026-06-29", TimeLayout: "15:04"},
	}
	for i := range options {
		options[i].DatePreview = example.Format(options[i].Value)
		options[i].TimePreview = example.Format(options[i].Value + " " + options[i].TimeLayout)
	}
	return options
}

func NormalizeDateFormat(value string) string {
	value = strings.TrimSpace(value)
	for _, option := range DateFormatOptions() {
		if value == option.Value {
			return value
		}
	}
	return DefaultDateFormat
}

func FormatCompanyDate(t time.Time, loc *time.Location, cs *ent.CompanySettings) string {
	if t.IsZero() {
		return ""
	}
	return t.In(loc).Format(NormalizeDateFormat(companyDateFormat(cs)))
}

// FormatLocalDate formats a calendar date without converting it to an instant
// in the current company timezone.
func FormatLocalDate(t time.Time, cs *ent.CompanySettings) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(NormalizeDateFormat(companyDateFormat(cs)))
}

// IsCompanyDateOverdue compares a stored calendar date with the company's
// current calendar day. It must not timezone-shift the stored date or treat a
// missing due date as an ancient overdue instant.
func IsCompanyDateOverdue(date, now time.Time, loc *time.Location) bool {
	return !date.IsZero() && date.Format("2006-01-02") < now.In(loc).Format("2006-01-02")
}

func FormatCompanyDateTime(t time.Time, loc *time.Location, cs *ent.CompanySettings) string {
	if t.IsZero() {
		return ""
	}
	dateLayout := NormalizeDateFormat(companyDateFormat(cs))
	return t.In(loc).Format(dateLayout + " " + timeLayoutForDateFormat(dateLayout))
}

func FormatCompanyTime(t time.Time, loc *time.Location, cs *ent.CompanySettings) string {
	if t.IsZero() {
		return ""
	}
	dateLayout := NormalizeDateFormat(companyDateFormat(cs))
	return t.In(loc).Format(timeLayoutForDateFormat(dateLayout))
}

func companyDateFormat(cs *ent.CompanySettings) string {
	if cs == nil {
		return DefaultDateFormat
	}
	return cs.DateFormat
}

func timeLayoutForDateFormat(dateLayout string) string {
	for _, option := range DateFormatOptions() {
		if option.Value == dateLayout {
			return option.TimeLayout
		}
	}
	return "3:04 PM"
}
