package authorization

import (
	"testing"
	"time"
)

func TestWorkloadClassificationBoundariesAndRepair(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	instant := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return &v
	}
	asOf := *instant("2026-03-09T02:00:00Z") // still March 8 locally
	f := WorkloadFilter{ScheduleFrom: "2026-03-08", ScheduleTo: "2026-03-08"}
	tests := []struct {
		name                                                      string
		row                                                       JobOperationalRow
		match, scheduled, unscheduled, repair, overdue, completed bool
	}{
		{"end at midnight", JobOperationalRow{Start: instant("2026-03-07T23:00:00-05:00"), End: instant("2026-03-08T00:00:00-05:00")}, false, false, false, false, false, false},
		{"23 hour day end", JobOperationalRow{Start: instant("2026-03-09T00:00:00-04:00"), End: instant("2026-03-09T01:00:00-04:00")}, false, false, false, false, false, false},
		{"cross midnight", JobOperationalRow{Start: instant("2026-03-07T23:00:00-05:00"), End: instant("2026-03-08T01:00:00-05:00")}, true, true, false, false, false, false},
		{"absent overdue", JobOperationalRow{Due: instant("2026-03-07T00:00:00Z")}, true, false, true, false, true, false},
		{"due today", JobOperationalRow{Due: instant("2026-03-08T00:00:00Z")}, true, false, true, false, false, false},
		{"incomplete outside range", JobOperationalRow{Start: instant("2020-01-01T00:00:00Z")}, true, false, false, true, false, false},
		{"backwards", JobOperationalRow{Start: instant("2020-01-02T00:00:00Z"), End: instant("2020-01-01T00:00:00Z")}, true, false, false, true, false, false},
		{"orphan arrival", JobOperationalRow{ArrivalStart: instant("2020-01-01T00:00:00Z")}, true, false, false, true, false, false},
		{"incomplete arrival outside range", JobOperationalRow{Start: instant("2020-01-01T01:00:00Z"), End: instant("2020-01-01T02:00:00Z"), ArrivalEnd: instant("2020-01-01T01:00:00Z")}, true, false, false, true, false, false},
		{"arrival does not contain start", JobOperationalRow{Start: instant("2020-01-01T01:00:00Z"), End: instant("2020-01-01T02:00:00Z"), ArrivalStart: instant("2020-01-01T01:30:00Z"), ArrivalEnd: instant("2020-01-01T02:00:00Z")}, true, false, false, true, false, false},
		{"reversed arrival", JobOperationalRow{Start: instant("2020-01-01T01:00:00Z"), End: instant("2020-01-01T02:00:00Z"), ArrivalStart: instant("2020-01-01T02:00:00Z"), ArrivalEnd: instant("2020-01-01T00:00:00Z")}, true, false, false, true, false, false},
		{"completed overlaps unscheduled", JobOperationalRow{Category: "job:completed", Due: instant("2026-03-07T00:00:00Z")}, true, false, true, false, false, true},
		{"canceled not overdue", JobOperationalRow{Category: "job:canceled", Due: instant("2026-03-07T00:00:00Z")}, true, false, true, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, match := workloadClassify(tt.row, f, asOf, loc)
			if match != tt.match {
				t.Fatalf("match=%v row=%+v", match, r)
			}
			if match && (r.Scheduled != tt.scheduled || r.Unscheduled != tt.unscheduled || r.Repair != tt.repair || r.Overdue != tt.overdue || r.Completed != tt.completed) {
				t.Fatalf("classification %+v", r)
			}
		})
	}
	for _, measure := range []string{"scheduled", "unscheduled", "repair", "overdue", "completed"} {
		mf := f
		mf.Measure = measure
		r, match := workloadClassify(JobOperationalRow{Category: "job:completed"}, mf, asOf, loc)
		if match != (measure == "unscheduled" || measure == "completed") {
			t.Fatalf("measure %s: %+v %v", measure, r, match)
		}
	}
	fall := WorkloadFilter{ScheduleFrom: "2026-11-01", ScheduleTo: "2026-11-01"}
	for _, interval := range [][2]string{{"2026-11-01T01:30:00-04:00", "2026-11-01T01:45:00-04:00"}, {"2026-11-01T01:30:00-05:00", "2026-11-01T01:45:00-05:00"}, {"2026-11-01T23:30:00-05:00", "2026-11-02T00:00:00-05:00"}} {
		if r, match := workloadClassify(JobOperationalRow{Start: instant(interval[0]), End: instant(interval[1])}, fall, asOf, loc); !match || !r.Scheduled {
			t.Fatalf("25-hour day lost interval %v: %+v", interval, r)
		}
	}
	if _, match := workloadClassify(JobOperationalRow{Start: instant("2026-11-02T00:00:00-05:00"), End: instant("2026-11-02T01:00:00-05:00")}, fall, asOf, loc); match {
		t.Fatal("25-hour day included next midnight")
	}
}

func TestWorkloadNormalizeRejectsInvalidAndDoesNotMutate(t *testing.T) {
	input := WorkloadFilter{AssigneeIDs: []int64{3, 1, 3}}
	f, err := NormalizeWorkloadFilter(input)
	if err != nil || len(f.AssigneeIDs) != 2 || f.AssigneeIDs[0] != 1 || input.AssigneeIDs[0] != 3 {
		t.Fatalf("normalize %+v %v", f, err)
	}
	for _, bad := range []WorkloadFilter{{Measure: "unknown"}, {ScheduleFrom: "2026-02-30"}, {DueFrom: "2026-03-02", DueTo: "2026-03-01"}, {AssigneeIDs: []int64{-1}}, {Unassigned: true, AssigneeIDs: []int64{1}}, {Category: "Completed"}, {Sort: "title"}} {
		if _, err := NormalizeWorkloadFilter(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	conjunction, err := NormalizeWorkloadFilter(WorkloadFilter{Measure: "scheduled,overdue,scheduled"})
	if err != nil || conjunction.Measure != "overdue,scheduled" {
		t.Fatalf("noncanonical conjunction %+v %v", conjunction, err)
	}
}

func TestWorkloadMeasureDrilldownIntersections(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	yesterday := now.AddDate(0, 0, -1)
	end := now.Add(time.Hour)
	rows := []JobOperationalRow{
		{ID: 1, Start: &now, End: &end, Due: &yesterday, AssigneeIDs: []int64{7}},
		{ID: 2, Start: &now, End: &end, AssigneeIDs: []int64{7}},
		{ID: 3, Due: &yesterday, AssigneeIDs: []int64{7}},
		{ID: 4, Start: &now, End: &end, Due: &yesterday, AssigneeIDs: []int64{8}},
	}
	for _, tt := range []struct {
		measure string
		want    int
	}{{"overdue", 2}, {"overdue,scheduled", 1}, {"overdue,unscheduled", 1}, {"completed,overdue", 0}, {"scheduled,unscheduled", 0}, {"overdue,repair", 0}} {
		f := WorkloadFilter{Measure: tt.measure, AssigneeIDs: []int64{7}}
		count := 0
		for _, j := range rows {
			if _, match := workloadClassify(j, f, now, time.UTC); match {
				count++
			}
		}
		if count != tt.want {
			t.Fatalf("%s: got %d want %d", tt.measure, count, tt.want)
		}
	}
}
