package templates

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
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

func TestActivityRenderersShowAbsoluteTimestamp(t *testing.T) {
	const absoluteTimestamp = "Jul 13, 2026 2:45 PM"
	entry := ActivityEntry{
		ActorName:  "Alex",
		Action:     "updated",
		EntityName: "Customer record",
		CreatedAt:  absoluteTimestamp,
	}

	tests := []struct {
		name      string
		component templ.Component
	}{
		{
			name: "widget",
			component: ActivityWidget(ActivityWidgetData{
				DOMID:   "activity-customer-42",
				Entries: []ActivityEntry{entry},
			}),
		},
		{
			name: "recent list",
			component: ActivityRecentList(ActivityPageData{
				Entries: []ActivityEntry{entry},
			}),
		},
		{
			name: "index",
			component: ActivityIndex(ActivityPageData{
				Entries: []ActivityEntry{entry},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := tt.component.Render(context.Background(), &output); err != nil {
				t.Fatal(err)
			}

			html := output.String()
			if !strings.Contains(html, absoluteTimestamp) {
				t.Fatalf("absolute timestamp was not visible: %s", html)
			}
			if strings.Contains(html, `title="`+absoluteTimestamp+`"`) {
				t.Fatalf("absolute timestamp was retained as a title tooltip: %s", html)
			}
			lowerHTML := strings.ToLower(html)
			for _, relativeWording := range []string{" ago", "just now", "yesterday"} {
				if strings.Contains(lowerHTML, relativeWording) {
					t.Fatalf("rendered relative timestamp %q: %s", relativeWording, html)
				}
			}
		})
	}
}

func TestActivityNavigationHasNoTotalsOrPageNumbers(t *testing.T) {
	var output bytes.Buffer
	component := ActivityIndex(ActivityPageData{
		Entries:  []ActivityEntry{{ActorName: "Alex", Action: "updated", EntityName: "Customer"}},
		NewerURL: "/activity?cursor=newer&type=customer",
		OlderURL: "/activity?cursor=older&type=customer",
	})
	if err := component.Render(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, unwanted := range []string{"Page 1", "entries)", "activity-pagination", "?page="} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("activity index contains legacy pagination %q: %s", unwanted, html)
		}
	}
	if !strings.Contains(html, "Newer") || !strings.Contains(html, "Older") || !strings.Contains(html, "type=customer") {
		t.Fatalf("cursor navigation missing preserved filters: %s", html)
	}
}
