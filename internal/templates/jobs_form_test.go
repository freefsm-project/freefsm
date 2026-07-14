package templates

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestJobFormUsesJobIDAsCanonicalFormIdentity(t *testing.T) {
	tests := []struct {
		name       string
		jobID      int64
		wantTitle  string
		wantAction string
		wantMode   string
	}{
		{name: "create", jobID: 0, wantTitle: "New Job", wantAction: "/jobs", wantMode: "create"},
		{name: "edit", jobID: 42, wantTitle: "Edit Job", wantAction: "/jobs/42", wantMode: "edit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rendered bytes.Buffer
			err := JobForm(JobFormPageData{
				Job:          &JobDetail{ID: tt.jobID},
				Errors:       map[string]string{},
				BillingTypes: []string{"flat_rate"},
			}).Render(context.Background(), &rendered)
			if err != nil {
				t.Fatalf("render JobForm: %v", err)
			}

			html := rendered.String()
			for _, want := range []string{
				"<title>" + tt.wantTitle,
				fmt.Sprintf(`action="%s"`, tt.wantAction),
				fmt.Sprintf(`name="form_mode" value="%s"`, tt.wantMode),
				fmt.Sprintf(`name="job_id" value="%d"`, tt.jobID),
				`hx-history="false"`,
			} {
				if !strings.Contains(html, want) {
					t.Errorf("rendered form missing %q", want)
				}
			}
		})
	}
}

func TestHXBoostedJobFormKeepsCanonicalAction(t *testing.T) {
	var rendered bytes.Buffer
	err := JobForm(JobFormPageData{
		Job:          &JobDetail{ID: 73},
		Errors:       map[string]string{},
		BillingTypes: []string{"flat_rate"},
	}).Render(context.Background(), &rendered)
	if err != nil {
		t.Fatalf("render JobForm: %v", err)
	}

	html := rendered.String()
	if !strings.Contains(html, `action="/jobs/73"`) {
		t.Fatalf("boosted-compatible edit form action is not canonical: %s", html)
	}
}
