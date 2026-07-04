package handlers

import (
	"testing"
	"time"
)

func TestScheduleActivityAction(t *testing.T) {
	start := time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		oldStart    *time.Time
		oldAssignee string
		newAssignee string
		want        string
	}{
		{name: "new schedule", want: "scheduled"},
		{name: "time changed", oldStart: &start, want: "rescheduled"},
		{name: "assignee changed", oldStart: &start, oldAssignee: "Chris", newAssignee: "Morgan", want: "dispatched"},
		{name: "new assignee", oldStart: &start, newAssignee: "Morgan", want: "dispatched"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scheduleActivityAction(tt.oldStart, tt.oldAssignee, tt.newAssignee); got != tt.want {
				t.Fatalf("scheduleActivityAction() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScheduleChanged(t *testing.T) {
	oldStart := time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC)
	newStart := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)

	if scheduleChanged(&oldStart, nil, &oldStart, nil, "Chris", "Chris") {
		t.Fatal("scheduleChanged reported unchanged schedule as changed")
	}
	if !scheduleChanged(&oldStart, nil, &newStart, nil, "Chris", "Chris") {
		t.Fatal("scheduleChanged did not detect time change")
	}
	if !scheduleChanged(&oldStart, nil, &oldStart, nil, "Chris", "Morgan") {
		t.Fatal("scheduleChanged did not detect assignee change")
	}
}
