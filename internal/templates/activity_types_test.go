package templates

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestScheduleActivityVerbAndClass(t *testing.T) {
	tests := []struct {
		action string
		verb   string
		class  string
	}{
		{action: "scheduled", verb: "scheduled", class: "activity-schedule"},
		{action: "rescheduled", verb: "rescheduled", class: "activity-schedule"},
		{action: "dispatched", verb: "dispatched", class: "activity-schedule"},
	}

	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			if got := activityVerb(tt.action); got != tt.verb {
				t.Fatalf("activityVerb(%q) = %q, want %q", tt.action, got, tt.verb)
			}
			if got := activityActionClass(tt.action); got != tt.class {
				t.Fatalf("activityActionClass(%q) = %q, want %q", tt.action, got, tt.class)
			}
		})
	}
}

func TestScheduleActivityDetail(t *testing.T) {
	meta := ActivityMetadata{
		OldStartDisplay: "Jul 4, 2026 9:00 AM",
		OldEndDisplay:   "Jul 4, 2026 10:00 AM",
		NewStartDisplay: "Jul 5, 2026 11:00 AM",
		NewEndDisplay:   "Jul 5, 2026 12:00 PM",
		OldAssignee:     "Chris",
		NewAssignee:     "Morgan",
		Source:          "dispatch",
	}
	want := "Jul 4, 2026 9:00 AM-Jul 4, 2026 10:00 AM -> Jul 5, 2026 11:00 AM-Jul 5, 2026 12:00 PM; Chris -> Morgan; source: dispatch"
	if got := ScheduleActivityDetail(meta); got != want {
		t.Fatalf("ScheduleActivityDetail() = %q, want %q", got, want)
	}
}

func TestActivityWidgetUsesPreparedIdentityAndPlainTargetName(t *testing.T) {
	var output bytes.Buffer
	component := ActivityWidget(ActivityWidgetData{
		DOMID: "activity-customer-42",
		Entries: []ActivityEntry{{
			ActorName:  "Alex",
			Action:     "updated",
			EntityName: "Historical Customer",
		}},
	})
	if err := component.Render(context.Background(), &output); err != nil {
		t.Fatal(err)
	}

	html := output.String()
	if !strings.Contains(html, `id="activity-customer-42"`) {
		t.Fatalf("widget did not render prepared DOM ID: %s", html)
	}
	if !strings.Contains(html, "Historical Customer") || strings.Contains(html, `href=""`) {
		t.Fatalf("widget did not render an unlinked target name: %s", html)
	}
}
