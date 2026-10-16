package authorization

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrTimesheetFilter distinguishes invalid selectors from a failed report read.
var ErrTimesheetFilter = errors.New("invalid Timesheet filter")

// ElapsedMicroseconds uses the database's exact precision. Aggregate subject
// time can exceed time.Duration's nanosecond range without any individual entry
// being unusually long. Formatting never converts the whole total to nanoseconds.
type ElapsedMicroseconds int64

func (d ElapsedMicroseconds) String() string {
	hours := int64(d) / 3600000000
	remainder := time.Duration(int64(d)%3600000000) * time.Microsecond
	if hours == 0 {
		return remainder.String()
	}
	return fmt.Sprintf("%dh%s", hours, strings.TrimPrefix((time.Hour+remainder).String(), "1h"))
}

func elapsedMicroseconds(start, end time.Time) (ElapsedMicroseconds, error) {
	if end.Before(start) {
		return 0, errors.New("recorded interval requires review")
	}
	seconds := end.Unix() - start.Unix()
	fraction := int64(end.Nanosecond()-start.Nanosecond()) / 1000
	if fraction < 0 {
		seconds--
		fraction += 1000000
	}
	if seconds > (math.MaxInt64-fraction)/1000000 {
		return 0, errors.New("report duration exceeds supported range")
	}
	return ElapsedMicroseconds(seconds*1000000 + fraction), nil
}

// TimesheetFilter selects a reporting interval, never an authorization scope.
// Dates are inclusive company-local dates; intervals are clipped for totals.
type TimesheetFilter struct {
	DateFrom, DateTo, Search         string
	Source, Lifecycle, GroupBy, Sort string
	UserID                           int64
	JobID                            int64
	Standalone                       bool
	IncludeVoid                      bool
}

type TimesheetRow struct {
	ID          int64
	Access      TimeEntryView
	Start       time.Time
	End         *time.Time
	Notes       string
	Manual      bool
	Duration    ElapsedMicroseconds
	Provisional bool
	Days        []TimesheetDay
	GroupID     int64
	GroupLabel  string
	History     []TimeRevision
	GPS         []ClockGPSFact
}

type TimesheetChoice struct {
	ID   int64
	Name string
}
type TimesheetGroup struct {
	ID          int64
	Key, Name   string
	Total       ElapsedMicroseconds
	Provisional bool
	Days        []TimesheetDay
}

type TimesheetDay struct {
	Date     string
	Duration ElapsedMicroseconds
}

// Timesheet is an all-matching authorized snapshot. It is not an export artifact.
type Timesheet struct {
	Filter                  TimesheetFilter
	AsOf                    time.Time
	Timezone                string
	Rows                    []TimesheetRow
	Days                    []TimesheetDay
	Total                   ElapsedMicroseconds
	Provisional             bool
	CanCreate               bool
	CanExport, CanExportGPS bool
	Subjects, Jobs          []TimesheetChoice
	Groups                  []TimesheetGroup
}

func (s *Service) Timesheet(ctx context.Context, a Actor, filter TimesheetFilter) (Timesheet, error) {
	var result Timesheet
	p, err := s.Principal(ctx, a)
	if err != nil {
		return result, err
	}
	scope := timeEntryGrant(p, "time.view", true, true)
	if scope == "" || a.Channel == "api" {
		return result, ErrForbidden
	}
	err = s.WithinSnapshotPermission(ctx, a, "time.view", scope, func(tx pgx.Tx, p Principal) error {
		var err error
		result, err = readTimesheet(ctx, tx, p, filter, "", "")
		return err
	})
	if err != nil {
		return Timesheet{}, err
	}
	return result, nil
}

// NormalizeTimesheetFilter is shared by report and durable export acceptance.
func NormalizeTimesheetFilter(filter TimesheetFilter) (TimesheetFilter, error) {
	if filter.GroupBy == "" {
		filter.GroupBy = "subject"
	}
	if filter.Sort == "" {
		filter.Sort = "start_asc"
	}
	if filter.GroupBy != "subject" && filter.GroupBy != "job" || filter.Sort != "start_asc" && filter.Sort != "start_desc" || filter.Source != "" && filter.Source != "manual" && filter.Source != "clocked" || filter.Lifecycle != "" && filter.Lifecycle != "active" && filter.Lifecycle != "closed" && filter.Lifecycle != "void" || filter.UserID < 0 || filter.JobID < 0 {
		return filter, ErrTimesheetFilter
	}
	for _, date := range []string{filter.DateFrom, filter.DateTo} {
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return filter, ErrTimesheetFilter
			}
		}
	}
	if filter.DateFrom != "" && filter.DateTo != "" && filter.DateFrom > filter.DateTo {
		return filter, ErrTimesheetFilter
	}
	return filter, nil
}

func readTimesheet(ctx context.Context, tx pgx.Tx, p Principal, filter TimesheetFilter, exportPermission string, exportScope Scope) (Timesheet, error) {
	var result Timesheet
	filter, err := NormalizeTimesheetFilter(filter)
	if err != nil {
		return result, err
	}
	err = func() error {
		if err := tx.QueryRow(ctx, `SELECT timezone FROM company_settings LIMIT 1`).Scan(&result.Timezone); err != nil {
			return err
		}
		loc, err := time.LoadLocation(result.Timezone)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&result.AsOf); err != nil {
			return err
		}
		result.AsOf = result.AsOf.In(loc)
		result.Filter = filter
		result.CanCreate = timeEntryGrant(p, "time.create", true, true) != ""
		for _, scope := range []Scope{All, Own, Assigned} {
			result.CanExport = result.CanExport || AuthorizeTimeExport(p, "time.export", scope) == nil
			result.CanExportGPS = result.CanExportGPS || AuthorizeTimeExport(p, "time.gps.export", scope) == nil
		}
		subjects, jobs := map[int64]string{}, map[int64]string{}
		rows, err := tx.Query(ctx, `SELECT id,clock_in,clock_out,coalesce(notes,''),is_manual FROM time_entries WHERE company_id=$1 ORDER BY clock_in,id`, p.CompanyID)
		if err != nil {
			return err
		}
		var candidates []TimesheetRow
		for rows.Next() {
			var row TimesheetRow
			if err := rows.Scan(&row.ID, &row.Start, &row.End, &row.Notes, &row.Manual); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		days := map[string]ElapsedMicroseconds{}
		for _, row := range candidates {
			access, err := readTimeEntryAccess(ctx, tx, p, row.ID, "", false)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			row.Access = access
			if exportPermission != "" {
				if err := checkTimeExportAnchor(ctx, tx, p, access.UserID, access.JobID, exportPermission, exportScope); err != nil {
					if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
						continue
					}
					return err
				}
			}
			if access.VoidedAt != nil && !filter.IncludeVoid {
				continue
			}
			subjects[access.UserID] = access.SubjectName
			if access.JobID > 0 {
				jobs[access.JobID] = access.JobTitle
			}
			if filter.UserID != 0 && filter.UserID != access.UserID || filter.Search != "" && !strings.Contains(strings.ToLower(row.Notes), strings.ToLower(filter.Search)) || filter.JobID != 0 && filter.JobID != access.JobID || filter.Standalone && access.JobID != 0 || filter.Source == "manual" && !row.Manual || filter.Source == "clocked" && row.Manual {
				continue
			}
			lifecycle := "closed"
			if access.VoidedAt != nil {
				lifecycle = "void"
			} else if row.End == nil {
				lifecycle = "active"
			}
			if filter.Lifecycle != "" && filter.Lifecycle != lifecycle {
				continue
			}
			if access.VoidedAt != nil {
				matched, err := voidMatchesCompanyDates(ctx, row.Start, row.End, loc, filter)
				if err != nil {
					return err
				}
				if !matched {
					continue
				}
			} else {
				end := result.AsOf
				if row.End != nil {
					end = *row.End
				}
				selected, duration, matched, err := selectedCompanyDays(ctx, row.Start, end, loc, filter)
				if err != nil {
					return err
				}
				if !matched {
					continue
				}
				row.Duration = duration
				row.Days = selected
				if result.Total > ElapsedMicroseconds(math.MaxInt64)-row.Duration {
					return errors.New("report duration exceeds supported range")
				}
				result.Total += row.Duration
				row.Provisional = row.End == nil
				for _, part := range selected {
					days[part.Date] += part.Duration
				}
				result.Provisional = result.Provisional || row.Provisional
			}
			row.History, err = readTimeEntryHistory(ctx, tx, p, row.ID)
			if err != nil {
				return err
			}
			if access.CanGPS && exportPermission != "time.export" {
				row.GPS, err = readClockGPS(ctx, tx, row.ID)
				if err != nil {
					return err
				}
			}
			row.Start = row.Start.In(loc)
			if row.End != nil {
				end := row.End.In(loc)
				row.End = &end
			}
			for i := range row.History {
				r := &row.History[i]
				r.AcceptedAt = r.AcceptedAt.In(loc)
				for _, state := range []*TimeState{r.Before, &r.After} {
					if state == nil {
						continue
					}
					state.ClockIn = state.ClockIn.In(loc)
					if state.ClockOut != nil {
						end := state.ClockOut.In(loc)
						state.ClockOut = &end
					}
				}
			}
			for i := range row.GPS {
				row.GPS[i].CapturedAt = row.GPS[i].CapturedAt.In(loc)
			}
			result.Rows = append(result.Rows, row)
		}
		// Retain a submitted selector even when it has no authorized option. This
		// echoes only caller-supplied identity, never existence or a hidden name.
		if filter.UserID != 0 {
			if _, ok := subjects[filter.UserID]; !ok {
				subjects[filter.UserID] = "Requested subject"
			}
		}
		if filter.JobID != 0 {
			if _, ok := jobs[filter.JobID]; !ok {
				jobs[filter.JobID] = "Requested Job"
			}
		}
		for id, name := range subjects {
			result.Subjects = append(result.Subjects, TimesheetChoice{id, name})
		}
		for id, name := range jobs {
			result.Jobs = append(result.Jobs, TimesheetChoice{id, name})
		}
		for _, options := range [][]TimesheetChoice{result.Subjects, result.Jobs} {
			sort.Slice(options, func(i, j int) bool {
				if options[i].Name != options[j].Name {
					return options[i].Name < options[j].Name
				}
				return options[i].ID < options[j].ID
			})
		}
		groups := map[int64]*TimesheetGroup{}
		groupDays := map[int64]map[string]ElapsedMicroseconds{}
		for i := range result.Rows {
			row := &result.Rows[i]
			id, name := row.Access.UserID, row.Access.SubjectName
			if filter.GroupBy == "job" {
				id, name = row.Access.JobID, row.Access.JobTitle
				if id == 0 {
					name = "Standalone"
				}
			}
			row.GroupID, row.GroupLabel = id, name
			g := groups[id]
			if g == nil {
				g = &TimesheetGroup{ID: id, Key: fmt.Sprintf("%s:%d", filter.GroupBy, id), Name: name}
				groups[id] = g
				groupDays[id] = map[string]ElapsedMicroseconds{}
			}
			g.Total += row.Duration
			g.Provisional = g.Provisional || row.Provisional
			for _, day := range row.Days {
				groupDays[id][day.Date] += day.Duration
			}
		}
		for id, g := range groups {
			g.Days = sortedTimesheetDays(groupDays[id])
			result.Groups = append(result.Groups, *g)
		}
		sort.Slice(result.Groups, func(i, j int) bool { return result.Groups[i].ID < result.Groups[j].ID })
		sort.Slice(result.Rows, func(i, j int) bool {
			a, b := result.Rows[i], result.Rows[j]
			if a.GroupID != b.GroupID {
				return a.GroupID < b.GroupID
			}
			if a.Start.Equal(b.Start) {
				return a.ID < b.ID
			}
			if filter.Sort == "start_desc" {
				return a.Start.After(b.Start)
			}
			return a.Start.Before(b.Start)
		})
		result.Days = sortedTimesheetDays(days)
		return nil
	}()
	if err != nil {
		return Timesheet{}, err
	}
	return result, nil
}

// Walk actual zone transitions, rather than adding 24 hours or asking time.Date
// to guess an ambiguous/nonexistent midnight. A skipped midnight begins at the
// first real instant of the new company date; repeated hours stay in their day.
func nextCompanyDay(start time.Time, loc *time.Location) (time.Time, error) {
	date := start.In(loc).Format("2006-01-02")
	for cursor := start; ; {
		local := cursor.In(loc)
		_, offset := local.Zone()
		next := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, time.FixedZone("", offset))
		_, transition := local.ZoneBounds()
		if !transition.IsZero() && transition.Before(next) {
			next = transition
		}
		if !next.After(cursor) {
			return time.Time{}, errors.New("invalid company day boundary")
		}
		if next.In(loc).Format("2006-01-02") != date {
			return next, nil
		}
		cursor = next
	}
}

// Filter the actual local-date segments, not one assumed contiguous UTC range.
// Some IANA transitions roll midnight back into the preceding date: both dates
// can occur twice inside a single recorded interval. Their elapsed pieces must
// be selected and reconciled by date without shifting the original instants.
func selectedCompanyDays(ctx context.Context, start, end time.Time, loc *time.Location, filter TimesheetFilter) ([]TimesheetDay, ElapsedMicroseconds, bool, error) {
	if _, err := elapsedMicroseconds(start, end); err != nil {
		return nil, 0, false, err
	}
	matched := start.Equal(end) && filter.includesCompanyDate(start.In(loc).Format("2006-01-02"))
	byDate := map[string]ElapsedMicroseconds{}
	var total ElapsedMicroseconds
	for cursor := start; cursor.Before(end); {
		if err := ctx.Err(); err != nil {
			return nil, 0, false, err
		}
		boundary, err := nextCompanyDay(cursor, loc)
		if err != nil {
			return nil, 0, false, err
		}
		if boundary.After(end) {
			boundary = end
		}
		date := cursor.In(loc).Format("2006-01-02")
		if filter.includesCompanyDate(date) {
			part, err := elapsedMicroseconds(cursor, boundary)
			if err != nil {
				return nil, 0, false, err
			}
			byDate[date] += part
			total += part
			matched = true
		}
		cursor = boundary
	}
	return sortedTimesheetDays(byDate), total, matched, nil
}

func (filter TimesheetFilter) includesCompanyDate(date string) bool {
	return (filter.DateFrom == "" || date >= filter.DateFrom) && (filter.DateTo == "" || date <= filter.DateTo)
}

// Void has no counted duration to validate. Missing or invalid retained ends
// select by the known start date; an ordered recorded interval selects its
// intersecting company dates. Neither case invents or rewrites an endpoint.
func voidMatchesCompanyDates(ctx context.Context, start time.Time, end *time.Time, loc *time.Location, filter TimesheetFilter) (bool, error) {
	if filter.DateFrom == "" && filter.DateTo == "" {
		return true, nil
	}
	if end == nil || !end.After(start) {
		return filter.includesCompanyDate(start.In(loc).Format("2006-01-02")), nil
	}
	for cursor := start; cursor.Before(*end); {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if filter.includesCompanyDate(cursor.In(loc).Format("2006-01-02")) {
			return true, nil
		}
		boundary, err := nextCompanyDay(cursor, loc)
		if err != nil {
			return false, err
		}
		if boundary.After(*end) {
			boundary = *end
		}
		cursor = boundary
	}
	return false, nil
}

func sortedTimesheetDays(byDate map[string]ElapsedMicroseconds) []TimesheetDay {
	var days []TimesheetDay
	for date, duration := range byDate {
		days = append(days, TimesheetDay{date, duration})
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	return days
}
