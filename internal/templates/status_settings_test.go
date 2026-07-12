package templates

import (
	"context"
	"strings"
	"testing"
)

func renderStatusSettings(t *testing.T, data StatusSettingsPageData) string {
	t.Helper()
	var out strings.Builder
	if err := StatusSettingsPage(data).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestStatusSettingsInvoiceRendersCompactFixedRestrictedSlots(t *testing.T) {
	data := StatusSettingsPageData{ObjectType: "invoice", Categories: []StatusCategoryColumn{
		{Key: "invoice:draft", Label: "Draft", Statuses: []StatusSettingsRow{{ID: 1, Name: "Invoice Draft", Color: "#F59E0B", Default: true, Usage: 1}}},
		{Key: "invoice:invoiced", Label: "Invoiced", Statuses: []StatusSettingsRow{{ID: 2, Name: "Invoiced", Color: "#0284C7", Usage: 4}}},
		{Key: "invoice:sent", Label: "Sent", Statuses: []StatusSettingsRow{{ID: 3, Name: "Sent", Color: "#0EA5E9"}}},
		{Key: "invoice:partially_paid", Label: "Partially Paid", Automatic: true, Statuses: []StatusSettingsRow{{ID: 4, Name: "Partially Paid", Color: "#84CC16", Usage: 2}}},
		{Key: "invoice:paid", Label: "Paid", Automatic: true, Statuses: []StatusSettingsRow{{ID: 5, Name: "Paid", Color: "#22C55E", Usage: 8}}},
		{Key: "invoice:void", Label: "Void", Statuses: []StatusSettingsRow{{ID: 6, Name: "Void", Color: "#EF4444"}}},
	}}
	html := renderStatusSettings(t, data)
	for _, want := range []string{"<html lang=\"en\" class=\"no-js\"", "classList.remove('no-js')", "status-category-board", "<button type=\"button\" class=\"status-chip\"", "--status-background:rgba(245, 158, 11, 0.30)", "Automatic", "1 record", "Default", "aria-label=\"Edit Invoice Draft status\"", "Save appearance", "autofocus"} {
		if !strings.Contains(html, want) {
			t.Errorf("render missing %q", want)
		}
	}
	for _, forbidden := range []string{"/move", "/delete", "requested_order", "status-create-dialog", "draggable=\"true\"", "status-drag-handle", "status-color-swatch", "⠿", "Make category default", "<noscript", "dialog.status-dialog:not([open])", "<article class=\"status-chip\"", "tabindex=\"0\""} {
		if strings.Contains(html, forbidden) {
			t.Errorf("invoice render exposed %q", forbidden)
		}
	}
}

func TestStatusSettingsCustomWorkflowUsesDialogsForAdvancedControls(t *testing.T) {
	data := StatusSettingsPageData{ObjectType: "project", Categories: []StatusCategoryColumn{
		{Key: "project:new", Label: "Opportunity", Statuses: []StatusSettingsRow{{ID: 7, Name: "Lead", Color: "#112233", Default: true, Usage: 2}, {ID: 8, Name: "Qualified", Color: "not-css", Usage: 0}}},
		{Key: "project:pending", Label: "Pending"},
	}}
	html := renderStatusSettings(t, data)
	for _, want := range []string{"Create Custom Status", "id=\"status-create-dialog\"", "status-create-form", "<button type=\"button\" class=\"status-chip\"", "draggable=\"true\"", "--status-background:rgba(17, 34, 51, 0.30)", "data-open-dialog=\"status-edit-7\"", "aria-label=\"Edit Lead status\"", "id=\"status-edit-7\"", "Status facts", "Destination category", "class=\"status-source-replacement\" hidden", "class=\"status-confirm\" hidden", "data-original-category=\"project:new\"", "data-original-order=", "data-default=\"true\"", "data-usage=\"2\"", "name=\"requested_order\"", "aria-label=\"Move Lead up\"", "Replacement status", "/settings/statuses/project/7/delete", "--status-color:#6B7280", "--status-background:rgba(107, 114, 128, 0.30)", "autofocus", "Status forms are expanded below", "data-status-settings", "statusSettingsInitialized", "root.addEventListener('click'", "root.addEventListener('change'", "target === dragged", "suppressUntil = Date.now() + 200", "Date.now() < suppressUntil"} {
		if !strings.Contains(html, want) {
			t.Errorf("render missing %q", want)
		}
	}
	if strings.Index(html, "status-chip") > strings.Index(html, "Status facts") {
		t.Error("advanced controls should follow the compact chip inside its dialog")
	}
	dialogContainer := strings.Index(html, "data-status-dialogs")
	lastList := strings.LastIndex(html, "status-category-list")
	firstDialog := strings.Index(html, "<dialog")
	if dialogContainer < 0 || dialogContainer < lastList || firstDialog < dialogContainer {
		t.Error("dialogs must render in their own container after every category list")
	}
	for _, forbidden := range []string{"<noscript", "<style>dialog.status-dialog", "document.querySelector('[data-status-board]')", "status-drag-handle", "status-color-swatch", "⠿"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("render exposed revisit-unsafe markup or handler %q", forbidden)
		}
	}
	if strings.Contains(html, "root.addEventListener('keydown'") || strings.Contains(html, "<article class=\"status-chip\"") || strings.Contains(html, "status-edit-button outline") {
		t.Error("status chips must be native buttons without nested interactive edit controls or custom keyboard handling")
	}
}
