package templates

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func TestBackupActivitySharedRendering(t *testing.T) {
	entry := ActivityEntry{
		ActorName: "Original administrator", Action: "restore_failed", TargetType: "instance",
		EntityName: "Instance backup and restore", EntityURL: "/settings/backup",
		Metadata: ActivityMetadata{SourceName: "Source company", FailureCategory: "storage", FailurePhase: "install", FailureMessage: `<script>alert("raw error")</script>`, DiagnosticID: "diagnostic-123"},
	}
	for name, component := range map[string]templ.Component{
		"recent": ActivityRecentList(ActivityPageData{Entries: []ActivityEntry{entry}}),
		"widget": ActivityWidget(ActivityWidgetData{DOMID: "activity", Entries: []ActivityEntry{entry}}),
		"index":  ActivityIndex(ActivityPageData{Entries: []ActivityEntry{entry}}),
	} {
		t.Run(name, func(t *testing.T) {
			var body bytes.Buffer
			if err := component.Render(context.Background(), &body); err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"Original administrator", "could not complete restoring", "Source company", "storage", "install", "diagnostic-123", "&lt;script&gt;"} {
				if !strings.Contains(body.String(), text) {
					t.Fatalf("missing %q in rendered activity", text)
				}
			}
			if strings.Contains(body.String(), entry.Metadata.FailureMessage) {
				t.Fatal("activity metadata rendered as HTML")
			}
		})
	}
	if verb := activityVerb("backup_download_started"); !strings.Contains(verb, "started downloading") {
		t.Fatalf("download activity overstates completion: %q", verb)
	}
}
